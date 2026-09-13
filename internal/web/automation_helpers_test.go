package web

import "github.com/anthonyalando8/prospector/internal/send"

// automationUnderDefaultConfig is what the repository's shipped configuration produces: review
// mode, an empty allowlist. Phase 7 made automation computed rather than unconditionally false,
// so the tests that used to assert "automation is impossible" now assert the stronger and more
// useful thing — that the default configuration enables nothing, even with a healthy schema and
// a ready send path.
func automationUnderDefaultConfig() send.Automation {
	return send.EvaluateAutomation(send.AutomationInputs{
		Mode:          send.ModeReview,
		Allowlist:     nil,
		SchemaVersion: send.RequiredSchemaVersion,
		Readiness:     send.Readiness{Ready: true},
		DailyCap:      1,
	})
}
