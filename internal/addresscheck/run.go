package addresscheck

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/anthonyalando8/prospector/internal/composition"
	"github.com/anthonyalando8/prospector/internal/store"
)

// DefaultAIDaily bounds AI address opinions per day (ADDRESS_AI_DAILY). One opinion is one
// request, drawn from the same AI request budget as email composition and review.
const DefaultAIDaily = 20

// Runner checks waiting addresses.
type Runner struct {
	DB       *store.DB
	Resolver Resolver
	// Judge gives AI opinions; nil means AI checks are unavailable.
	Judge Judge
	// AIDaily caps AI opinions per day; HourLimit and DayLimit are the shared AI request budget.
	AIDaily, HourLimit, DayLimit int
	Now                          func() time.Time
}

// FromEnvironment builds a runner from the same AI settings email review uses.
func FromEnvironment(db *store.DB) *Runner {
	r := &Runner{DB: db, Resolver: netResolver{}, AIDaily: DefaultAIDaily}
	r.HourLimit, _ = strconv.Atoi(os.Getenv("AI_REQUESTS_PER_HOUR"))
	r.DayLimit, _ = strconv.Atoi(os.Getenv("AI_REQUESTS_PER_DAY"))
	if n, err := strconv.Atoi(os.Getenv("ADDRESS_AI_DAILY")); err == nil && n >= 0 {
		r.AIDaily = n
	}
	if os.Getenv("EMAIL_COMPOSITION_ENABLED") == "true" {
		if p, err := composition.NewConfigured("REVIEW"); err == nil {
			r.Judge = ProviderJudge{Provider: p}
		}
	}
	return r
}

// Result is what one run did.
type RunResult struct {
	Checked, AIJudged    int
	AIOff, AIUnavailable bool
	AIBudgetReason       string
	Verdicts, AIVerdicts map[string]int
}

const actor = "address-check:" + Version

// Run checks every waiting address without a check of this version (free), then, when the
// operator switched AI checks on, asks for AI opinions within the caps.
func (r *Runner) Run(ctx context.Context, limit, aiLimit int) (RunResult, error) {
	res := RunResult{Verdicts: map[string]int{}, AIVerdicts: map[string]int{}}
	subjects, err := r.DB.AddressCheckSubjects(ctx, Version, limit)
	if err != nil {
		return res, err
	}
	for _, s := range subjects {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		det := Check(ctx, r.Resolver, s)
		if _, err := r.record(ctx, s, det, Opinion{}, "", actor); err != nil {
			return res, err
		}
		res.Checked++
		res.Verdicts[det.Verdict]++
	}

	if !r.DB.SettingOn(ctx, store.SettingAddressAI) {
		res.AIOff = true
		return res, nil
	}
	if r.Judge == nil {
		res.AIUnavailable = true
		return res, nil
	}
	subjects, err = r.DB.AIAddressSubjects(ctx, Version, aiLimit)
	if err != nil {
		return res, err
	}
	for _, s := range subjects {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if ok, reason := r.aiAllowed(ctx); !ok {
			res.AIBudgetReason = reason
			break
		}
		det := Check(ctx, r.Resolver, s)
		opinion, model := r.judge(ctx, s, det)
		if _, err := r.record(ctx, s, det, opinion, model, actor); err != nil {
			return res, err
		}
		res.AIJudged++
		res.AIVerdicts[opinion.Verdict]++
	}
	return res, nil
}

// CheckOne is a person asking for a check now, optionally with an AI opinion. The AI part still
// respects the budget; when it is spent, the deterministic check is recorded on its own and the
// reason is returned.
func (r *Runner) CheckOne(ctx context.Context, candidateID int64, withAI bool, by string) (store.AddressCheck, string, error) {
	s, err := r.DB.AddressCheckSubject(ctx, candidateID)
	if err != nil {
		return store.AddressCheck{}, "", err
	}
	det := Check(ctx, r.Resolver, *s)
	opinion, model, note := Opinion{}, "", ""
	if withAI {
		switch ok, reason := r.aiAllowed(ctx); {
		case r.Judge == nil:
			note = "AI checks are not configured (no review provider)."
		case !ok:
			note = reason
		default:
			opinion, model = r.judge(ctx, *s, det)
		}
	}
	check, err := r.record(ctx, *s, det, opinion, model, by)
	return check, note, err
}

func (r *Runner) judge(ctx context.Context, s store.AddressCheckSubject, det Result) (Opinion, string) {
	callCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	opinion, model, err := r.Judge.Judge(callCtx, s, det)
	if err != nil {
		return Opinion{Verdict: AIFailed, Reasons: []string{"The AI request failed; the deterministic check stands on its own."}}, model
	}
	return opinion, model
}

func (r *Runner) aiAllowed(ctx context.Context) (bool, string) {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if r.AIDaily <= 0 {
		return false, "AI address checks have no daily allowance (ADDRESS_AI_DAILY)."
	}
	if n, err := r.DB.AIAddressChecksSince(ctx, day); err != nil {
		return false, "Could not read today's AI address checks."
	} else if n >= r.AIDaily {
		return false, fmt.Sprintf("Today's %d AI address checks are used.", r.AIDaily)
	}
	if r.HourLimit <= 0 || r.DayLimit <= 0 {
		return false, "No AI request budget is configured (AI_REQUESTS_PER_HOUR / AI_REQUESTS_PER_DAY)."
	}
	hour, dayUsed, err := r.DB.AIRequestsLastHourAndDay(ctx)
	if err != nil {
		return false, "Could not read the AI request budget."
	}
	if hour >= r.HourLimit || dayUsed >= r.DayLimit {
		return false, "The shared AI request budget is used up for now."
	}
	return true, ""
}

func (r *Runner) record(ctx context.Context, s store.AddressCheckSubject, det Result, o Opinion, model, by string) (store.AddressCheck, error) {
	check := store.AddressCheck{
		CandidateID: s.CandidateID, Email: s.Email, Version: Version, Verdict: det.Verdict,
		MailServers: det.MailServers, Checks: det.Checks, AIVerdict: o.Verdict, AIReasons: o.Reasons,
		AIModel: model, CheckedBy: by, CheckedAt: time.Now(),
	}
	if check.AIVerdict == "" {
		check.AIVerdict = store.AIAddressNotRun
	}
	id, err := r.DB.RecordAddressCheck(ctx, check)
	check.ID = id
	return check, err
}

type netResolver struct{}

func (netResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	return net.DefaultResolver.LookupMX(ctx, name)
}
func (netResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}
