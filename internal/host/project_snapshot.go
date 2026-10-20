package host

import (
	"errors"
)

var ErrProjectSnapshotBusy = errors.New("请先暂停并等待当前操作完全结束后再复制项目")

// WithProjectSnapshot flushes usage and reads an idle project's files while
// excluding engine startup, interventions, and exclusive jobs. The caller must
// also serialize its own startup preparation and project edits. Snapshot must
// not call back into Host: it runs under the Host's lifecycle mutex.
func (h *Host) WithProjectSnapshot(snapshot func() error) error {
	// An intervention can write the store while the engine is paused. Refuse it
	// immediately rather than wait for an in-flight model request.
	if !h.interMu.TryLock() {
		return ErrProjectSnapshotBusy
	}
	defer h.interMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing || h.lifecycle == lifecycleRunning || (h.engine != nil && h.engine.isRunning()) || h.cocreating || h.exclusive != "" {
		return ErrProjectSnapshotBusy
	}
	return h.usage.withStableSnapshot(snapshot)
}
