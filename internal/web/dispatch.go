package web

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/anthonyalando8/prospector/internal/automation"
	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/dispatch"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

// newDispatcher builds the one canonical outbound path. Readiness is computed here, once, from
// the live schema version and the actual transport configuration — it is not a compile-time
// constant and not a bare env flag.
func newDispatcher(db *store.DB) *dispatch.Service {
	mode := os.Getenv("EMAIL_PIPELINE_MODE")
	if mode == "" {
		mode = send.ModeReview
	}
	transport, transportReason := send.NewSMTPFromEnv()

	// A handler constructed without a database (tests, and the constructor-only checks that
	// assert no network or provider is touched) still gets a dispatcher, but one that reports
	// exactly why it cannot send rather than one that looks ready.
	schemaVersion, schemaHealthy := 0, false
	if db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		if schemaVersion, err = db.SchemaVersion(ctx); err == nil {
			schemaHealthy = db.CheckSchema(ctx) == nil
		}
	}

	limits := send.DefaultLimits()
	if v := os.Getenv("DAILY_SEND_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limits.DailyCap = n
		}
	}
	if v := os.Getenv("MIN_SEND_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			limits.MinIntervalSeconds = n
		}
	}

	readiness := send.EvaluateReadiness(send.Inputs{
		Mode: mode, SchemaVersion: schemaVersion, SchemaHealthy: schemaHealthy,
		TransportReady: transportReason == "", TransportReason: transportReason,
		// A usable sender domain, not merely a non-empty string: it stamps every Message-ID.
		SenderConfigured: transport.SenderDomain() != "", Limits: limits,
	})
	// Whether selective automation is even configured is computed here too. A parse failure was
	// already fatal at startup, so an error at this point means a handler was built without
	// going through main; treat it as automation off rather than guessing.
	automationCfg, err := automation.LoadConfig(mode, os.Getenv("AUTO_SEND_INTENTS"), os.Getenv("AUTO_SEND_DAILY_CAP"))
	if err != nil {
		automationCfg = automation.DefaultConfig()
		automationCfg.Mode = mode
	}
	state := send.EvaluateAutomation(send.AutomationInputs{
		Mode: mode, Allowlist: automationCfg.Allowlist, SchemaVersion: schemaVersion,
		Readiness: readiness, DailyCap: automationCfg.DailyCap,
	})
	return dispatch.New(db, transport, mode, limits, readiness).WithAutomation(state)
}

// CompositionApprove records the explicit human authorization to send. It is deliberately a
// separate act from review: review says the email is defensible, approval says send this one.
func (h *Handler) CompositionApprove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid draft", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid approval", http.StatusBadRequest)
		return
	}
	revision, err := strconv.Atoi(r.FormValue("revision"))
	if err != nil {
		http.Error(w, "Missing content revision", http.StatusBadRequest)
		return
	}
	// The hash comes from the page the operator actually read. If the draft changed underneath
	// them, ApproveForSend refuses rather than approving bytes they never saw.
	hash := r.FormValue("content_hash")
	approver := r.FormValue("approved_by")
	if approver == "" {
		approver = "operator"
	}
	if _, err := h.db.ApproveForSend(r.Context(), id, revision, hash, approver); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/compositions/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// CompositionApproveManual records a person's approval of an email the AI review did not pass.
// It needs an explicit acknowledgement that the AI feedback was read; it never sends anything.
func (h *Handler) CompositionApproveManual(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid draft", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid approval", http.StatusBadRequest)
		return
	}
	if r.FormValue("acknowledge") != "yes" {
		http.Error(w, "Confirm that you have read the AI feedback before approving manually", http.StatusBadRequest)
		return
	}
	revision, err := strconv.Atoi(r.FormValue("revision"))
	if err != nil {
		http.Error(w, "Missing content revision", http.StatusBadRequest)
		return
	}
	approver := r.FormValue("approved_by")
	if approver == "" {
		approver = "operator"
	}
	note := r.FormValue("note")
	if len(note) > 500 {
		note = note[:500]
	}
	if _, err := h.db.ApproveManually(r.Context(), id, revision, r.FormValue("content_hash"), approver, note); err != nil {
		http.Error(w, "Cannot approve: "+err.Error(), http.StatusConflict)
		return
	}
	h.invalidateDashboard()
	h.logActivity(r.Context(), "composition", "Draft "+strconv.FormatInt(id, 10)+" approved manually by "+approver+" despite AI feedback", true)
	http.Redirect(w, r, "/compositions/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (h *Handler) CompositionRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid draft", http.StatusBadRequest)
		return
	}
	_ = r.ParseForm()
	reason := r.FormValue("reason")
	if reason == "" {
		reason = "revoked by operator"
	}
	if err := h.db.RevokeApproval(r.Context(), id, reason); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/compositions/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// CompositionSend is the only send route in the application. It delegates every decision to
// dispatch.Service, which is the only code permitted to reach a mail transport.
func (h *Handler) CompositionSend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid draft", http.StatusBadRequest)
		return
	}
	if h.dispatcher == nil {
		http.Error(w, "Sending is not configured", http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	initiator := r.FormValue("initiator")
	if initiator == "" {
		initiator = "operator"
	}
	// A refusal is a normal outcome, not a server error. Either way the draft page is the record
	// of what happened: it shows the blockers, or the attempt and the thread that opened.
	if _, err := h.dispatcher.Send(r.Context(), dispatch.Request{
		DraftID: id, Initiator: initiator, Kind: send.KindFirstContact,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/compositions/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (h *Handler) Threads(w http.ResponseWriter, r *http.Request) { h.threadList(w, r) }

func (h *Handler) ThreadView(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid thread", http.StatusBadRequest)
		return
	}
	thread, err := h.db.GetThread(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load thread", http.StatusNotFound)
		return
	}
	events, err := h.db.ThreadEvents(r.Context(), id)
	if err != nil {
		http.Error(w, "Cannot load thread history", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "thread.html", map[string]any{"Thread": thread, "Events": events, "BackURL": safeReturn(r.URL.Query().Get("return"), "/threads"), "BackLabel": returnLabel(safeReturn(r.URL.Query().Get("return"), "/threads"))})
}

// ThreadEventRecord captures an out-of-band delivery fact (a bounce, a reply, an unsubscribe
// request) that no provider webhook told us about. A permanent bounce recorded here suppresses
// the address, which is why it is an explicit, attributed act.
func (h *Handler) ThreadEventRecord(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.Error(w, "Invalid thread", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid event", http.StatusBadRequest)
		return
	}
	// RecordThreadEvent is the authority on which kinds exist; an unknown one is refused there
	// rather than validated twice in two places that could drift apart.
	if err := h.db.RecordThreadEvent(r.Context(), store.ThreadEvent{
		ThreadID: id, Kind: r.FormValue("kind"), Detail: r.FormValue("detail"), Source: "operator",
	}); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/threads/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// sendView is everything the composition page needs to show the send state honestly: the
// approval, the preflight decision, the attempt history and the thread.
type sendView struct {
	Readiness send.Readiness
	Mode      string
	Approval  *store.SendApproval
	Decision  send.Decision
	Attempts  []*store.SendAttempt
	Thread    *store.OutboundThread
	Hash      string
}

// buildSendView never guesses. Where it cannot determine something it leaves it zero, and the
// template treats a missing decision as "not sendable".
func (h *Handler) buildSendView(ctx context.Context, draftID int64, email composition.Email) sendView {
	v := sendView{Hash: composition.Hash(email)}
	if h.dispatcher == nil {
		return v
	}
	v.Readiness, v.Mode = h.dispatcher.Readiness(), h.dispatcher.Mode()
	if approval, err := h.db.LiveApproval(ctx, draftID); err == nil {
		v.Approval = approval
	}
	if decision, err := h.dispatcher.Preview(ctx, dispatch.Request{
		DraftID: draftID, Initiator: "preview", Kind: send.KindFirstContact,
	}); err == nil {
		v.Decision = decision
	}
	if attempts, err := h.db.AttemptsForDraft(ctx, draftID); err == nil {
		v.Attempts = attempts
		for _, a := range attempts {
			if a.ThreadID != nil {
				if thread, err := h.db.GetThread(ctx, *a.ThreadID); err == nil {
					v.Thread = thread
				}
			}
		}
	}
	return v
}

// AutomationState reports whether selective automation is configured. It lives on the handler
// because the handler already owns the dispatcher that computed it, and startup needs the same
// answer the UI shows.
func (h *Handler) AutomationState() send.Automation {
	if h.dispatcher == nil {
		return send.Automation{Mode: os.Getenv("EMAIL_PIPELINE_MODE"),
			Reasons: []string{"no send dispatcher is configured"}}
	}
	return h.dispatcher.Automation()
}

// Dispatcher exposes the canonical send path for the automation worker, which needs the same
// service the UI uses rather than a second one configured differently.
func (h *Handler) Dispatcher() *dispatch.Service { return h.dispatcher }

// AIBudgetAvailable reports whether AI request capacity remains. Automation shares the same
// budget as manual composition; it has no allowance of its own.
func (h *Handler) AIBudgetAvailable(ctx context.Context) (bool, string) {
	if h.aiBudget == nil {
		return false, "no AI request budget is configured"
	}
	_, ready, reason := h.aiBudget.NextAvailable(ctx)
	if ready {
		return true, ""
	}
	if reason == "" {
		reason = "the AI request budget is exhausted"
	}
	return false, reason
}
