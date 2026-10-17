package web

import (
	"os"
	"strings"
	"testing"
)

// Every link out of the application opened in the same tab, so clicking a job posting's source
// URL replaced the review page the user was working through. Back does not restore scroll
// position or an expanded snapshot, so the review had to be found again from the list.
func TestExternalLinksOpenInANewTab(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want string
	}{{
		name: "a bare external link gets a target and the rel tokens that make it safe",
		in:   `<a href="https://boards.example.com/jobs/1">source</a>`,
		want: `<a rel="noopener noreferrer" target="_blank" href="https://boards.example.com/jobs/1">source</a>`,
	}, {
		name: "an existing rel keeps its own tokens",
		in:   `<a href="https://example.com/apply" rel="nofollow">apply</a>`,
		want: `<a target="_blank" href="https://example.com/apply" rel="nofollow noopener noreferrer">apply</a>`,
	}, {
		name: "a link the template already sent to a new tab only has its rel completed",
		in:   `<a href="https://example.com" class="signal-url" target="_blank">source</a>`,
		want: `<a rel="noopener noreferrer" href="https://example.com" class="signal-url" target="_blank">source</a>`,
	}, {
		name: "insecure http is still external",
		in:   `<a href="http://example.com">old site</a>`,
		want: `<a rel="noopener noreferrer" target="_blank" href="http://example.com">old site</a>`,
	}} {
		t.Run(c.name, func(t *testing.T) {
			if got := openExternalLinksInNewTab(c.in); got != c.want {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

// Navigation inside the application must stay in the tab the user is in, and a mailto: sent to a
// new tab leaves an empty one behind once the mail client takes over.
func TestInternalAndNonWebLinksAreLeftAlone(t *testing.T) {
	for _, unchanged := range []string{
		`<a href="/opportunities?page=2">Next</a>`,
		`<a class="skip-link" href="#page-main">Skip to content</a>`,
		`<a class="mailto" href="mailto:dev@example.com?subject=Re%3A%20following%20up">open in mail client</a>`,
		`<a href="https://example.com" target="_self">deliberately this tab</a>`,
		`<a class="disabled">&larr; Prev</a>`,
		`<abbr title="https://example.com">not a link</abbr>`,
	} {
		if got := openExternalLinksInNewTab(unchanged); got != unchanged {
			t.Errorf("rewrote a link it should not have:\ngot  %s\nwant %s", got, unchanged)
		}
	}
}

// The rewrite runs over a whole rendered page, so it must reach every anchor in it and leave the
// surrounding markup alone.
func TestTheRewriteRunsOverAWholePage(t *testing.T) {
	page := `<html><body><nav><a href="/companies">Back</a></nav>` +
		`<dd><a href="https://boards.example.com/1" rel="noreferrer">posting</a></dd>` +
		`<p><a
			href="https://careers.example.com">careers</a></p></body></html>`
	got := openExternalLinksInNewTab(page)

	if strings.Count(got, `target="_blank"`) != 2 {
		t.Errorf("expected both external links to open in a new tab, got %s", got)
	}
	if !strings.Contains(got, `<a href="/companies">Back</a>`) {
		t.Errorf("the internal link was rewritten: %s", got)
	}
	if strings.Count(got, "noopener") != 2 || strings.Count(got, "noreferrer") != 2 {
		t.Errorf("rel tokens were not applied once per external link: %s", got)
	}
}

// Anchors written by hand should say what they do, rather than relying on the rewrite: the
// attributes belong where a reader of the template can see them.
func TestHandWrittenExternalLinksCarryTheirOwnAttributes(t *testing.T) {
	for _, file := range []string{
		"templates/company.html", "templates/contact_discovery.html",
		"templates/opportunity.html", "templates/review.html",
	} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		markup := string(body)
		for _, anchor := range anchorOpenTag.FindAllString(markup, -1) {
			href := hrefAttribute.FindStringSubmatch(anchor)
			if href == nil {
				continue
			}
			// A link to somewhere else entirely is a whole URL out of the data. Links within the
			// application are written as a path, so their href starts with "/" even where the
			// rest of it is interpolated.
			target := strings.TrimSpace(href[1])
			if !strings.HasPrefix(target, "{{") {
				continue
			}
			if strings.Contains(target, "backURL") || strings.Contains(target, "BackURL") ||
				strings.Contains(target, "prevURL") || strings.Contains(target, "nextURL") {
				continue // pagination and breadcrumbs stay in this tab
			}
			if !strings.Contains(anchor, `target="_blank"`) || !strings.Contains(anchor, "noopener") {
				t.Errorf("%s: external link is missing its new-tab attributes: %s", file, anchor)
			}
		}
	}
}
