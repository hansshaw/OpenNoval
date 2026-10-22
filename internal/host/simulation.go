package host

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	"github.com/voocel/ainovel-cli/internal/store"
)

// RunStyleAnalysis owns only its model clients and derived simulation profile.
// It neither acquires a novel Host nor enters the creation request limiter; the
// caller owns cancellation and must drain the returned events before closing.
func RunStyleAnalysis(ctx context.Context, cfg bootstrap.Config, bundle assets.Bundle) (<-chan sim.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg = bootstrap.CloneConfig(cfg)
	cfg.AcquireModelRequest = nil
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return nil, err
	}
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, err
	}
	return sim.Run(bootstrap.WithModelRequestAgent(ctx, "simulation"), sim.Deps{
		Store: store.NewStore(cfg.OutputDir),
		LLM:   models.ForRole("architect"),
		Prompts: sim.Prompts{
			Source: bundle.Prompts.SimulationSource,
			Merge:  bundle.Prompts.SimulationMerge,
		},
	}, sim.Options{SourceDir: filepath.Join(cfg.OutputDir, "simulate")})
}

// Simulate runs independently from the chapter engine. Only caller cancellation
// or Host.Close can stop it; a creation pause leaves analysis running.
func (h *Host) Simulate(ctx context.Context) (<-chan sim.Event, error) {
	h.mu.Lock()
	cfg := bootstrap.CloneConfig(h.cfg)
	bundle := h.bundle
	h.mu.Unlock()
	return h.runSimulation(ctx, func(ctx context.Context) (<-chan sim.Event, error) {
		return RunStyleAnalysis(ctx, cfg, bundle)
	})
}

func (h *Host) runSimulation(ctx context.Context, start func(context.Context) (<-chan sim.Event, error)) (<-chan sim.Event, error) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	h.mu.Lock()
	if h.closing || h.simulationDone != nil {
		h.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("Host 正在关闭或已有风格画像任务正在运行")
	}
	h.simulationCancel, h.simulationDone = cancel, done
	// Registration shares Close's lock so no new task can appear after Wait.
	h.asyncWG.Add(1)
	h.mu.Unlock()
	finish := func() {
		cancel()
		h.mu.Lock()
		if h.simulationDone == done {
			h.simulationCancel, h.simulationDone = nil, nil
		}
		h.mu.Unlock()
		close(done)
		h.asyncWG.Done()
	}
	source, err := start(ctx)
	if err != nil {
		finish()
		return nil, err
	}
	out := make(chan sim.Event, 32)
	go func() {
		defer close(out)
		defer finish()
		for event := range source {
			select {
			case out <- event:
			case <-ctx.Done():
				for range source {
				}
				return
			}
		}
	}()
	return out, nil
}
