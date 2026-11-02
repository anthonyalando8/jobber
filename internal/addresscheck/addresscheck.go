// Package addresscheck judges whether a discovered contact address can plausibly receive mail,
// for the addresses contact discovery could not promote on its own and left for a person.
//
// It has two halves. The deterministic half is free and runs on every such address: it reads the
// address, compares it with the company's domain and the addresses the company is known to use,
// and asks DNS whether the domain has mail servers. The AI half is optional and gives an opinion
// on plausibility from the same facts.
//
// Neither half establishes that the mailbox exists, and neither sends anything to it. Probing a
// mail server with RCPT TO would be closer to proof, but it is unreliable (many servers accept
// every address) and it harms the sending domain's reputation, so it is deliberately not done.
// Neither half can accept an address either: that stays a person's decision.
package addresscheck

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/relevance"
	"github.com/anthonyalando8/prospector/internal/store"
)

// Version names the rules. A change to them re-checks every waiting address once.
const Version = "address-v1"

// Results of one finding.
const (
	Pass = "pass"
	Warn = "warn"
	Fail = "fail"
)

// Mail server states, as stored.
const (
	MailMX           = "mx"
	MailAddressOnly  = "address_only"
	MailNone         = "none"
	MailLookupFailed = "lookup_failed"
	MailNotChecked   = "not_checked"
)

// Resolver is the slice of DNS the check needs; net.DefaultResolver satisfies it.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Result is the deterministic verdict.
type Result struct {
	Verdict     string
	MailServers string
	Checks      []store.AddressCheckItem
}

// Item finds one check by code, for tests and the view.
func (r Result) Item(code string) (store.AddressCheckItem, bool) {
	for _, c := range r.Checks {
		if c.Code == code {
			return c, true
		}
	}
	return store.AddressCheckItem{}, false
}

// freeMail are consumer mailbox providers. An address there is a person's own mailbox, not the
// company's, which is sometimes right (a founder's gmail) and is always worth a second look.
var freeMail = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "yahoo.com": true, "yahoo.co.uk": true, "ymail.com": true,
	"outlook.com": true, "hotmail.com": true, "live.com": true, "msn.com": true, "icloud.com": true,
	"me.com": true, "aol.com": true, "proton.me": true, "protonmail.com": true, "gmx.com": true,
	"zoho.com": true, "mail.com": true, "yandex.com": true,
}

// Check runs every deterministic finding. A nil resolver skips the DNS lookup.
func Check(ctx context.Context, resolver Resolver, s store.AddressCheckSubject) Result {
	res := Result{MailServers: MailNotChecked}
	add := func(code, result, text string) {
		res.Checks = append(res.Checks, store.AddressCheckItem{Code: code, Result: result, Text: text})
	}

	address, err := store.NormalizeContactValue("email", s.Email)
	if err != nil {
		add("syntax", Fail, "This is not a well-formed email address.")
		res.Verdict = verdictOf(res.Checks)
		return res
	}
	address = strings.ToLower(address)
	at := strings.LastIndex(address, "@")
	local, domain := address[:at], address[at+1:]
	add("syntax", Pass, "The address is well formed.")

	if endpoint := relevance.ClassifyEndpoint(address); !endpoint.Contactable {
		add("mailbox_purpose", Fail, "The address is for "+strings.ReplaceAll(endpoint.Purpose, "_", " ")+
			", not for a person to read. "+endpoint.Evidence)
	}

	company := normalizeDomain(s.CompanyDomain)
	switch {
	case company == "":
		add("company_domain", Warn, "The company's domain is not known, so the address cannot be matched to it.")
	case domain == company || strings.HasSuffix(domain, "."+company):
		add("company_domain", Pass, "The address is on the company's own domain ("+company+").")
	case freeMail[domain]:
		add("company_domain", Warn, "This is a personal "+domain+" mailbox, not one on the company's domain ("+company+").")
	case lookalike(domain, company):
		add("company_domain", Fail, "The domain "+domain+" is one or two letters away from the company's ("+company+"); it is most likely a misspelling.")
	case knownDomain(domain, s.KnownAddresses):
		add("company_domain", Pass, "The address is on "+domain+", which the company already uses for published addresses.")
	default:
		add("company_domain", Warn, "The address is on "+domain+", not the company's domain ("+company+").")
	}

	if resolver != nil {
		res.MailServers = lookupMail(ctx, resolver, domain)
		switch res.MailServers {
		case MailMX:
			add("mail_servers", Pass, domain+" publishes mail servers, so it accepts email.")
		case MailAddressOnly:
			add("mail_servers", Warn, domain+" has no mail servers listed; delivery would rely on a fallback many domains do not serve.")
		case MailNone:
			add("mail_servers", Fail, domain+" has no mail servers, or does not exist, so nothing sent there can arrive.")
		case MailLookupFailed:
			add("mail_servers", Warn, "The mail server lookup for "+domain+" failed; it was not possible to tell.")
		}
	}

	if s.PersonName != "" {
		if nameInLocalPart(local, s.PersonName) {
			add("name_match", Pass, "The address contains the name of the person it is attributed to.")
		} else if !isRoleLocal(local) {
			add("name_match", Warn, "The address does not contain "+s.PersonName+"'s name, so it may belong to someone else.")
		}
	}

	if s.Provenance == "inferred_pattern" {
		if len(s.KnownAddresses) > 0 {
			add("provenance", Warn, "Nobody published this address. It was built from the format of "+
				s.KnownAddresses[0]+"; a wrong guess bounces.")
		} else {
			add("provenance", Warn, "Nobody published this address, and no published address supports its format.")
		}
	}

	res.Verdict = verdictOf(res.Checks)
	return res
}

func verdictOf(checks []store.AddressCheckItem) string {
	verdict := store.AddressLooksDeliverable
	for _, c := range checks {
		switch c.Result {
		case Fail:
			return store.AddressWillNotReceive
		case Warn:
			verdict = store.AddressDoubtful
		}
	}
	return verdict
}

// lookupMail reads the domain's mail servers. A null MX (a single "." record) is a domain saying
// it accepts no mail at all, which is the same answer as having none.
func lookupMail(ctx context.Context, resolver Resolver, domain string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	records, err := resolver.LookupMX(ctx, domain)
	if err == nil {
		for _, mx := range records {
			if host := strings.TrimSuffix(mx.Host, "."); host != "" {
				return MailMX
			}
		}
		return MailNone
	}
	if !notFound(err) {
		return MailLookupFailed
	}
	if hosts, err := resolver.LookupHost(ctx, domain); err == nil && len(hosts) > 0 {
		return MailAddressOnly
	} else if err != nil && !notFound(err) {
		return MailLookupFailed
	}
	// "No such domain" is only believable from a resolver that can find a domain that certainly
	// exists. A resolver cut off from the internet answers "not found" for everything, and
	// trusting it would mark every good address as unable to receive mail.
	if records, err := resolver.LookupMX(ctx, canaryDomain); err != nil || len(records) == 0 {
		return MailLookupFailed
	}
	return MailNone
}

// canaryDomain certainly publishes mail servers; it is only ever looked up, never written to.
const canaryDomain = "gmail.com"

func notFound(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns) && dns.IsNotFound
}

func normalizeDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	return strings.TrimSuffix(strings.TrimPrefix(d, "www."), ".")
}

func knownDomain(domain string, known []string) bool {
	for _, k := range known {
		if i := strings.LastIndex(k, "@"); i >= 0 && strings.EqualFold(k[i+1:], domain) {
			return true
		}
	}
	return false
}

// lookalike reports a domain within two edits of the company's that is not the company's: the
// shape of a typo or of a scraped address with a mangled character.
func lookalike(domain, company string) bool {
	if domain == company {
		return false
	}
	return distance(domain, company) <= 2 && len(company) >= 6
}

func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// nameInLocalPart accepts the usual shapes: first, last, first.last, flast, firstl.
func nameInLocalPart(local, name string) bool {
	parts := strings.Fields(strings.ToLower(name))
	if len(parts) == 0 {
		return false
	}
	clean := strings.NewReplacer(".", "", "_", "", "-", "").Replace(local)
	first, last := letters(parts[0]), letters(parts[len(parts)-1])
	if len(first) >= 3 && strings.Contains(clean, first) {
		return true
	}
	if len(last) >= 3 && strings.Contains(clean, last) {
		return true
	}
	return false
}

func letters(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isRoleLocal(local string) bool {
	switch local {
	case "jobs", "careers", "hr", "hiring", "recruiting", "recruitment", "talent", "people", "info",
		"hello", "contact", "team", "office", "admin", "sales", "support", "engineering", "tech":
		return true
	}
	return false
}
