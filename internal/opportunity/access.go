package opportunity

import (
	"net/url"
	"regexp"
	"strings"
)

// Access answers the two practical questions a listing raises before fit even matters: can
// someone applying from Kenya take this job, and how does one apply. Both are read only from what
// the source published. Nothing is assumed in either direction: a listing that does not say who
// may apply is "unclear", not "worldwide", and an intermediary is not "paid" without evidence.
type Access struct {
	// Eligibility is the geographic scope the listing states.
	Eligibility string `json:"eligibility"`
	// KenyaAccess is whether an applicant in Kenya is within that scope.
	KenyaAccess string `json:"kenya_access"`
	// EligibilityEvidence is the text the decision rests on.
	EligibilityEvidence string `json:"eligibility_evidence,omitempty"`
	// Route is how one applies or makes contact.
	Route string `json:"route"`
	// RouteURL is where that route starts, when there is a link.
	RouteURL string `json:"route_url,omitempty"`
	// Cost is whether applying costs anything or needs an account.
	Cost string `json:"cost"`
	// CostEvidence is the text a registration or payment requirement was read from.
	CostEvidence string `json:"cost_evidence,omitempty"`
	// Contract is true when the listing is contract, freelance or part-time work: the kind of
	// short engagement that converts fastest into paid work.
	Contract bool `json:"contract"`
}

// AccessClassifierVersion changes whenever the rules below change, so stored classifications
// are recomputed rather than trusted.
const AccessClassifierVersion = 1

const (
	EligibilityWorldwide  = "worldwide"
	EligibilityRegion     = "region_restricted"
	EligibilityCountry    = "country_specific"
	EligibilityUnclear    = "unclear"
	KenyaOpen             = "open"
	KenyaLikely           = "likely"
	KenyaRestricted       = "restricted"
	KenyaUnknown          = "unknown"
	RouteDirectContact    = "direct_contact"
	RouteEmployerSite     = "employer_site"
	RouteEmployerATS      = "employer_ats"
	RouteJobBoard         = "job_board"
	RouteUnknown          = "unknown"
	CostFree              = "free"
	CostRegistration      = "registration"
	CostPaid              = "paid"
	CostUnknown           = "unknown"
	kenyanSourceSuffix    = ".co.ke"
	maxEvidenceCharacters = 160
)

var (
	worldwidePhrases = []string{"worldwide", "anywhere in the world", "anywhere", "work from anywhere", "global", "all countries", "any location", "any country", "international"}
	// Regions that include Kenya. EMEA and "Africa" include it; "Europe" does not.
	kenyaRegions = []string{"kenya", "nairobi", "east africa", "africa", "emea", "sub-saharan", "eat timezone", "gmt+3", "utc+3"}
	// Regions and countries that do not include Kenya.
	otherRegions = []string{"europe", "european union", "eu only", "eea", "americas", "north america", "latin america", "latam", "south america", "apac", "asia", "oceania", "cet", "est timezone", "pst", "us time zones", "uk time"}
	countries    = []string{"us", "usa", "united states", "u.s.", "us only", "us-only", "canada", "united kingdom", "uk only", "uk", "australia", "new zealand", "germany", "france", "spain", "portugal", "netherlands", "poland", "ireland", "india", "brazil", "mexico", "argentina", "colombia", "philippines", "singapore", "japan", "south africa", "nigeria", "egypt", "israel", "sweden", "switzerland", "italy", "romania", "ukraine", "pakistan", "vietnam", "indonesia", "turkey", "denmark", "norway", "finland", "belgium", "austria", "czech", "greece", "hungary", "estonia", "latvia", "lithuania", "bulgaria", "croatia"}
	// Statements in the description that restrict who may apply, whatever the location field says.
	restrictionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(must|need to|required to) (be|reside|live)( currently)? (based|located|living|residing)? ?in (the )?(us|usa|united states|uk|united kingdom|canada|europe|eu|australia)\b`),
		regexp.MustCompile(`(?i)\b(authori[sz]ed|eligible|legally able) to work in (the )?(us|usa|united states|uk|united kingdom|canada|eu|european union|australia)\b`),
		regexp.MustCompile(`(?i)\b(us|u\.s\.) citizens?(hip)? (only|required)\b`),
		regexp.MustCompile(`(?i)\b(security clearance|green card)\b`),
		regexp.MustCompile(`(?i)\bonly (accept|consider|hire|hiring)( applicants| candidates)? (from|in|based in) (the )?(us|usa|united states|uk|europe|eu|canada|north america|latam|latin america)\b`),
	}
	// Statements that the employer hires across borders.
	openPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(hire|hiring|open to|welcome) (candidates|applicants|people|talent)? ?(from )?(anywhere|worldwide|globally|all over the world)\b`),
		regexp.MustCompile(`(?i)\b(fully remote|100% remote),? (anywhere|worldwide|globally)\b`),
		regexp.MustCompile(`(?i)\bremote[- ]first,? (team )?(across|around) the (world|globe)\b`),
		regexp.MustCompile(`(?i)\b(independent )?contractors? (from|in) any country\b`),
	}
	registrationPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(log ?in|sign ?in|sign up|register|create an account) to (apply|view|see)\b`),
		regexp.MustCompile(`(?i)\b(registration|an account) (is )?required to apply\b`),
	}
	paidPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(premium|paid|pro) (members?|membership|subscribers?|subscription)( only| required)?\b.{0,40}\bapply\b`),
		regexp.MustCompile(`(?i)\bapply\b.{0,40}\b(premium|paid) (members?|membership|subscription)\b`),
		regexp.MustCompile(`(?i)\b(upgrade|subscribe) to apply\b`),
		regexp.MustCompile(`(?i)\b(buy|purchase|spend) (credits|connects|tokens) to apply\b`),
		regexp.MustCompile(`(?i)\bapplication fee\b`),
	}
	contractPattern = regexp.MustCompile(`(?i)\b(contract|contractor|freelance|freelancer|part[- ]time|temporary|fixed[- ]term|short[- ]term|project[- ]based|consultant|b2b)\b`)
	atsHosts        = []string{"greenhouse.io", "lever.co", "ashbyhq.com", "workable.com", "breezy.hr", "bamboohr.com", "recruitee.com", "smartrecruiters.com", "jobvite.com", "myworkdayjobs.com", "workday.com", "teamtailor.com", "personio.de", "personio.com", "pinpointhq.com", "homerun.co", "jazzhr.com", "applytojob.com"}
	boardHosts      = []string{"remoteok.com", "weworkremotely.com", "remotive.com", "himalayas.app", "jobicy.com", "workingnomads.com", "brightermonday.co.ke", "myjobmag.co.ke", "linkedin.com", "indeed.com", "glassdoor.com", "wellfound.com", "angel.co"}
)

// ClassifyAccess reads a listing's eligibility and application route from its published facts.
func ClassifyAccess(d DiscoveredOpportunity) Access {
	a := Access{}
	a.Eligibility, a.KenyaAccess, a.EligibilityEvidence = classifyEligibility(d)
	a.Route, a.RouteURL = classifyRoute(d)
	a.Cost, a.CostEvidence = classifyCost(d, a.Route)
	a.Contract = contractPattern.MatchString(d.EmploymentType) || contractPattern.MatchString(d.Title)
	return a
}

func classifyEligibility(d DiscoveredOpportunity) (string, string, string) {
	location := strings.ToLower(strings.TrimSpace(d.Location))
	description := d.Description
	// A Kenyan board lists Kenyan jobs: local, so open to a Kenyan applicant.
	if strings.HasSuffix(strings.ToLower(d.Source), kenyanSourceSuffix) {
		return EligibilityCountry, KenyaOpen, "listed on a Kenyan job board"
	}
	// An explicit restriction in the text outranks a hopeful location field such as "Remote".
	for _, pattern := range restrictionPatterns {
		if m := pattern.FindString(description); m != "" {
			return EligibilityCountry, KenyaRestricted, evidence(m)
		}
	}
	if location != "" {
		if containsAnyWord(location, kenyaRegions) {
			if strings.Contains(location, "kenya") || strings.Contains(location, "nairobi") {
				return EligibilityCountry, KenyaOpen, evidence(d.Location)
			}
			return EligibilityRegion, KenyaLikely, evidence(d.Location)
		}
		if containsAnyWord(location, worldwidePhrases) && !containsAnyWord(location, otherRegions) && !containsAnyWord(location, countries) {
			return EligibilityWorldwide, KenyaOpen, evidence(d.Location)
		}
		if containsAnyWord(location, otherRegions) {
			return EligibilityRegion, KenyaRestricted, evidence(d.Location)
		}
		if containsAnyWord(location, countries) {
			return EligibilityCountry, KenyaRestricted, evidence(d.Location)
		}
	}
	for _, pattern := range openPatterns {
		if m := pattern.FindString(description); m != "" {
			return EligibilityWorldwide, KenyaOpen, evidence(m)
		}
	}
	if location == "" || location == "remote" {
		return EligibilityUnclear, KenyaUnknown, ""
	}
	return EligibilityUnclear, KenyaUnknown, evidence(d.Location)
}

// containsAnyWord matches phrases on word boundaries, so "us" does not match "business" and
// "eu" does not match "museum".
func containsAnyWord(text string, phrases []string) bool {
	for _, phrase := range phrases {
		if wordMatch(text, phrase) {
			return true
		}
	}
	return false
}

func wordMatch(text, phrase string) bool {
	for start := 0; start <= len(text)-len(phrase); {
		i := strings.Index(text[start:], phrase)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(phrase)
		if (i == 0 || !isWordByte(text[i-1])) && (end == len(text) || !isWordByte(text[end])) {
			return true
		}
		start = i + 1
	}
	return false
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func classifyRoute(d DiscoveredOpportunity) (string, string) {
	meta := d.Metadata
	if meta[MetaApplyEmail] != "" {
		return RouteDirectContact, "mailto:" + meta[MetaApplyEmail]
	}
	if apply := meta[MetaApplyURL]; apply != "" {
		host := hostOf(apply)
		switch {
		case hostIn(host, atsHosts):
			return RouteEmployerATS, apply
		case hostIn(host, boardHosts):
			return RouteJobBoard, apply
		case host != "":
			return RouteEmployerSite, apply
		}
	}
	if d.URL != "" {
		if hostIn(hostOf(d.URL), atsHosts) {
			return RouteEmployerATS, d.URL
		}
		return RouteJobBoard, d.URL
	}
	return RouteUnknown, ""
}

func classifyCost(d DiscoveredOpportunity, route string) (string, string) {
	text := d.Description
	for _, pattern := range paidPatterns {
		if m := pattern.FindString(text); m != "" {
			return CostPaid, evidence(m)
		}
	}
	for _, pattern := range registrationPatterns {
		if m := pattern.FindString(text); m != "" {
			return CostRegistration, evidence(m)
		}
	}
	switch route {
	case RouteDirectContact, RouteEmployerSite, RouteEmployerATS:
		// Applying to the employer directly costs nothing.
		return CostFree, ""
	}
	return CostUnknown, ""
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

func hostIn(host string, hosts []string) bool {
	for _, h := range hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

func evidence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxEvidenceCharacters {
		s = s[:maxEvidenceCharacters] + "…"
	}
	return s
}

// PursuitScore orders opportunities by how realistically they can be pursued from Kenya and turned
// into paid work: fit first, then whether the applicant is eligible, how direct the route is, what
// it costs, whether it is short-term contract work and how fresh it is. Every term is a small
// fixed number so the ordering can be explained by reading this function.
func PursuitScore(fit string, a Access, ageDays int) int {
	score := map[string]int{"strong_match": 40, "possible_match": 25, "unassessed": 12, "unrelated": 0}[fit]
	score += map[string]int{KenyaOpen: 25, KenyaLikely: 18, KenyaUnknown: 10, KenyaRestricted: 0}[a.KenyaAccess]
	score += map[string]int{RouteDirectContact: 20, RouteEmployerSite: 15, RouteEmployerATS: 15, RouteJobBoard: 9, RouteUnknown: 5}[a.Route]
	score += map[string]int{CostFree: 5, CostUnknown: 3, CostRegistration: 1, CostPaid: -15}[a.Cost]
	if a.Contract {
		score += 5
	}
	switch {
	case ageDays <= 7:
		score += 5
	case ageDays <= 30:
		score += 2
	case ageDays > 60:
		score -= 5
	}
	return score
}
