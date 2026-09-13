// Package evidence holds research facts about a company as data, never as instructions.
// It records where a fact came from, groups equivalent observations so the same fact seen on
// three pages stays one fact, and states explicitly what a given detection does not establish.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// LegacyExtraction labels a pre-v2 keyword detection. Those rows are readable history; their
// text is not a source quotation and is never rewritten into one.
const LegacyExtraction = "legacy_detection"

const MaxExcerptBytes = 4096

// Fact is one stored observation about a company.
type Fact struct {
	SignalID         int64     `json:"signal_id"`
	CompanyID        int64     `json:"company_id"`
	Kind             string    `json:"kind"`
	Detail           string    `json:"detail"`
	Excerpt          string    `json:"excerpt,omitempty"`
	SourceURL        string    `json:"source_url,omitempty"`
	FactKey          string    `json:"fact_key,omitempty"`
	FactGroup        string    `json:"fact_group,omitempty"`
	ExtractionMethod string    `json:"extraction_method"`
	Confidence       string    `json:"confidence"`
	Observations     int       `json:"observations"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
	CreatedAt        time.Time `json:"created_at"`
}

func (f Fact) Legacy() bool { return f.ExtractionMethod == LegacyExtraction }

// Semantics states, per detection kind, exactly what the detection proves and the conclusions
// it can never support. Phase 4 composition reads these; nothing may claim a DoesNotEstablish
// item on the strength of the detection alone.
type Semantics struct {
	Establishes      string   `json:"establishes"`
	DoesNotEstablish []string `json:"does_not_establish,omitempty"`
	// NeedEvidence marks a detection that genuinely evidences a stated engineering need or
	// request, as opposed to the mere presence of some technology on a website.
	NeedEvidence bool `json:"need_evidence,omitempty"`
	// PaymentNeed marks a detection that evidences an actual payment-engineering need. Mere
	// acceptance of payments never sets this.
	PaymentNeed bool `json:"payment_need,omitempty"`
	// ServiceRelevance marks a detection describing what the company actually builds or sells,
	// which is what makes a service or collaboration reason truthful.
	ServiceRelevance bool `json:"service_relevance,omitempty"`
}

var kindSemantics = map[string]Semantics{
	"checkout_present": {
		Establishes: "the site presents checkout or payment functionality",
		DoesNotEstablish: []string{
			"the recipient has a payment problem",
			"the recipient needs a payment integration",
			"the recipient has reconciliation failures",
			"the recipient wants payment work outsourced",
		},
	},
	"paybill_listed": {
		Establishes: "the company accepts M-Pesa payments",
		DoesNotEstablish: []string{
			"the recipient has a payment problem",
			"the recipient needs M-Pesa integration work",
			"the recipient has reconciliation failures",
		},
	},
	"payment_complaint": {
		Establishes:  "a payment problem was reported in the source text",
		PaymentNeed:  true,
		NeedEvidence: true,
	},
	"engineering_request": {
		Establishes:  "the company states an engineering need or request in its own words",
		NeedEvidence: true,
	},
	"offers_tech_services": {
		Establishes:      "the site markets software or web development as a service",
		ServiceRelevance: true,
		DoesNotEstablish: []string{"the company needs outside development help"},
	},
	"product_description": {
		Establishes:      "the company describes a product or service in its own words",
		ServiceRelevance: true,
	},
	"active_site": {
		Establishes:      "a recent year string appears in the page text",
		DoesNotEstablish: []string{"the site is actively maintained", "the business is currently trading"},
	},
	"stale_site": {
		Establishes:      "only older year strings appear in the page text",
		DoesNotEstablish: []string{"the site is abandoned", "the company needs a rebuild"},
	},
	"hiring_backend": {
		Establishes:      "a legacy discovery run recorded a hiring listing snippet",
		NeedEvidence:     true,
		DoesNotEstablish: []string{"the advertised role is an engineering role", "the vacancy is still open"},
	},
}

// SemanticsFor returns what a detection kind establishes. Unrecognized kinds, including every
// tech: keyword detection, establish presence in page text and nothing more.
func SemanticsFor(kind string) Semantics {
	if s, ok := kindSemantics[kind]; ok {
		return s
	}
	if strings.HasPrefix(kind, "tech:") {
		return Semantics{
			Establishes: "the term " + strings.TrimPrefix(kind, "tech:") + " appears in the page source",
			DoesNotEstablish: []string{
				"the company needs help with this technology",
				"this is the company's production stack",
			},
		}
	}
	return Semantics{Establishes: "the detection was recorded from page text"}
}

// NormalizeText collapses whitespace and case so the same sentence observed twice produces one
// fact key. It deliberately does not stem or paraphrase.
func NormalizeText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// FactKey identifies one canonical fact for a company. Equal kind and equal normalized detail
// is the same fact regardless of which page it was seen on, which is what stops a site's
// footer text from being counted once per page.
func FactKey(kind, detail string) string {
	sum := sha256.Sum256([]byte(NormalizeText(kind) + "\x00" + NormalizeText(detail)))
	return hex.EncodeToString(sum[:16])
}

// GroupKey identifies a set of facts that mean the same thing. Same kind and same normalized
// detail group together; so do the legacy duplicate rows that were preserved verbatim.
func GroupKey(kind, detail string) string { return FactKey(kind, detail) }

// Group is a set of equivalent observations counted once by relevance and selection.
type Group struct {
	Key          string    `json:"key"`
	Kind         string    `json:"kind"`
	Detail       string    `json:"detail"`
	Excerpt      string    `json:"excerpt,omitempty"`
	SourceURLs   []string  `json:"source_urls,omitempty"`
	SignalIDs    []int64   `json:"signal_ids"`
	Observations int       `json:"observations"`
	Legacy       bool      `json:"legacy"`
	Confidence   string    `json:"confidence"`
	Semantics    Semantics `json:"semantics"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

// Cited returns the source references a later composer may use for this group.
func (g Group) Cited() []string {
	refs := make([]string, 0, len(g.SignalIDs))
	for _, id := range g.SignalIDs {
		refs = append(refs, "evidence:"+itoa(id))
	}
	return refs
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// GroupFacts collapses equivalent observations. Repeated rows raise the observation count and
// extend LastSeen; they never produce an additional group, because seeing the same sentence on
// three pages is one fact observed three times, not three independent facts.
func GroupFacts(facts []Fact) []Group {
	index := map[string]*Group{}
	order := []string{}
	for _, f := range facts {
		key := f.FactGroup
		if key == "" {
			key = GroupKey(f.Kind, f.Detail)
		}
		g, ok := index[key]
		if !ok {
			g = &Group{
				Key: key, Kind: f.Kind, Detail: f.Detail, Excerpt: f.Excerpt,
				Legacy: f.Legacy(), Confidence: f.Confidence, Semantics: SemanticsFor(f.Kind),
				FirstSeen: f.firstSeen(), LastSeen: f.lastSeen(),
			}
			index[key] = g
			order = append(order, key)
		}
		g.SignalIDs = append(g.SignalIDs, f.SignalID)
		g.Observations += max(f.Observations, 1)
		// A group is a verbatim excerpt only if every member is; one legacy detection in the
		// group keeps the whole group labelled legacy.
		if f.Legacy() {
			g.Legacy = true
		}
		if g.Excerpt == "" && f.Excerpt != "" {
			g.Excerpt = f.Excerpt
		}
		if f.SourceURL != "" && !contains(g.SourceURLs, f.SourceURL) {
			g.SourceURLs = append(g.SourceURLs, f.SourceURL)
		}
		if s := f.firstSeen(); !s.IsZero() && (g.FirstSeen.IsZero() || s.Before(g.FirstSeen)) {
			g.FirstSeen = s
		}
		if s := f.lastSeen(); s.After(g.LastSeen) {
			g.LastSeen = s
		}
		if confidenceRank(f.Confidence) > confidenceRank(g.Confidence) {
			g.Confidence = f.Confidence
		}
	}
	groups := make([]Group, 0, len(order))
	for _, key := range order {
		g := index[key]
		sort.Slice(g.SignalIDs, func(i, j int) bool { return g.SignalIDs[i] < g.SignalIDs[j] })
		sort.Strings(g.SourceURLs)
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Kind != groups[j].Kind {
			return groups[i].Kind < groups[j].Kind
		}
		return groups[i].Key < groups[j].Key
	})
	return groups
}

func (f Fact) firstSeen() time.Time {
	if !f.FirstSeen.IsZero() {
		return f.FirstSeen
	}
	return f.CreatedAt
}

func (f Fact) lastSeen() time.Time {
	if !f.LastSeen.IsZero() {
		return f.LastSeen
	}
	return f.CreatedAt
}

func confidenceRank(c string) int {
	switch c {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
