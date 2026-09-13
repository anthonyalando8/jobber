package worker

import (
	"sync"
	"time"
)

type LoopStatus struct {
	Name          string
	AliveAt       time.Time
	StartedAt     time.Time
	CompletedAt   time.Time
	Busy          bool
	Company       string
	Failures      int
	LastFailureAt time.Time
}
type RuntimeStatus struct {
	mu    sync.RWMutex
	loops map[string]LoopStatus
}

func (s *Scheduler) Status() []LoopStatus {
	s.runtime.mu.RLock()
	defer s.runtime.mu.RUnlock()
	result := make([]LoopStatus, 0, len(s.runtime.loops))
	for _, name := range []string{"discovery", "research", "domain-resolve"} {
		if state, ok := s.runtime.loops[name]; ok {
			result = append(result, state)
		}
	}
	return result
}
func (s *Scheduler) updateStatus(name string, change func(*LoopStatus)) {
	s.runtime.mu.Lock()
	defer s.runtime.mu.Unlock()
	if s.runtime.loops == nil {
		s.runtime.loops = map[string]LoopStatus{}
	}
	state := s.runtime.loops[name]
	state.Name = name
	change(&state)
	s.runtime.loops[name] = state
}

// Observation only: this does not retry, skip, or alter any work eligibility.
func (s *Scheduler) recordFailure(name string) {
	s.updateStatus(name, func(state *LoopStatus) { state.Failures++; state.LastFailureAt = time.Now() })
}
