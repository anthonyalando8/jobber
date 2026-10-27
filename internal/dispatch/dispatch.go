// Package dispatch is the one canonical outbound path. Every send, from any caller, runs
// through Service.Send: it loads a single consistent view of state, applies the send preflight,
// reserves the attempt before contacting the provider, transmits exactly the approved bytes, and
// records what happened. No other code in the application may call a mail transport.
package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/plan"
	"github.com/anthonyalando8/prospector/internal/send"
	"github.com/anthonyalando8/prospector/internal/store"
)

type Service struct {
	db         *store.DB
	transport  send.Transport
	mode       string
	limits     send.Limits
	readiness  send.Readiness
	automation send.Automation
}

func New(db *store.DB, transport send.Transport, mode string, limits send.Limits, readiness send.Readiness) *Service {
	return &Service{db: db, transport: transport, mode: mode, limits: limits, readiness: readiness}
}

// WithAutomation records whether selective automation is configured. It is a separate call so
// that constructing a dispatcher never implies automation is on: a Service built without this
// reports automation disabled.
func (s *Service) WithAutomation(a send.Automation) *Service {
	s.automation = a
	return s
}

func (s *Service) Automation() send.Automation { return s.automation }
func (s *Service) Readiness() send.Readiness   { return s.readiness }
func (s *Service) Limits() send.Limits         { return s.limits }
func (s *Service) Mode() string                { return s.mode }

// Request names what to send. It carries no content, recipient or intent: all three come from
// the approved composition, so a caller cannot smuggle in a different message.
type Request struct {
	DraftID      int64
	Initiator    string
	Kind         string
	FollowupStep int
	ThreadID     int64
}

// Outcome is the result of one dispatch.
type Outcome struct {
	Decision send.Decision
	Attempt  *store.SendAttempt
	Thread   *store.OutboundThread
	// Resolved is true when a duplicate request attached to an existing attempt instead of
	// transmitting again.
	Resolved bool
}

// view is one consistent read of everything the preflight is allowed to see.
type view struct {
	composed *store.ComposedDraft
	approval *store.SendApproval
	contact  *store.Contact
	company  *store.Company
	planRow  *store.ContactPlan
	email    composition.Email
	hash     string
	pre      send.Preflight
}

// load gathers state once. Doing this in one place keeps the decision a pure function of a
// single snapshot rather than of whatever each check happened to read.
func (s *Service) load(ctx context.Context, r Request) (*view, error) {
	if r.Kind == "" {
		r.Kind = send.KindFirstContact
	}
	composed, err := s.db.GetComposition(ctx, r.DraftID)
	if err != nil {
		return nil, err
	}
	v := &view{composed: composed}
	snapshot := composed.Snapshot

	if composed.Revision > 0 && len(composed.Attempts) >= composed.Revision {
		attempt := composed.Attempts[composed.Revision-1]
		v.email = attempt.Email
		v.hash = composition.Hash(attempt.Email)
	}

	if v.approval, err = s.db.LiveApproval(ctx, r.DraftID); err != nil {
		return nil, err
	}
	if v.planRow, err = s.db.GetContactPlan(ctx, snapshot.PlanID); err != nil {
		return nil, err
	}
	if v.company, err = s.db.GetCompany(ctx, snapshot.CompanyID); err != nil {
		return nil, err
	}
	contact, err := s.db.GetContact(ctx, snapshot.Recipient.ContactID)
	contactState := send.ContactState{}
	if err == nil {
		v.contact = contact
		contactState = send.ContactState{Exists: true, ID: contact.ID, CompanyID: contact.CompanyID,
			Email: contact.Value, Status: contact.Status}
	}

	addressSuppressed, err := s.db.IsSuppressed(ctx, snapshot.Recipient.Address)
	if err != nil {
		return nil, err
	}
	domainSuppressed := false
	if v.company.Domain != "" {
		if domainSuppressed, err = s.db.IsSuppressed(ctx, v.company.Domain); err != nil {
			return nil, err
		}
	}

	// The last attempt decides whether both layers passed. Deterministic findings are never
	// erased by the semantic pass, so both are read from the same record.
	deterministic, semantic := false, false
	if composed.Revision > 0 && len(composed.Attempts) >= composed.Revision {
		last := composed.Attempts[composed.Revision-1]
		deterministic = !composition.NeedsReview(last.Validation)
		semantic = last.Review != nil && last.Review.Passed && last.Review.Complete && !composed.ReviewOutdated()
	}

	identity := send.Identity{
		DraftID: r.DraftID, CompositionRevision: composed.Revision, ContentHash: v.hash,
		RecipientContactID: snapshot.Recipient.ContactID, RecipientEmail: snapshot.Recipient.Address,
		PlanID: snapshot.PlanID, PlanRevision: snapshot.PlanRevision, Intent: snapshot.Intent,
		Kind: r.Kind, FollowupStep: r.FollowupStep,
	}

	priorAttempts := []send.PriorAttempt{}
	if err := identity.Validate(); err == nil {
		stored, err := s.db.AttemptsForKey(ctx, identity.Key())
		if err != nil {
			return nil, err
		}
		for _, a := range stored {
			priorAttempts = append(priorAttempts, send.PriorAttempt{ID: a.ID, State: a.State})
		}
	}

	sentToday, lastSendAt, err := s.db.SendVolume(ctx)
	if err != nil {
		return nil, err
	}
	activeThreads, lastFirstContact, err := s.db.DomainSendHistory(ctx, v.company.Domain)
	if err != nil {
		return nil, err
	}

	threadState := send.ThreadState{}
	if r.Kind == send.KindFollowup {
		var thread *store.OutboundThread
		if r.ThreadID > 0 {
			thread, err = s.db.GetThread(ctx, r.ThreadID)
		} else {
			thread, err = s.db.ActiveThreadForContact(ctx, snapshot.Recipient.ContactID)
		}
		if err == nil && thread != nil {
			threadState = send.ThreadState{Exists: true, ID: thread.ID, State: thread.State,
				Intent: plan.Intent(thread.Intent), ContactID: thread.RecipientContactID,
				FollowupStep: thread.FollowupStep}
			if thread.LastSentAt != nil {
				threadState.LastSentAt = *thread.LastSentAt
			}
		}
	}

	v.pre = send.Preflight{
		Now: time.Now().UTC(), Mode: s.mode, Ready: s.readiness, Identity: identity,
		CompositionExists: true, CompositionStatus: composed.Status, CurrentRevision: composed.Revision,
		DeterministicPassed: deterministic, SemanticPassed: semantic, StaleReasons: composed.StaleReasons,
		DeterministicCritical: len(store.ManualApprovalBlockers(snapshot, v.email)) > 0,
		PlanStatus:            v.planRow.Status, Approval: v.approval.ToPolicy(),
		Contact: contactState, CompanyID: snapshot.CompanyID, Domain: v.company.Domain,
		AddressSuppressed: addressSuppressed, DomainSuppressed: domainSuppressed,
		PriorAttempts: priorAttempts, ActiveThreadsForDomain: activeThreads,
		LastFirstContactToDomain: lastFirstContact, SentToday: sentToday, LastSendAt: lastSendAt,
		Thread: threadState, Limits: s.limits,
	}
	return v, nil
}

// Preview runs the preflight without transmitting anything, so the UI can show exactly why a
// send is or is not possible and never offer an enabled button that would fail.
func (s *Service) Preview(ctx context.Context, r Request) (send.Decision, error) {
	v, err := s.load(ctx, r)
	if err != nil {
		return send.Decision{}, err
	}
	return send.Evaluate(v.pre), nil
}

// Send is the canonical path. It is the only function in the application permitted to call a
// mail transport.
func (s *Service) Send(ctx context.Context, r Request) (Outcome, error) {
	if strings.TrimSpace(r.Initiator) == "" {
		return Outcome{}, fmt.Errorf("a send must record who initiated it")
	}
	if s.transport == nil {
		return Outcome{}, fmt.Errorf("no sending transport is configured")
	}
	v, err := s.load(ctx, r)
	if err != nil {
		return Outcome{}, err
	}
	decision := send.Evaluate(v.pre)
	out := Outcome{Decision: decision}
	if !decision.Allowed {
		if decision.ResolvesTo > 0 {
			out.Resolved = true
			if existing, err := s.db.GetSendAttempt(ctx, decision.ResolvesTo); err == nil {
				out.Attempt = existing
			}
		}
		return out, nil
	}

	snapshot := v.composed.Snapshot
	identity := v.pre.Identity
	messageID := send.NewMessageID(decision.Key, s.transport.SenderDomain())

	attempt := store.SendAttempt{
		IdempotencyKey: decision.Key, DraftID: r.DraftID, CompositionRevision: v.composed.Revision,
		ApprovalID: v.approval.ID, CompanyID: snapshot.CompanyID,
		RecipientContactID: identity.RecipientContactID, RecipientEmail: identity.RecipientEmail,
		Intent: string(identity.Intent), Kind: identity.Kind, FollowupStep: identity.FollowupStep,
		Subject: v.email.Subject, Body: v.email.Body, ContentHash: v.hash, MessageID: messageID,
		ProfileRevisionID: snapshot.ProfileRevisionID, PlanID: snapshot.PlanID,
		PlanRevision: snapshot.PlanRevision, Initiator: r.Initiator,
	}
	if snapshot.OpportunityID != 0 {
		id, revision := snapshot.OpportunityID, snapshot.OpportunityRevision
		attempt.OpportunityID, attempt.OpportunityRevision = &id, &revision
	}
	if v.pre.Thread.Exists {
		id := v.pre.Thread.ID
		attempt.ThreadID = &id
	}

	// Reserve before transmitting. A crash from here on leaves a durable record.
	reserved, err := s.db.ReserveSend(ctx, attempt)
	if err == store.ErrAlreadyReserved {
		out.Resolved = true
		out.Decision.Blockers = out.Decision.Blockers.Add("send_in_flight",
			"Another request reserved this exact message first; this one did not transmit.")
		out.Decision.Allowed = false
		if existing, lookupErr := s.db.AttemptsForKey(ctx, decision.Key); lookupErr == nil && len(existing) > 0 {
			out.Attempt = existing[len(existing)-1]
			out.Decision.ResolvesTo = out.Attempt.ID
		}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Attempt = reserved

	if err := s.db.MarkSending(ctx, reserved.ID); err != nil {
		return out, err
	}

	message := send.Message{
		ToAddress: identity.RecipientEmail, ToName: snapshot.Recipient.Name,
		Subject: v.email.Subject, Body: v.email.Body, MessageID: messageID,
	}
	if v.pre.Thread.Exists {
		if thread, err := s.db.GetThread(ctx, v.pre.Thread.ID); err == nil && thread.RootMessageID != "" {
			message.InReplyTo, message.References = thread.RootMessageID, thread.RootMessageID
		}
	}

	result := s.transport.Send(ctx, message)
	settled, err := s.db.SettleSend(ctx, reserved.ID, result)
	if err != nil {
		return out, err
	}
	out.Attempt = settled

	if settled.State != send.StateAccepted {
		return out, nil
	}
	if identity.Kind == send.KindFirstContact {
		thread, err := s.db.OpenThread(ctx, settled)
		if err != nil {
			return out, err
		}
		out.Thread = thread
		return out, nil
	}
	if v.pre.Thread.Exists {
		if err := s.db.RecordThreadSend(ctx, v.pre.Thread.ID, identity.FollowupStep); err != nil {
			return out, err
		}
		if thread, err := s.db.GetThread(ctx, v.pre.Thread.ID); err == nil {
			out.Thread = thread
		}
	}
	return out, nil
}
