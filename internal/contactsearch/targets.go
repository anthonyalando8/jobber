package contactsearch

import (
	"strings"

	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/relevance"
)

// Query is one search the pipeline intends to run, with the reason it is being run. The purpose
// is stored so an operator reading a result can see what question produced it.
type Query struct {
	Text    string `json:"text"`
	Purpose string `json:"purpose"`
}

// Strategy is what this search is looking for on this company, for this purpose.
type Strategy struct {
	Intent Intent `json:"intent"`
	// PreferredFunctions are the recipient functions worth finding, strongest first. They are
	// the existing relevance function names, so the ranking here and the recipient policy's
	// ranking are talking about the same things.
	PreferredFunctions []string `json:"preferred_functions"`
	// AcceptsApplicationRoute reports whether an apply form or ATS link is a valid answer.
	// For a job application it usually is. For service outreach it never is: an ATS is where
	// candidates apply, and sending a services pitch into a hiring queue is both useless and
	// rude.
	AcceptsApplicationRoute bool    `json:"accepts_application_route"`
	Queries                 []Query `json:"queries"`
}

// Intent mirrors the three authoritative intents. It is an alias of plan.Intent rather than a
// second vocabulary, so a new intent cannot be added here without the plan package agreeing.
type Intent = plan.Intent

// StrategyFor builds the search plan for one company under one purpose.
//
// The three purposes want genuinely different people, and searching for all of them every time
// would both waste the budget and produce recipients that are wrong for the purpose at hand.
// Where no intent is established, the fallback is the employment-side strategy without an
// application route, because that is what the strongest opportunity context supports — and an
// intent is never invented to justify a broader search.
func StrategyFor(intent Intent, companyName, companyDomain string) Strategy {
	name := strings.TrimSpace(companyName)
	quoted := "\"" + name + "\""
	site := ""
	if domain := normalizeHost(companyDomain); domain != "" {
		site = " site:" + domain
	}

	switch intent {
	case plan.SpecificJobApplication:
		// Applying to an advertised vacancy. The employer's own stated route is the answer if
		// one exists; a recruiter is the answer if it does not.
		return Strategy{
			Intent:                  intent,
			PreferredFunctions:      []string{relevance.FunctionRecruiting, relevance.FunctionEngineering, relevance.FunctionLeadership},
			AcceptsApplicationRoute: true,
			Queries: []Query{
				{quoted + " careers", "the employer's own careers page, where an application route is usually published"},
				{quoted + " careers email", "an application address the employer publishes"},
				{quoted + " jobs email", "an application address published alongside its vacancies"},
				{quoted + " recruiter", "a named recruiter who can receive an application"},
				{quoted + " talent acquisition", "the team that handles applications"},
				{strings.TrimSpace("careers apply" + site), "an application route on the employer's own domain"},
			},
		}
	case plan.ServiceOutreach:
		// Offering engineering services. The audience is someone who can judge or commission
		// technical work. A recruiting mailbox is the wrong door, and an ATS is not a door at
		// all.
		return Strategy{
			Intent:                  intent,
			PreferredFunctions:      []string{relevance.FunctionEngineering, relevance.FunctionLeadership},
			AcceptsApplicationRoute: false,
			Queries: []Query{
				{quoted + " head of engineering", "a technical decision maker who can judge an engineering proposal"},
				{quoted + " CTO", "the technical leader of the company"},
				{quoted + " engineering manager", "an engineering decision maker"},
				{quoted + " leadership team", "the employer's own statement of who leads it"},
				{strings.TrimSpace("team leadership" + site), "the employer's own team page"},
			},
		}
	default:
		// Speculative employment enquiry, and the fallback when no intent is established.
		return Strategy{
			Intent:                  intent,
			PreferredFunctions:      []string{relevance.FunctionRecruiting, relevance.FunctionEngineering, relevance.FunctionLeadership},
			AcceptsApplicationRoute: false,
			Queries: []Query{
				{quoted + " recruiter", "a recruiter who can act on an opportunities enquiry"},
				{quoted + " talent acquisition", "the team responsible for hiring"},
				{quoted + " careers", "the employer's own careers page"},
				{quoted + " head of engineering", "an engineering leader who can judge relevance"},
				{quoted + " engineering manager", "an engineering hiring contact"},
				{strings.TrimSpace("careers team" + site), "the employer's own pages about its people"},
			},
		}
	}
}

// RankForIntent orders candidates for a purpose, strongest first.
//
// The ordering it produces is a presentation order for the operator and a promotion order for
// the pipeline. It is not a send decision: plan.RankRecipients remains the only thing that says
// whether a contact may receive a message, and it is applied separately to whatever this
// promotes.
func RankForIntent(strategy Strategy, candidates []Candidate) []Candidate {
	rank := map[string]int{}
	for i, function := range strategy.PreferredFunctions {
		rank[function] = len(strategy.PreferredFunctions) - i
	}
	score := func(c Candidate) int {
		total := 0
		if c.Kind == KindApplicationURL || c.Kind == KindApplicationEmail {
			if strategy.AcceptsApplicationRoute {
				total += 1000 // the employer's own stated route wins outright when it applies
			} else {
				total -= 500 // and is actively wrong for outreach that is not an application
			}
		}
		total += rank[c.Function] * 50
		switch c.Confidence {
		case ConfidenceHigh:
			total += 60
		case ConfidenceMedium:
			total += 30
		}
		if c.PersonName != "" {
			total += 25
		}
		if c.Inferred() {
			total -= 200 // a derived address never outranks one somebody published
		}
		if c.Email != "" && c.EndpointContactable {
			total += 20
		}
		total += c.BestTrust() / 10
		return total
	}
	ordered := append([]Candidate(nil), candidates...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && score(ordered[j]) > score(ordered[j-1]); j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	return ordered
}

// CareerPagePaths are the employer-owned paths worth reading directly. Fetching these needs no
// search engine at all, which matters: the highest-trust evidence in this pipeline is reachable
// even when no search provider is configured.
//
// Each entry is a distinct page, not another spelling of one already listed. "/careers" and
// "/careers/" are the same document, and listing both spent two of the page budget and recorded
// a single observation twice, as though two sources had independently said it.
var CareerPagePaths = []string{
	"/careers", "/jobs", "/join-us", "/about/careers", "/company/careers",
	"/team", "/about/team", "/about-us/team", "/people", "/leadership", "/about",
	"/contact", "/company/jobs", "/work-with-us",
}
