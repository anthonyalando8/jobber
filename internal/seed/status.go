package seed

import (
	"context"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// SourceInfo describes a discovery source for the operator: what it is, which market it serves
// and how it is accessed. It is the reference the source status page renders, so the reasons a
// source exists (and the terms it is read under) live next to the code that reads it.
type SourceInfo struct {
	Name        string
	Label       string
	Market      string // "kenya" or "international"
	Access      string // how it is read: public API, RSS feed, public HTML listing
	Description string
	// Scheduled is false for sources that only run on demand (manual CSV import).
	Scheduled bool
}

// Catalog lists every source with its description. Order is the order the status page shows.
var Catalog = []SourceInfo{
	{Name: "jobboard", Label: "Kenyan job boards", Market: "kenya", Access: "Public listing pages (MyJobMag, BrighterMonday)", Description: "Local vacancies, searched for backend, Python, Go, PHP and general software roles.", Scheduled: true},
	{Name: "weworkremotely", Label: "We Work Remotely", Market: "international", Access: "Public RSS category feeds", Description: "Remote programming, back-end, full-stack and DevOps roles, with the region each listing accepts.", Scheduled: true},
	{Name: "remoteok", Label: "Remote OK", Market: "international", Access: "Public JSON API", Description: "Remote roles from startups and small companies; publishes employer and apply links.", Scheduled: true},
	{Name: "remotive", Label: "Remotive", Market: "international", Access: "Public JSON API (software-dev category)", Description: "Remote software roles with the candidate location each one accepts.", Scheduled: true},
	{Name: "himalayas", Label: "Himalayas", Market: "international", Access: "Public JSON API", Description: "Remote roles with explicit location restrictions.", Scheduled: true},
	{Name: "jobicy", Label: "Jobicy", Market: "international", Access: "Public JSON API (development category)", Description: "Remote developer roles with a stated geography for each listing.", Scheduled: true},
	{Name: "workingnomads", Label: "Working Nomads", Market: "international", Access: "Public JSON API", Description: "Remote development roles aggregated from employers.", Scheduled: true},
	{Name: "ats", Label: "Employer career boards", Market: "international", Access: "Public Greenhouse, Lever and Ashby job-board APIs", Description: "Engineering roles read straight from employers' own applicant tracking boards: those configured in ATS_BOARDS plus any board a discovered listing links to.", Scheduled: true},
	{Name: "manual", Label: "Manual import", Market: "kenya", Access: "Pasted CSV", Description: "Companies you add yourself.", Scheduled: false},
}

// InfoFor returns the catalog entry for a source name.
func InfoFor(name string) (SourceInfo, bool) {
	for _, info := range Catalog {
		if info.Name == name {
			return info, true
		}
	}
	return SourceInfo{}, false
}

// SourceStatus is the last recorded outcome of a source run.
type SourceStatus struct {
	SourceInfo
	LastRun  time.Time
	OK       bool
	Added    int
	Error    string
	Duration time.Duration
	// LastSuccess is when the source last ran without error, so a source that has been failing
	// for days reads differently from one that failed once.
	LastSuccess time.Time
}

func statusKey(name string) string { return "source:status:" + name }

// recordStatus stores the outcome of a run. It never fails the run: status is an observation.
func recordStatus(ctx context.Context, rdb *redis.Client, name string, added int, duration time.Duration, runErr error) {
	if rdb == nil {
		return
	}
	now := time.Now().UTC()
	fields := map[string]any{
		"last_run": now.Format(time.RFC3339), "added": added,
		"duration_ms": duration.Milliseconds(), "ok": runErr == nil, "error": "",
	}
	if runErr != nil {
		fields["error"] = truncate(runErr.Error(), 300)
	} else {
		fields["last_success"] = now.Format(time.RFC3339)
	}
	if err := rdb.HSet(ctx, statusKey(name), fields).Err(); err != nil {
		log.Printf("seed: recording %s status: %v", name, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Statuses returns every catalogued source that is registered, with its last recorded run.
func (r *Registry) Statuses(ctx context.Context, rdb *redis.Client) []SourceStatus {
	registered := map[string]bool{}
	for _, name := range r.List() {
		registered[name] = true
	}
	out := []SourceStatus{}
	for _, info := range Catalog {
		if !registered[info.Name] {
			continue
		}
		status := SourceStatus{SourceInfo: info}
		if rdb != nil {
			values, err := rdb.HGetAll(ctx, statusKey(info.Name)).Result()
			if err == nil {
				status.LastRun, _ = time.Parse(time.RFC3339, values["last_run"])
				status.LastSuccess, _ = time.Parse(time.RFC3339, values["last_success"])
				status.OK = values["ok"] == "1" || values["ok"] == "true"
				status.Added, _ = strconv.Atoi(values["added"])
				status.Error = values["error"]
				ms, _ := strconv.Atoi(values["duration_ms"])
				status.Duration = time.Duration(ms) * time.Millisecond
			}
		}
		out = append(out, status)
	}
	return out
}

// Scheduled returns the registered sources that run automatically, in catalog order.
func (r *Registry) Scheduled() []string {
	registered := map[string]bool{}
	for _, name := range r.List() {
		registered[name] = true
	}
	out := []string{}
	for _, info := range Catalog {
		if info.Scheduled && registered[info.Name] {
			out = append(out, info.Name)
		}
	}
	// Anything registered but not catalogued still runs, so a new adapter is never silently idle.
	extra := []string{}
	for name := range registered {
		if _, ok := InfoFor(name); !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// Due reports whether a source's last attempt is older than interval, and marks the attempt when
// it is. Using SETNX with an expiry makes it a lease: two overlapping callers cannot both run the
// same source, and a crashed run frees the source when the lease lapses.
func Due(ctx context.Context, rdb *redis.Client, name string, interval time.Duration) bool {
	if rdb == nil {
		return true
	}
	ok, err := rdb.SetNX(ctx, "source:lease:"+name, time.Now().Unix(), interval).Result()
	if err != nil {
		log.Printf("seed: lease for %s: %v (running anyway)", name, err)
		return true
	}
	return ok
}

// ClearLease lets a source run again immediately, for an explicit operator request.
func ClearLease(ctx context.Context, rdb *redis.Client, name string) {
	if rdb != nil {
		rdb.Del(ctx, "source:lease:"+name)
	}
}
