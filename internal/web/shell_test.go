package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A page that brings no styling of its own must be wrapped by the application shell, or it is
// served as bare HTML: no stylesheet, no sidebar, no layout.
//
// This is not hypothetical. The contact discovery page shipped missing from applicationShell's
// route map and rendered as unstyled markup on a white page — everything correct and nothing
// readable. Nothing failed, because nothing was checking: the shell returns the page untouched
// for a name it does not recognize, which is right for a fragment and silently wrong for a page.
//
// The older self-styled pages carry a complete inline <style> block and stand on their own, so
// they are exempt by the same rule that catches the bug rather than by being named here.
func TestEveryUnstyledPageTemplateIsWrappedByTheShell(t *testing.T) {
	shell, err := os.ReadFile("shell.go")
	if err != nil {
		t.Fatal(err)
	}
	templates, err := filepath.Glob("templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 {
		t.Fatal("no templates found; this test is not checking anything")
	}

	for _, path := range templates {
		name := filepath.Base(path)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		markup := string(body)
		if !strings.Contains(markup, "<body") {
			continue // a fragment, such as the sidebar itself
		}
		if strings.Contains(markup, "<style") {
			continue // carries its own complete styling
		}
		if !strings.Contains(string(shell), `"`+name+`"`) {
			t.Errorf("%s has no styling of its own and is not registered in applicationShell, "+
				"so it renders as bare HTML", name)
		}
	}
}

// The contact discovery page in particular must reach the shell, since its whole job is to make
// provenance legible.
func TestContactDiscoveryPageIsRegisteredWithTheShell(t *testing.T) {
	shell, err := os.ReadFile("shell.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shell), `"contact_discovery.html"`) {
		t.Fatal("the contact discovery page is not wrapped by the application shell")
	}
}

// applicationShell must actually inject the stylesheets and the navigation, not merely accept the
// name. Run from the repository root, because the shell loads the sidebar by repo-relative path.
func TestApplicationShellInjectsStylesheetsAndNavigation(t *testing.T) {
	restore := chdirRepoRoot(t)
	defer restore()

	page := "<html><head><title>t</title></head><body><h1>Contact discovery</h1></body></html>"
	wrapped, err := applicationShell("contact_discovery.html", page)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"/static/dashboard.css", "/static/shell.css", "/static/lists.css",
		`class="sidebar"`, `id="page-main"`, "</main></body>",
	} {
		if !strings.Contains(wrapped, expected) {
			t.Errorf("the wrapped page is missing %q", expected)
		}
	}

	// An unregistered name is returned untouched. That is correct for a fragment, and is exactly
	// why an unregistered page fails silently.
	if untouched, err := applicationShell("not_a_page.html", page); err != nil || untouched != page {
		t.Fatal("an unregistered template was not returned unchanged")
	}
}

func chdirRepoRoot(t *testing.T) func() {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("internal", "web", "templates", "sidebar.html")); err != nil {
		os.Chdir(original)
		t.Fatal("repository root not found from the test working directory")
	}
	return func() { os.Chdir(original) }
}
