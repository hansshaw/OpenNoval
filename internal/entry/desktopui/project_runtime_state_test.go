package desktopui

import (
	"encoding/json"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/host"
)

func TestStateKeepsIdleHostBusyUntilProjectCompletionCleanupReleasesItsSlot(t *testing.T) {
	s := newTestServer(t, true)
	rt := s.rt
	if rt.Snapshot().IsRunning {
		t.Fatal("fixture must start with an idle Host")
	}
	// Deterministically hold the same slot that completion collection normally
	// releases after the engine has published its final lifecycle snapshot.
	s.mu.Lock()
	if s.runSlots == nil {
		s.runSlots = make(map[*host.Host]func())
	}
	released := false
	s.runSlots[rt] = func() { released = true }
	s.mu.Unlock()
	readState := func() struct {
		Busy     bool
		Snapshot host.UISnapshot
	} {
		t.Helper()
		var state struct {
			Busy     bool
			Snapshot host.UISnapshot
		}
		response := request(s, "GET", "/api/state", "")
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &state) != nil {
			t.Fatalf("state: %d %s", response.Code, response.Body.String())
		}
		return state
	}
	state := readState()
	if !state.Busy || state.Snapshot.IsRunning {
		t.Fatalf("idle Host with uncollected completion must still be busy: %+v", state)
	}
	body := mustJSON(t, map[string]any{"directory": s.dir, "name": "收尾后可编辑"})
	if response := request(s, "PATCH", "/api/workspaces", body); response.Code != 409 {
		t.Fatalf("metadata changed before completion cleanup: %d %s", response.Code, response.Body.String())
	}
	s.mu.Lock()
	s.releaseIdleProjectRunLocked(rt)
	clean := released && len(s.runSlots) == 0
	s.mu.Unlock()
	if !clean {
		t.Fatal("completion cleanup did not release the project slot")
	}
	if state := readState(); state.Busy || state.Snapshot.IsRunning {
		t.Fatalf("completed cleanup must restore idle controls: %+v", state)
	}
	if response := request(s, "PATCH", "/api/workspaces", body); response.Code != 200 {
		t.Fatalf("metadata edit was not restored after cleanup: %d %s", response.Code, response.Body.String())
	}
}
