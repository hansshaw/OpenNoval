package bootstrap

import (
	"context"
	"sync"
)

// Each ModelSet owns these tasks, including retired clients after a hot swap.
// Journal files are shared by path, so lifecycle must never belong to Journal:
// shutting down one Host cannot wait for another project's requests.
type modelRequestLifecycle struct {
	mu     sync.Mutex
	closed bool
	next   uint64
	active map[uint64]context.CancelFunc
	wg     sync.WaitGroup
}

func (l *modelRequestLifecycle) begin(ctx context.Context) (context.Context, func(), error) {
	if l == nil {
		return ctx, func() {}, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	l.next++
	id := l.next
	if l.active == nil {
		l.active = make(map[uint64]context.CancelFunc)
	}
	l.active[id] = cancel
	l.wg.Add(1)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			l.mu.Lock()
			delete(l.active, id)
			l.mu.Unlock()
			l.wg.Done()
		})
	}, nil
}

func (l *modelRequestLifecycle) stop() {
	l.mu.Lock()
	l.closed = true
	cancels := make([]context.CancelFunc, 0, len(l.active))
	for _, cancel := range l.active {
		cancels = append(cancels, cancel)
	}
	l.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// CloseRequests cancels and joins every request-log producer owned by this
// ModelSet before the Host releases its project lease. It is safe to call more
// than once. No model, fallback or persistence lock is held while waiting.
func (ms *ModelSet) CloseRequests() {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	ms.requestsClosed = true
	lifecycles := make([]*modelRequestLifecycle, 0, len(ms.requestLifecycles))
	for lifecycle := range ms.requestLifecycles {
		lifecycles = append(lifecycles, lifecycle)
	}
	ms.mu.Unlock()
	for _, lifecycle := range lifecycles {
		lifecycle.stop()
	}
	for _, lifecycle := range lifecycles {
		lifecycle.wg.Wait()
	}
}
