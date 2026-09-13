package send

import "fmt"

// Rollout modes, in increasing order of what they permit. Each is a strict superset of the one
// before it, and none of them makes an individual message sendable on its own: every mode still
// runs the full preflight, and v2_auto additionally runs the automation policy per message.
const (
	ModeDisabled   = "disabled"
	ModeReview     = "v2_review"
	ModeManualSend = "v2_manual_send"
	// ModeAuto permits selective automation of allowlisted intents. It does not mean every
	// eligible draft is sent: it means the automation policy is consulted at all.
	ModeAuto = "v2_auto"
)

// RequiredSchemaVersion is the tracked schema this send path depends on.
const RequiredSchemaVersion = 23

// RequiredAutomationSchemaVersion is the tracked schema selective automation depends on. It is
// separate from RequiredSchemaVersion so that a database able to send manually is not forced to
// carry automation tables it will never use.
const RequiredAutomationSchemaVersion = 25

func ValidMode(mode string) bool {
	switch mode {
	case ModeDisabled, ModeReview, ModeManualSend, ModeAuto:
		return true
	}
	return false
}

// SendingPermitted reports whether a mode allows transmission at all, by any route.
func SendingPermitted(mode string) bool {
	return mode == ModeManualSend || mode == ModeAuto
}

// Readiness replaces the Phase 1–4 construction interlock. The old interlock was a compile-time
// constant that said "not yet"; this is a computed gate that says exactly which condition is
// unmet, and it is evaluated fresh rather than baked into the binary.
type Readiness struct {
	Ready   bool     `json:"ready"`
	Mode    string   `json:"mode"`
	Reasons []string `json:"reasons,omitempty"`
}

// Inputs is what readiness is computed from.
type Inputs struct {
	Mode             string
	SchemaVersion    int
	SchemaHealthy    bool
	TransportReady   bool
	TransportReason  string
	SenderConfigured bool
	Limits           Limits
}

// EvaluateReadiness reports whether an explicitly approved manual send could proceed at all. It never
// reports ready for automatic sending, because no mode enables it.
func EvaluateReadiness(in Inputs) Readiness {
	r := Readiness{Mode: in.Mode}
	if !ValidMode(in.Mode) {
		r.Reasons = append(r.Reasons, fmt.Sprintf("unknown rollout mode %q", in.Mode))
		return r
	}
	if !SendingPermitted(in.Mode) {
		r.Reasons = append(r.Reasons, "rollout mode "+in.Mode+" does not permit sending")
	}
	if !in.SchemaHealthy {
		r.Reasons = append(r.Reasons, "database schema check has not passed")
	}
	if in.SchemaVersion < RequiredSchemaVersion {
		r.Reasons = append(r.Reasons, fmt.Sprintf("tracked schema %d is below the required %d", in.SchemaVersion, RequiredSchemaVersion))
	}
	if !in.TransportReady {
		reason := in.TransportReason
		if reason == "" {
			reason = "sending transport is not configured"
		}
		r.Reasons = append(r.Reasons, reason)
	}
	if !in.SenderConfigured {
		r.Reasons = append(r.Reasons, "sender identity (from address) is not configured")
	}
	if in.Limits.DailyCap <= 0 {
		r.Reasons = append(r.Reasons, "no daily send cap is configured")
	}
	r.Ready = len(r.Reasons) == 0
	return r
}

// Automation reports whether the system is configured to permit selective automation at all.
// It is deliberately not a per-message answer: it means "the automation policy may be consulted",
// never "an eligible draft will be sent". The per-message decision belongs to the automation
// policy, and no configuration here can substitute for it.
type Automation struct {
	Enabled   bool     `json:"enabled"`
	Mode      string   `json:"mode"`
	Allowlist []string `json:"allowlist"`
	Reasons   []string `json:"reasons,omitempty"`
}

// AutomationInputs are the configured facts automation depends on.
type AutomationInputs struct {
	Mode          string
	Allowlist     []string
	SchemaVersion int
	Readiness     Readiness
	DailyCap      int
}

// EvaluateAutomation computes whether selective automation is configured. Phases 5 and 6
// answered this with an unconditional false because the gates did not exist yet. They exist now,
// so the answer is computed — but every condition must hold, and the default configuration
// satisfies none of them.
func EvaluateAutomation(in AutomationInputs) Automation {
	a := Automation{Mode: in.Mode, Allowlist: in.Allowlist}
	if in.Mode != ModeAuto {
		a.Reasons = append(a.Reasons, "rollout mode "+in.Mode+" is not "+ModeAuto)
	}
	if len(in.Allowlist) == 0 {
		a.Reasons = append(a.Reasons, "no intent is allowlisted for automatic sending")
	}
	if in.SchemaVersion < RequiredAutomationSchemaVersion {
		a.Reasons = append(a.Reasons, fmt.Sprintf("tracked schema %d is below the %d that automation requires",
			in.SchemaVersion, RequiredAutomationSchemaVersion))
	}
	if !in.Readiness.Ready {
		a.Reasons = append(a.Reasons, "the send path is not ready")
	}
	if in.DailyCap <= 0 {
		a.Reasons = append(a.Reasons, "no automation-specific daily cap is configured")
	}
	a.Enabled = len(a.Reasons) == 0
	return a
}
