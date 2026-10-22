package projectjobs

import (
	"context"
	"fmt"
	"strings"
)

func normalizeLimits(limits Limits) (Limits, error) {
	valid := func(value int) bool { return value >= 1 && value <= MaxConcurrency }
	if limits.MaxProjects == 0 {
		limits.MaxProjects = DefaultMaxProjects
	}
	if !valid(limits.MaxProjects) {
		return Limits{}, fmt.Errorf("%w: maxProjects must be between 1 and %d", ErrInvalidLimits, MaxConcurrency)
	}
	if !valid(limits.MaxConcurrent) || !valid(limits.ProviderLimit) {
		return Limits{}, fmt.Errorf("%w: maxConcurrent and providerLimit must be between 1 and %d", ErrInvalidLimits, MaxConcurrency)
	}
	result := Limits{MaxProjects: limits.MaxProjects, MaxConcurrent: limits.MaxConcurrent, ProviderLimit: limits.ProviderLimit, ProviderLimits: make(map[string]int, len(limits.ProviderLimits))}
	for provider, value := range limits.ProviderLimits {
		if strings.TrimSpace(provider) == "" || !valid(value) {
			return Limits{}, fmt.Errorf("%w: provider overrides require a nonempty ID and a limit between 1 and %d", ErrInvalidLimits, MaxConcurrency)
		}
		result.ProviderLimits[provider] = value
	}
	return result, nil
}

func cloneLimits(limits Limits) Limits {
	result := Limits{MaxProjects: limits.MaxProjects, MaxConcurrent: limits.MaxConcurrent, ProviderLimit: limits.ProviderLimit, ProviderLimits: make(map[string]int, len(limits.ProviderLimits))}
	for provider, limit := range limits.ProviderLimits {
		result.ProviderLimits[provider] = limit
	}
	return result
}

func (s *Scheduler) Limits(ctx context.Context) (Limits, error) {
	var result Limits
	err := s.call(ctx, func(st *state) error {
		result = cloneLimits(st.limits)
		return nil
	})
	return result, err
}

// SetLimits updates the running scheduler without replacing it. Raising a limit
// makes queued jobs eligible immediately. Lowering it leaves active jobs alone
// and blocks new dispatch until the affected slots have drained below the limit.
// Limits are saved before they become effective. A failed settings write does
// not cancel work or change the effective limits, unlike a failed cost journal.
func (s *Scheduler) SetLimits(ctx context.Context, limits Limits) (Limits, error) {
	next, err := normalizeLimits(limits)
	if err != nil {
		return Limits{}, err
	}
	var result Limits
	err = s.call(ctx, func(st *state) error {
		if st.closing {
			return ErrClosed
		}
		// Only the limits pointer changes; the command loop owns the remaining
		// journal data, which saveJournal reads synchronously without mutation.
		candidate := st.journal
		candidate.Limits = &next
		if err := saveJournal(st.cfg.PersistencePath, candidate); err != nil {
			return fmt.Errorf("persist project job limits: %w", err)
		}
		st.journal, st.limits = candidate, next
		s.modelRequests.setLimits(next)
		st.persistenceErr = nil // the complete current journal was flushed
		result = cloneLimits(next)
		return nil
	})
	return result, err
}
