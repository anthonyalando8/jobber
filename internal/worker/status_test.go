package worker

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWorkerStatusReportsActualLoopAndCompletion(t *testing.T) {
	s := &Scheduler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go s.loop(ctx, &wg, "research", time.Hour, func(context.Context) { close(entered); <-release })
	<-entered
	states := s.Status()
	if len(states) != 1 || !states[0].Busy || states[0].AliveAt.IsZero() || states[0].StartedAt.IsZero() {
		t.Fatal("running loop not accurately reported")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		states = s.Status()
		if !states[0].Busy {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if states[0].Busy || states[0].CompletedAt.IsZero() {
		t.Fatal("completed cycle not accurately reported")
	}
	cancel()
	wg.Wait()
}
func TestUnstartedSchedulerDoesNotClaimWorkersRunning(t *testing.T) {
	s := &Scheduler{}
	if len(s.Status()) != 0 {
		t.Fatal("unstarted workers misrepresented")
	}
}
