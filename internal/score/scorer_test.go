package score

import (
	"testing"

	"github.com/anthonyalando8/prospector/internal/store"
)

func TestClassifySegment(t *testing.T) {
	s := NewScorer()

	cases := []struct {
		name    string
		company *store.Company
		signals []*store.Signal
		want    string
	}{
		{"jobboard source is always hiring", &store.Company{Source: "jobboard", Name: "Acme Software"}, nil, "hiring"},
		{"hiring_backend signal is hiring", &store.Company{Source: "manual", Name: "Acme Software"}, []*store.Signal{{Kind: "hiring_backend"}}, "hiring"},
		{"unrecognized source/signals default to agency", &store.Company{Source: "manual", Name: "Acme Software"}, nil, "agency"},
		{"a bank that's hiring is enterprise, not hiring", &store.Company{Source: "jobboard", Name: "Equity Bank Kenya"}, nil, "enterprise"},
		{"an insurer hiring via a signal is enterprise", &store.Company{Source: "manual", Name: "Britam Insurance"}, []*store.Signal{{Kind: "hiring_backend"}}, "enterprise"},
		{"a bank NOT hiring is not force-classified enterprise", &store.Company{Source: "manual", Name: "Equity Bank Kenya"}, nil, "agency"},
		{"remoteok source is hiring_us, not hiring", &store.Company{Source: "remoteok", Name: "Acme Software"}, nil, "hiring_us"},
		{"remoteok source wins over the enterprise-keyword check", &store.Company{Source: "remoteok", Name: "Acme Bank"}, nil, "hiring_us"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := s.classifySegment(c.company, c.signals); got != c.want {
				t.Errorf("classifySegment() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestIsLikelyEnterprise(t *testing.T) {
	yes := []string{"Equity Bank Kenya", "Britam Insurance", "Nairobi SACCO", "Zetech University", "Aga Khan Hospital", "Safaricom PLC"}
	for _, name := range yes {
		if !isLikelyEnterprise(name) {
			t.Errorf("isLikelyEnterprise(%q) = false, want true", name)
		}
	}

	no := []string{"Cakes & Cakes Bakery", "Flexi-Personnel", "Rapidtech Networks Limited", "SINO SOFT"}
	for _, name := range no {
		if isLikelyEnterprise(name) {
			t.Errorf("isLikelyEnterprise(%q) = true, want false", name)
		}
	}
}

func TestCalculateScore_Deterministic(t *testing.T) {
	s := NewScorer()
	signals := []*store.Signal{{Kind: "hiring_backend"}, {Kind: "checkout_present"}}

	total1, _ := s.calculateScore(signals, "hiring")
	total2, _ := s.calculateScore(signals, "hiring")
	if total1 != total2 {
		t.Errorf("calculateScore() is not deterministic: %d != %d for identical input", total1, total2)
	}
}

func TestCalculateScore_ZeroSignalsStillScoresTheBase(t *testing.T) {
	// This is the fix for the bug where a company with contacts but no signals sat stuck at
	// score=0/segment=NULL forever (Score() used to return early with no DB write at all).
	// calculateScore itself must never treat "no signals" as "no score" — it should fall back
	// cleanly to the segment's base line.
	s := NewScorer()
	total, breakdown := s.calculateScore(nil, "sme")
	if total != segmentBase["sme"] {
		t.Errorf("calculateScore(nil signals) = %d, want the sme base score %d", total, segmentBase["sme"])
	}
	if len(breakdown) != 1 {
		t.Errorf("calculateScore(nil signals) breakdown = %v, want exactly one line (the base)", breakdown)
	}
}

func TestCalculateScore_BreakdownExplainsTheTotal(t *testing.T) {
	// review.md §15: never make the UI show an unexplained number — the stored line items must
	// actually sum to the total.
	s := NewScorer()
	signals := []*store.Signal{{Kind: "hiring_backend"}, {Kind: "stale_site"}, {Kind: "checkout_present"}}

	total, breakdown := s.calculateScore(signals, "hiring")

	sum := 0
	for _, line := range breakdown {
		sum += line.Points
	}
	// calculateScore caps the total at 100 but the breakdown lines are the uncapped
	// contributions, so only compare when nothing was capped.
	if sum != total && total < 100 {
		t.Errorf("breakdown lines sum to %d but total is %d — they must match when not capped", sum, total)
	}
	if len(breakdown) != len(signals)+1 { // +1 for the base segment line
		t.Errorf("breakdown has %d lines, want %d (one base + one per recognized signal)", len(breakdown), len(signals)+1)
	}
}

func TestCalculateScore_CapsAt100(t *testing.T) {
	s := NewScorer()
	manySignals := make([]*store.Signal, 20)
	for i := range manySignals {
		manySignals[i] = &store.Signal{Kind: "hiring_backend"}
	}
	total, _ := s.calculateScore(manySignals, "hiring")
	if total != 100 {
		t.Errorf("calculateScore() with many strong signals = %d, want capped at 100", total)
	}
}

func TestCalculateScore_UnknownSignalKindsAreIgnored(t *testing.T) {
	s := NewScorer()
	base, _ := s.calculateScore(nil, "sme")
	withUnknown, _ := s.calculateScore([]*store.Signal{{Kind: "some_future_signal_kind_not_yet_scored"}}, "sme")
	if base != withUnknown {
		t.Errorf("an unrecognized signal kind changed the score (%d -> %d) — it should be silently ignored, not error or double-count", base, withUnknown)
	}
}
