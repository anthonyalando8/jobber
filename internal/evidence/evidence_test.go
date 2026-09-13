package evidence

import (
	"testing"
	"time"
)

func fact(id int64, kind, detail, url string, seen time.Time) Fact {
	return Fact{SignalID: id, CompanyID: 1, Kind: kind, Detail: detail, SourceURL: url,
		ExtractionMethod: "site_text_v1", Confidence: "medium", Observations: 1,
		FirstSeen: seen, LastSeen: seen, CreatedAt: seen}
}

// J. The same fact seen on three pages is one fact observed three times.
func TestSameFactOnSeveralPagesIsOneGroup(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	groups := GroupFacts([]Fact{
		fact(1, "offers_tech_services", "Site markets software development as a service", "https://example.test/", base),
		fact(2, "offers_tech_services", "Site markets  software development   as a service", "https://example.test/about", base.Add(time.Hour)),
		fact(3, "offers_tech_services", "SITE MARKETS SOFTWARE DEVELOPMENT AS A SERVICE", "https://example.test/services", base.Add(2*time.Hour)),
	})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Observations != 3 {
		t.Fatalf("observations = %d, want 3", g.Observations)
	}
	if len(g.SourceURLs) != 3 {
		t.Fatalf("source URLs = %v, want all three pages recorded", g.SourceURLs)
	}
	if len(g.Cited()) != 3 {
		t.Fatalf("all original signal ids must stay citable, got %v", g.Cited())
	}
	if !g.LastSeen.After(g.FirstSeen) {
		t.Fatal("repeated observation should extend last seen without moving first seen")
	}
	if !g.FirstSeen.Equal(base) {
		t.Fatalf("first seen = %v, want the earliest observation %v", g.FirstSeen, base)
	}
}

// Distinct facts stay distinct.
func TestDifferentFactsStayInDifferentGroups(t *testing.T) {
	now := time.Now()
	groups := GroupFacts([]Fact{
		fact(1, "checkout_present", "Checkout or payment functionality is present on the site", "https://a.test/", now),
		fact(2, "paybill_listed", "The site lists an M-Pesa paybill or till", "https://a.test/", now),
	})
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
}

// Preserved legacy duplicates group together and stay labelled as detections.
func TestLegacyDuplicatesGroupAndStayLabelled(t *testing.T) {
	now := time.Now()
	legacy := func(id int64) Fact {
		f := fact(id, "tech:react", "Found react in site code", "https://a.test/", now)
		f.ExtractionMethod = LegacyExtraction
		return f
	}
	groups := GroupFacts([]Fact{legacy(1), legacy(2), legacy(3)})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if !groups[0].Legacy {
		t.Fatal("a group built from legacy detections must be labelled legacy")
	}
	if groups[0].Detail != "Found react in site code" {
		t.Fatalf("legacy detection text must be preserved verbatim, got %q", groups[0].Detail)
	}
}

// Presence detections must state what they cannot establish.
func TestPresenceDetectionsDisclaimPaymentProblems(t *testing.T) {
	for _, kind := range []string{"checkout_present", "paybill_listed"} {
		s := SemanticsFor(kind)
		if s.PaymentNeed {
			t.Fatalf("%s must never evidence a payment need", kind)
		}
		if s.NeedEvidence {
			t.Fatalf("%s must never evidence an engineering need", kind)
		}
		if len(s.DoesNotEstablish) == 0 {
			t.Fatalf("%s must state the conclusions it cannot support", kind)
		}
	}
	if !SemanticsFor("payment_complaint").PaymentNeed {
		t.Fatal("an actual reported payment problem should evidence a payment need")
	}
	if SemanticsFor("tech:react").NeedEvidence {
		t.Fatal("a technology detected in page source is not evidence of a need")
	}
}
