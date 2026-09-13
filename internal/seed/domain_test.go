package seed

import (
	"strings"
	"testing"

	"github.com/anthonyalando8/prospector/internal/opportunity"
)

// The production defect: 180 of 212 internationally-sourced companies were stored with a guessed
// .co.ke domain, so research ran against a host that does not exist and no contact was ever
// found. This pins the corrected behaviour at the point the domain is actually chosen.
func TestDomainForSourceRespectsMarket(t *testing.T) {
	kenyan := domainForSource(opportunity.DiscoveredOpportunity{
		CompanyName: "Savannah Informatics", Source: "jobboard", Metadata: map[string]string{}})
	if !strings.HasSuffix(kenyan, ".co.ke") {
		t.Fatalf("a Kenyan-sourced company got %q; .co.ke should still be tried first there", kenyan)
	}

	for _, source := range []string{"remoteok", "weworkremotely", "remotive", "himalayas"} {
		got := domainForSource(opportunity.DiscoveredOpportunity{
			CompanyName: "Delinea", Source: source, Metadata: map[string]string{}})
		if strings.HasSuffix(got, ".co.ke") {
			t.Fatalf("%s produced %q; an international source must not guess a Kenyan domain", source, got)
		}
		if got != "delinea.com" {
			t.Fatalf("%s produced %q, want delinea.com", source, got)
		}
	}
}

// A URL the source published outranks any guess, for either market.
func TestPublishedURLOutranksGuessedDomain(t *testing.T) {
	fromEmployerURL := domainForSource(opportunity.DiscoveredOpportunity{
		CompanyName: "Wakacje.pl", Source: "remoteok",
		Metadata: map[string]string{opportunity.MetaEmployerURL: "https://www.wakacje.pl/careers"}})
	if fromEmployerURL != "wakacje.pl" {
		t.Fatalf("employer URL was ignored: got %q", fromEmployerURL)
	}

	fromApplyURL := domainForSource(opportunity.DiscoveredOpportunity{
		CompanyName: "Wonderdog", Source: "weworkremotely",
		Metadata: map[string]string{opportunity.MetaApplyURL: "https://wonderdog.ai/jobs/apply"}})
	if fromApplyURL != "wonderdog.ai" {
		t.Fatalf("apply URL was ignored: got %q", fromApplyURL)
	}

	// Even a Kenyan source defers to a published URL: a stated fact beats a guess either way.
	local := domainForSource(opportunity.DiscoveredOpportunity{
		CompanyName: "Some Kenyan Firm", Source: "jobboard",
		Metadata: map[string]string{opportunity.MetaEmployerURL: "https://somekenyanfirm.co.ke"}})
	if local != "somekenyanfirm.co.ke" {
		t.Fatalf("published Kenyan URL was ignored: got %q", local)
	}

	// A link back to the board is not the employer's site, so the guess is used instead.
	board := domainForSource(opportunity.DiscoveredOpportunity{
		CompanyName: "Delinea", Source: "remoteok",
		Metadata: map[string]string{opportunity.MetaApplyURL: "https://remoteok.com/remote-jobs/123"}})
	if board != "delinea.com" {
		t.Fatalf("a board URL was mistaken for the employer: got %q", board)
	}
}

// A company with no usable name still produces nothing rather than a nonsense domain.
func TestDomainForSourceHandlesUnusableNames(t *testing.T) {
	if got := domainForSource(opportunity.DiscoveredOpportunity{
		CompanyName: "", Source: "remoteok", Metadata: map[string]string{}}); got != "" {
		t.Fatalf("an empty company name produced %q", got)
	}
}
