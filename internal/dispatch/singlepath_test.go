package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Phase 5 invariant that cannot be expressed as a unit test of behavior: there is exactly
// one way out to a mail server. This walks the source rather than trusting the call graph,
// because the failure it guards against is someone adding a second transport later, not a bug
// in the current code.
func TestOnlyTheSendPackageMayReachAMailTransport(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{
		filepath.Join("internal", "send"): true,
	}
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", ".local", "scratchpad":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if allowed[filepath.Dir(rel)] {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The import is the thing that matters: a file that cannot import the standard SMTP
		// package cannot open an SMTP conversation, whatever else it does. The needle is built
		// at runtime so this file is not its own first offender.
		needle := `"net/` + `smtp"`
		if strings.Contains(string(body), needle) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("these files can reach a mail transport directly, outside internal/send: %v", offenders)
	}
}
