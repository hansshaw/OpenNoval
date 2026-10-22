package projectjobs

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ReserveProject counts foreground execution alongside background projects.
// Admission never waits: HTTP callers may hold their own server lock. The
// caller releases only after its Host has stopped, including cancellation.
func (s *Scheduler) ReserveProject(ctx context.Context, projectID string) (func(), error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("project ID is required")
	}
	err := s.call(ctx, func(st *state) error {
		if st.closing {
			return ErrClosed
		}
		if st.persistenceErr != nil {
			return st.persistenceErr
		}
		if st.activeProjects[projectID] > 0 || st.queuedProjects[projectID] > 0 {
			return ErrProjectBusy
		}
		if len(st.activeProjects) >= st.limits.MaxProjects {
			return ErrProjectLimit
		}
		st.reservedProjects[projectID] = true
		st.activeProjects[projectID]++
		return nil
	})
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = s.call(context.Background(), func(st *state) error {
				delete(st.reservedProjects, projectID)
				st.activeProjects[projectID]--
				if st.activeProjects[projectID] == 0 {
					delete(st.activeProjects, projectID)
				}
				return nil
			})
		})
	}, nil
}

// AcquireModel waits for one real model request, independent of project slots.
// Holding or waiting for this permit never occupies the scheduler command loop.
// The release callback is idempotent and must run after the stream is closed.
func (s *Scheduler) AcquireModel(ctx context.Context, providerID string) (func(), error) {
	if ctx == nil || strings.TrimSpace(providerID) == "" {
		return nil, fmt.Errorf("context and provider ID are required")
	}
	return s.modelRequests.acquire(ctx, providerID)
}

type modelRequestLimiter struct {
	mu        sync.Mutex
	limits    Limits
	active    int
	providers map[string]int
	changed   chan struct{}
	closed    bool
}

func newModelRequestLimiter(limits Limits) *modelRequestLimiter {
	return &modelRequestLimiter{limits: cloneLimits(limits), providers: make(map[string]int), changed: make(chan struct{})}
}

func (m *modelRequestLimiter) wakeLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

func (m *modelRequestLimiter) setLimits(limits Limits) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limits = cloneLimits(limits)
	m.wakeLocked()
}

func (m *modelRequestLimiter) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.wakeLocked()
}

func (m *modelRequestLimiter) acquire(ctx context.Context, provider string) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if m.closed {
			m.mu.Unlock()
			return nil, ErrClosed
		}
		limit := m.limits.ProviderLimits[provider]
		if limit == 0 {
			limit = m.limits.ProviderLimit
		}
		if m.active < m.limits.MaxConcurrent && m.providers[provider] < limit {
			m.active++
			m.providers[provider]++
			m.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					m.mu.Lock()
					defer m.mu.Unlock()
					m.active--
					m.providers[provider]--
					if m.providers[provider] == 0 {
						delete(m.providers, provider)
					}
					m.wakeLocked()
				})
			}, nil
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}
