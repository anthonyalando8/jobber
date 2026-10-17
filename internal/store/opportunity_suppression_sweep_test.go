package store

import (
	"context"
	"testing"
	"time"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

// sweptBasis finds what the sweep concluded about one opportunity, or "" if it left it alone.
// The sweep runs over the whole workspace, so a test asserts about its own fixture rather than
// about the total.
func sweptBasis(result SuppressionSweepResult, opportunityID int64) string {
	for _, candidate := range result.Suppressed {
		if candidate.OpportunityID == opportunityID {
			return candidate.Basis
		}
	}
	return ""
}

// sweep runs the real sweep, with the fixture's own sources declared enumerable. Tests must never
// borrow a production source name such as "remoteok": a fresh fixture under that name would make
// every real posting from that source an absence candidate, and the sweep would suppress them.
func sweep(t *testing.T, db *DB, policy SuppressionPolicy, enumerable ...string) SuppressionSweepResult {
	t.Helper()
	policy.EnumerableSources = enumerable
	if len(enumerable) == 0 {
		// No source enumerates, so the absence rule cannot fire at all.
		policy.EnumerableSources = []string{"no-such-source-" + t.Name()}
	}
	result, err := db.SweepOpportunitySuppressions(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range result.Errors {
		t.Errorf("the sweep reported an error: %v", failure)
	}
	return result
}

// expires_at was stored by every adapter and read by nothing, so a posting whose closing date
// passed months ago stayed in the priority list, the dashboard queue and contact discovery. A
// source that says 'closed' was equally ignored.
func TestTheSweepSuppressesPostingsTheSourceSaysAreOver(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lastWeek := time.Now().Add(-7 * 24 * time.Hour)

	expired, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{ExpiresAt: &lastWeek})
	closed, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{Availability: "closed"})
	live, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{})

	result := sweep(t, db, DefaultSuppressionPolicy())

	if got := sweptBasis(result, expired); got != BasisExpiryPassed {
		t.Errorf("a posting past its closing date was not suppressed on that basis: %q", got)
	}
	if got := sweptBasis(result, closed); got != BasisSourceClosed {
		t.Errorf("a posting the source marked closed was not suppressed on that basis: %q", got)
	}
	if got := sweptBasis(result, live); got != "" {
		t.Errorf("an open, currently-listed posting was suppressed as %q", got)
	}
	if selectionStatus(t, db, live) != "new" {
		t.Error("a live posting was taken out of the working queues")
	}
	if selectionStatus(t, db, expired) != "ignored" {
		t.Error("the expired posting is still in the working queues")
	}

	// The conclusion has to match the evidence: an expiry date that passed is expiry, and a feed
	// saying closed is the employer withdrawing the posting. They are not interchangeable.
	current, err := db.CurrentOpportunitySuppression(ctx, closed)
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.Reason != SuppressWithdrawnBySource {
		t.Errorf("wrong conclusion recorded for a source-closed posting: %+v", current)
	}
	if current.DecidedBy != SuppressionSweepActor || !current.Automatic() {
		t.Errorf("an automatic decision does not identify itself as one: %+v", current)
	}
	if current.Evidence["availability_status"] != "closed" {
		t.Errorf("the decision did not record the fact it relied on: %+v", current.Evidence)
	}
}

// A posting that stops appearing in a feed has almost certainly been taken down — but only if the
// feed is still being read. This is the property that keeps the inference honest: if a source
// breaks, is rate-limited or is removed from the seed registry, every posting it ever found goes
// stale at once, and concluding "no longer hiring" from that would quietly empty the workspace.
func TestTheSweepWillNotInferAbsenceFromASourceThatStoppedBeingRead(t *testing.T) {
	db := testDB(t)
	longAgo := time.Now().Add(-90 * 24 * time.Hour)

	// A source whose only posting is old. Nothing has been seen from it recently, so its silence
	// about this posting says nothing about the posting.
	deadSource, _ := makeSuppressibleOpportunity(t, db,
		suppressionFixture{Source: "abandoned-board", LastSeen: longAgo})

	// A source still producing sightings. One of its postings stopped appearing weeks before the
	// others, which is evidence about that posting rather than about the feed.
	healthy := "healthy-board-" + t.Name()
	vanished, _ := makeSuppressibleOpportunity(t, db,
		suppressionFixture{Source: healthy, LastSeen: longAgo})
	stillListed, _ := makeSuppressibleOpportunity(t, db,
		suppressionFixture{Source: healthy, LastSeen: time.Now()})

	// Both sources enumerate, so the only thing that can separate them is freshness.
	result := sweep(t, db, DefaultSuppressionPolicy(), "abandoned-board", healthy)

	if got := sweptBasis(result, deadSource); got != "" {
		t.Errorf("a posting was suppressed as %q because its source went quiet, not because the posting did", got)
	}
	if selectionStatus(t, db, deadSource) != "new" {
		t.Error("a broken source emptied the queue of its own postings")
	}
	if got := sweptBasis(result, vanished); got != BasisAbsentFromSource {
		t.Errorf("a posting missing for 90 days from a live feed was not suppressed: %q", got)
	}
	if got := sweptBasis(result, stillListed); got != "" {
		t.Errorf("a currently-listed posting was suppressed as %q", got)
	}
}

// The grace period has to mean something. A posting absent for a couple of days is a feed hiccup,
// a paused listing or a crawl that missed a page, not a closed vacancy.
func TestTheSweepWaitsOutTheGracePeriodBeforeInferringAbsence(t *testing.T) {
	db := testDB(t)
	source := "grace-board-" + t.Name()
	recentlyMissing, _ := makeSuppressibleOpportunity(t, db,
		suppressionFixture{Source: source, LastSeen: time.Now().Add(-3 * 24 * time.Hour)})
	makeSuppressibleOpportunity(t, db, suppressionFixture{Source: source, LastSeen: time.Now()})

	result := sweep(t, db, DefaultSuppressionPolicy(), source)
	if got := sweptBasis(result, recentlyMissing); got != "" {
		t.Errorf("a posting missing for three days was suppressed as %q", got)
	}

	// With a grace period short enough to have elapsed, the same posting is suppressed — so the
	// test above is the grace period working, not the absence rule failing to fire at all.
	result = sweep(t, db, SuppressionPolicy{AbsenceGrace: time.Hour, SourceFreshness: 48 * time.Hour}, source)
	if got := sweptBasis(result, recentlyMissing); got != BasisAbsentFromSource {
		t.Errorf("a shorter grace period did not reach the same posting: %q", got)
	}
}

// An operator who puts a posting back has overruled the sweep's reasoning. If the next tick
// suppressed it again on the same basis the operator could not win, and the reinstate button
// would be a lie.
func TestTheSweepDoesNotUndoAnOperatorsReinstatement(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lastWeek := time.Now().Add(-7 * 24 * time.Hour)
	id, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{ExpiresAt: &lastWeek})

	if got := sweptBasis(sweep(t, db, DefaultSuppressionPolicy()), id); got != BasisExpiryPassed {
		t.Fatalf("the fixture was not suppressed automatically: %q", got)
	}
	if _, err := db.ReinstateOpportunity(ctx, id, "operator", "employer confirmed it is still open"); err != nil {
		t.Fatal(err)
	}

	if got := sweptBasis(sweep(t, db, DefaultSuppressionPolicy()), id); got != "" {
		t.Errorf("the sweep re-suppressed a reinstated posting as %q", got)
	}
	if got := selectionStatus(t, db, id); got != "new" {
		t.Errorf("the reinstated posting was taken out of the queues again: %q", got)
	}

	// A newer source revision is new information, so the sweep is allowed to look again. Without
	// this the exclusion would be permanent and a genuinely expired posting could never be
	// suppressed after one reinstatement.
	if _, err := db.Exec(ctx,
		`INSERT INTO opportunity_revision(opportunity_id,revision,document,content_hash)
		 VALUES($1,2,'{"title":"Backend Engineer"}','hash-2')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE opportunity SET current_revision=2 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got := sweptBasis(sweep(t, db, DefaultSuppressionPolicy()), id); got != BasisExpiryPassed {
		t.Errorf("a newer source revision did not let the sweep reconsider: %q", got)
	}
}

// The sweep never revises a decision that already stands, whoever made it.
func TestTheSweepLeavesAnExistingDecisionAlone(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lastWeek := time.Now().Add(-7 * 24 * time.Hour)
	id, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{ExpiresAt: &lastWeek})

	if _, err := db.SuppressOpportunity(ctx, id, SuppressNotPursuing, BasisOperator,
		"not worth applying", "operator", nil); err != nil {
		t.Fatal(err)
	}
	if got := sweptBasis(sweep(t, db, DefaultSuppressionPolicy()), id); got != "" {
		t.Errorf("the sweep wrote over a standing operator decision as %q", got)
	}
	current, err := db.CurrentOpportunitySuppression(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.Reason != SuppressNotPursuing || current.Note != "not worth applying" {
		t.Errorf("the operator's reason was replaced: %+v", current)
	}
}

// Candidates can be inspected without anything being written, so a first run over a real backlog
// can be read before it is trusted.
func TestCandidatesCanBeInspectedWithoutSuppressingAnything(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	lastWeek := time.Now().Add(-7 * 24 * time.Hour)
	id, _ := makeSuppressibleOpportunity(t, db, suppressionFixture{ExpiresAt: &lastWeek})

	candidates, err := db.OpportunitySuppressionCandidates(ctx, DefaultSuppressionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		found = found || candidate.OpportunityID == id
	}
	if !found {
		t.Error("the expired posting was not reported as a candidate")
	}
	if got := selectionStatus(t, db, id); got != "new" {
		t.Errorf("inspecting candidates changed an opportunity: %q", got)
	}
	if current, err := db.CurrentOpportunitySuppression(ctx, id); err != nil || current != nil {
		t.Errorf("inspecting candidates wrote a decision: %v %v", current, err)
	}
}

// Most of this system's sources are read as a keyword search or a truncated feed: one page of
// results for "backend", or the most recent items in an RSS category. A posting leaves such a
// read when newer postings push it out, while remaining perfectly open. Inferring "no longer
// hiring" from that would mark live vacancies dead, which is the one error this mechanism must
// not make — so absence is only evidence for a source that enumerates its whole listing.
//
// This is not hypothetical. On the real workspace the first dry run proposed 26 postings on this
// basis, every one of them from a single-page keyword scrape.
func TestTheSweepWillNotInferAbsenceFromASourceThatDoesNotEnumerate(t *testing.T) {
	db := testDB(t)
	searchScraped := "keyword-scraper-" + t.Name()

	// The source was read seconds ago and this posting has been missing from it for 90 days —
	// every condition the absence rule tests, except that the read is not a complete listing.
	pushedOffPageOne, _ := makeSuppressibleOpportunity(t, db,
		suppressionFixture{Source: searchScraped, LastSeen: time.Now().Add(-90 * 24 * time.Hour)})
	makeSuppressibleOpportunity(t, db, suppressionFixture{Source: searchScraped, LastSeen: time.Now()})

	// Swept with some other source declared enumerable: this one is not.
	if got := sweptBasis(sweep(t, db, DefaultSuppressionPolicy(), "some-complete-feed"), pushedOffPageOne); got != "" {
		t.Errorf("a posting was declared no longer hiring because it fell off a search page: %q", got)
	}
	if got := selectionStatus(t, db, pushedOffPageOne); got != "new" {
		t.Errorf("a live posting was taken out of the working queues: %q", got)
	}

	// Declaring that same source enumerable is what makes the rule apply, so the test above is
	// the enumeration guard working rather than the fixture failing to qualify some other way.
	if got := sweptBasis(sweep(t, db, DefaultSuppressionPolicy(), searchScraped), pushedOffPageOne); got != BasisAbsentFromSource {
		t.Errorf("an enumerating source did not reach the same posting: %q", got)
	}
}

// The shipped default must not include a source that is read as a keyword search or a truncated
// feed, because that is exactly how live postings would get marked dead.
func TestOnlyCompleteListingsAreDeclaredEnumerable(t *testing.T) {
	for _, mustNotEnumerate := range []string{
		"myjobmag.co.ke",       // one page of results per search term
		"brightermonday.co.ke", // likewise
		"weworkremotely",       // category RSS, truncated to recent items
	} {
		if opportunity.SourceEnumerates(mustNotEnumerate) {
			t.Errorf("%s is read as a partial listing but is declared enumerable, so the sweep "+
				"would suppress live postings that merely fell out of view", mustNotEnumerate)
		}
	}
	if len(opportunity.EnumerableSources) == 0 {
		t.Error("no source enumerates, so the absence rule can never fire")
	}
}
