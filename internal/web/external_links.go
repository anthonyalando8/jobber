package web

import (
	"regexp"
	"strings"
)

// A link that leaves the application opens in a new tab. A source posting, an employer's careers
// page or a signal's citation is reference material read alongside the workspace page that cited
// it; navigating the same tab away loses the user's place in the review they were part way
// through, and the browser Back button does not restore scroll position or an open <details>.
//
// Templates say so in their own markup where the link is written by hand. This rewrite is the
// backstop for the ones that do not: every page is funnelled through renderTemplate, so an
// external link added later gets the same treatment without anyone having to remember the
// attributes. It is also why the fix does not depend on JavaScript.
var (
	anchorOpenTag = regexp.MustCompile(`(?is)<a\s[^>]*>`)
	hrefAttribute = regexp.MustCompile(`(?is)\bhref\s*=\s*"([^"]*)"`)
	relAttribute  = regexp.MustCompile(`(?is)\brel\s*=\s*"([^"]*)"`)
	targetAttr    = regexp.MustCompile(`(?is)\btarget\s*=\s*"([^"]*)"`)
)

// openExternalLinksInNewTab gives every anchor pointing at an absolute http(s) URL a
// target="_blank", plus the rel tokens that make that target safe: without noopener the opened
// page can reach back through window.opener and redirect the tab it came from.
func openExternalLinksInNewTab(html string) string {
	return anchorOpenTag.ReplaceAllStringFunc(html, func(tag string) string {
		href := hrefAttribute.FindStringSubmatch(tag)
		if href == nil || !isExternalLink(href[1]) {
			return tag
		}
		if target := targetAttr.FindStringSubmatch(tag); target != nil {
			// An author who named a target meant it. Only _blank needs the rel tokens.
			if !strings.EqualFold(strings.TrimSpace(target[1]), "_blank") {
				return tag
			}
		} else {
			tag = withAttribute(tag, `target="_blank"`)
		}
		return withRelTokens(tag, "noopener", "noreferrer")
	})
}

// isExternalLink reports whether an href leaves the application. Every link the application
// writes to its own pages is root-relative, so an absolute http(s) URL is by definition somebody
// else's site. Other schemes are left alone: a mailto: handed to a new tab leaves a blank one
// behind, and a fragment or relative link never leaves the page.
func isExternalLink(href string) bool {
	href = strings.TrimSpace(href)
	return strings.HasPrefix(strings.ToLower(href), "http://") ||
		strings.HasPrefix(strings.ToLower(href), "https://")
}

// withAttribute inserts an attribute into an anchor's opening tag, immediately after the tag name
// so the author's own attributes keep their order.
func withAttribute(tag, attribute string) string {
	return "<a " + attribute + tag[len("<a"):]
}

// withRelTokens adds the given rel tokens, keeping any the template already set — several links
// carry a deliberate nofollow that must survive.
func withRelTokens(tag string, tokens ...string) string {
	existing := relAttribute.FindStringSubmatch(tag)
	if existing == nil {
		return withAttribute(tag, `rel="`+strings.Join(tokens, " ")+`"`)
	}
	present := strings.Fields(existing[1])
	for _, token := range tokens {
		if !hasToken(present, token) {
			present = append(present, token)
		}
	}
	return relAttribute.ReplaceAllLiteralString(tag, `rel="`+strings.Join(present, " ")+`"`)
}

func hasToken(tokens []string, want string) bool {
	for _, token := range tokens {
		if strings.EqualFold(token, want) {
			return true
		}
	}
	return false
}
