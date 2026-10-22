package projectjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// BenchmarkDurableReportBurst exercises the real compact JSON + fsync journal
// with 2000 retained 1 KiB event payloads. It measures queue/report persistence,
// not model throughput, project disk scanning, or machine-wide concurrency.
func BenchmarkDurableReportBurst(b *testing.B) {
	for _, count := range []int{64, 128} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "jobs.json")
			payload, _ := json.Marshal(map[string]string{"detail": strings.Repeat("x", 1024)})
			j := journal{Version: 1, NextSequence: 2001, Jobs: []Job{{ID: "history", Request: Request{ProjectID: "history", ProviderID: "one", Kind: "batch"}, Status: Succeeded, CreatedAt: time.Now().UTC()}}}
			for i := uint64(1); i <= 2000; i++ {
				j.Events = append(j.Events, Event{Sequence: i, JobID: "history", ProjectID: "history", ProviderID: "one", Kind: "progress", Payload: payload})
			}
			if err := saveJournal(path, j); err != nil {
				b.Fatal(err)
			}
			ready := make(chan Reporter, count)
			s, err := New(Config{MaxProjects: count, MaxConcurrent: count, PersistencePath: path}, RunnerFunc(func(ctx context.Context, job Job, report Reporter) error {
				ready <- report
				<-ctx.Done()
				return ctx.Err()
			}))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := s.Close(context.Background()); err != nil {
					b.Error(err)
				}
			})
			for i := 0; i < count; i++ {
				if _, err := s.Submit(context.Background(), Request{ProjectID: fmt.Sprint(i), ProviderID: "one", Kind: "batch"}); err != nil {
					b.Fatal(err)
				}
			}
			reporters := make([]Reporter, count)
			for i := range reporters {
				reporters[i] = <-ready
			}
			b.ReportAllocs()
			b.SetBytes(int64(count * 4 * len(payload)))
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				var wg sync.WaitGroup
				results := make(chan error, count)
				for _, report := range reporters {
					wg.Add(1)
					go func(report Reporter) {
						defer wg.Done()
						for i := 0; i < 4; i++ {
							if err := report.Emit("progress", "durable burst", payload); err != nil {
								results <- err
								return
							}
						}
						results <- report.RecordCost(0.01)
					}(report)
				}
				wg.Wait()
				for i := 0; i < count; i++ {
					if err := <-results; err != nil {
						b.Fatal(err)
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(count*5), "reports/op")
		})
	}
}
