package addresscheck

import (
	"context"
	"net"
	"testing"

	"github.com/anthonyalando8/prospector/internal/store"
)

// fakeDNS answers from a table; nothing here touches the network.
type fakeDNS struct {
	mx    map[string][]*net.MX
	hosts map[string][]string
	fail  bool
}

func (f fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if f.fail {
		return nil, &net.DNSError{Err: "timeout", Name: name, IsTimeout: true}
	}
	if r, ok := f.mx[name]; ok {
		return r, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeDNS) LookupHost(_ context.Context, name string) ([]string, error) {
	if r, ok := f.hosts[name]; ok {
		return r, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

var dns = fakeDNS{
	mx: map[string][]*net.MX{
		"acmepay.com": {{Host: "mx1.acmepay.com.", Pref: 10}},
		"gmail.com":   {{Host: "gmail-smtp-in.l.google.com.", Pref: 5}},
		"partner.io":  {{Host: "mx.partner.io.", Pref: 10}},
		"nullmx.com":  {{Host: ".", Pref: 0}},
		"acmepey.com": {{Host: "mx.parked.example.", Pref: 10}},
	},
	hosts: map[string][]string{"webonly.com": {"203.0.113.7"}},
}

func subject(email string) store.AddressCheckSubject {
	return store.AddressCheckSubject{
		CandidateID: 1, CompanyName: "AcmePay", CompanyDomain: "https://www.acmepay.com/", Email: email,
		PersonName: "Jane Wanjiru", Provenance: "third_party_published",
	}
}

func TestCheckVerdicts(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, email, company, verdict, mail string
		provenance                          string
		failing                             string // a check code expected to fail or warn
	}{
		{name: "company domain with mail servers and the person's name", email: "jane.wanjiru@acmepay.com",
			verdict: store.AddressLooksDeliverable, mail: MailMX},
		{name: "personal gmail is doubtful, not refused", email: "jane.wanjiru@gmail.com",
			verdict: store.AddressDoubtful, mail: MailMX, failing: "company_domain"},
		{name: "one letter off the company's domain is a misspelling", email: "jane.wanjiru@acmepey.com",
			verdict: store.AddressWillNotReceive, mail: MailMX, failing: "company_domain"},
		{name: "a domain with no mail servers and no address cannot receive", email: "jane@nowhere-at-all.com",
			company: "nowhere-at-all.com", verdict: store.AddressWillNotReceive, mail: MailNone, failing: "mail_servers"},
		{name: "a null MX refuses all mail", email: "jane@nullmx.com", company: "nullmx.com",
			verdict: store.AddressWillNotReceive, mail: MailNone, failing: "mail_servers"},
		{name: "address-only fallback is doubtful", email: "jane@webonly.com", company: "webonly.com",
			verdict: store.AddressDoubtful, mail: MailAddressOnly, failing: "mail_servers"},
		{name: "a no-reply address is not a mailbox", email: "no-reply@acmepay.com",
			verdict: store.AddressWillNotReceive, mail: MailMX, failing: "mailbox_purpose"},
		{name: "another person's name is doubtful", email: "peter.otieno@acmepay.com",
			verdict: store.AddressDoubtful, mail: MailMX, failing: "name_match"},
		{name: "an inferred address is always doubtful", email: "jane.wanjiru@acmepay.com", provenance: "inferred_pattern",
			verdict: store.AddressDoubtful, mail: MailMX, failing: "provenance"},
		{name: "malformed", email: "jane at acmepay", verdict: store.AddressWillNotReceive, mail: MailNotChecked, failing: "syntax"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := subject(c.email)
			if c.company != "" {
				s.CompanyDomain = c.company
			}
			if c.provenance != "" {
				s.Provenance = c.provenance
			}
			got := Check(ctx, dns, s)
			if got.Verdict != c.verdict || got.MailServers != c.mail {
				t.Fatalf("verdict %s/%s, want %s/%s: %+v", got.Verdict, got.MailServers, c.verdict, c.mail, got.Checks)
			}
			if c.failing != "" {
				item, ok := got.Item(c.failing)
				if !ok || item.Result == Pass {
					t.Fatalf("%s should not pass: %+v", c.failing, got.Checks)
				}
			}
		})
	}
}

func TestPublishedSiblingDomainCounts(t *testing.T) {
	s := subject("jane.wanjiru@partner.io")
	s.KnownAddresses = []string{"careers@partner.io"}
	got := Check(context.Background(), dns, s)
	if item, _ := got.Item("company_domain"); item.Result != Pass {
		t.Fatalf("a domain the company already publishes addresses on should pass: %+v", got.Checks)
	}
}

func TestLookupFailureIsNotAVerdictAgainstTheAddress(t *testing.T) {
	got := Check(context.Background(), fakeDNS{fail: true}, subject("jane.wanjiru@acmepay.com"))
	if got.MailServers != MailLookupFailed || got.Verdict != store.AddressDoubtful {
		t.Fatalf("a DNS timeout must be doubtful, not will-not-receive: %+v", got)
	}
}

func TestAIOpinionCannotOverruleAHardFault(t *testing.T) {
	hard := Result{Verdict: store.AddressWillNotReceive}
	o := Clean(Opinion{Verdict: "Plausible", Reasons: []string{"Looks fine."}}, hard)
	if o.Verdict != AIDoubtful {
		t.Fatalf("plausible over a hard fault must be lowered: %+v", o)
	}
	if o := Clean(Opinion{Verdict: "certainly valid"}, Result{}); o.Verdict != AIFailed {
		t.Fatalf("an answer outside the vocabulary is a failure: %+v", o)
	}
	long := make([]string, 9)
	for i := range long {
		long[i] = "reason"
	}
	if o := Clean(Opinion{Verdict: "doubtful", Reasons: long}, Result{}); len(o.Reasons) != 5 {
		t.Fatalf("reasons are bounded: %d", len(o.Reasons))
	}
}

func TestBrokenResolverIsNotTakenAtItsWord(t *testing.T) {
	offline := fakeDNS{} // answers "not found" for every name, including the canary
	got := Check(context.Background(), offline, subject("jane.wanjiru@acmepay.com"))
	if got.MailServers != MailLookupFailed || got.Verdict == store.AddressWillNotReceive {
		t.Fatalf("a resolver that finds nothing must not condemn the address: %+v", got)
	}
}
