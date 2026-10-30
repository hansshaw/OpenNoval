package host

import (
	"errors"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/bootstrap"
)

func TestReconfigureModelsRejectsActiveWorkIncludingPausedEngine(t *testing.T) {
	for _, state := range []string{"running", "paused-engine", "cocreating", "exclusive", "closing"} {
		t.Run(state, func(t *testing.T) {
			h, _ := newModelConfigTestHost(t)
			switch state {
			case "running":
				h.lifecycle = lifecycleRunning
			case "paused-engine":
				h.lifecycle = lifecyclePaused
				h.engine = &engine{running: true}
			case "cocreating":
				h.cocreating = true
			case "exclusive":
				h.exclusive = "导入"
			case "closing":
				h.closing = true
			}
			persisted := false
			err := h.ReconfigureModels(bootstrap.CloneConfig(h.cfg), func() error {
				persisted = true
				return nil
			})
			if !errors.Is(err, ErrModelConfigurationBusy) || persisted {
				t.Fatalf("active work accepted or persisted, err = %v, persisted = %v", err, persisted)
			}
		})
	}
}
