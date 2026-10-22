package store

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

var errInjectedOutlineMutation = errors.New("injected outline mutation failure")

func TestOutlineMutationsRollbackEveryFileWrite(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		setup func(*testing.T) *Store
		run   func(*Store) error
	}{
		{
			name:  "replace flat outline",
			paths: outlineMutationFiles,
			setup: func(t *testing.T) *Store {
				return planningMutationStore(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 2)})
			},
			run: func(s *Store) error {
				_, err := s.ReplaceFlatOutline([]domain.OutlineEntry{testOutlineEntry("新一"), testOutlineEntry("新二")})
				return err
			},
		},
		{
			name:  "replace layered outline",
			paths: outlineMutationFiles,
			setup: func(t *testing.T) *Store {
				return planningMutationStore(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 2)})
			},
			run: func(s *Store) error {
				_, err := s.ReplaceLayeredOutline([]domain.VolumeOutline{
					{Title: "新第一卷", Arcs: []domain.ArcOutline{{
						Title: "新一弧", Goal: "新目标", Chapters: []domain.OutlineEntry{testOutlineEntry("新一")},
					}}},
					{Title: "新第二卷", Arcs: []domain.ArcOutline{{
						Title: "新二弧", Goal: "新目标二", Chapters: []domain.OutlineEntry{testOutlineEntry("新二")},
					}}},
				})
				return err
			},
		},
		{
			name:  "expand next arc",
			paths: outlineMutationFiles,
			setup: func(t *testing.T) *Store {
				s := planningMutationStore(t, []domain.VolumeOutline{{
					Title: "第一卷",
					Arcs: []domain.ArcOutline{
						{Title: "已展开弧", Goal: "开局", Chapters: []domain.OutlineEntry{testOutlineEntry("章一")}},
						{Title: "历史骨架", Goal: "承接", EstimatedChapters: 3},
					},
				}})
				p, err := s.Progress.Load()
				if err != nil {
					t.Fatal(err)
				}
				p.CompletedChapters = []int{1}
				if err := s.Progress.Save(p); err != nil {
					t.Fatal(err)
				}
				return s
			},
			run: func(s *Store) error {
				_, err := s.ExpandNextArc(domain.ArcExpansion{
					Title: "展开弧", Goal: "完成承接",
					Chapters: []domain.OutlineEntry{testOutlineEntry("章二")},
				})
				return err
			},
		},
		{
			name:  "append volume",
			paths: outlineMutationFiles,
			setup: func(t *testing.T) *Store {
				return planningMutationStore(t, []domain.VolumeOutline{testOutlineVolume(1, "第一卷", 1)})
			},
			run: func(s *Store) error {
				_, err := s.AppendVolume(domain.VolumeOutline{
					Title: "第二卷",
					Arcs: []domain.ArcOutline{{
						Title: "第二弧", Goal: "继续", Chapters: []domain.OutlineEntry{testOutlineEntry("章二")},
					}},
				})
				return err
			},
		},
		{
			name:  "revise layered outline",
			paths: outlineMutationFiles,
			setup: func(t *testing.T) *Store {
				return planningMutationStore(t, []domain.VolumeOutline{testOutlineVolume(1, "旧卷", 2)})
			},
			run: func(s *Store) error {
				_, err := s.ReviseOutline(2, []domain.OutlineEntry{testOutlineEntry("修订章二")})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, failPath := range test.paths {
				t.Run(failPath, func(t *testing.T) {
					s := test.setup(t)
					before := captureOutlineMutationSnapshot(t, s)
					installOutlineMutationFailure(t, s, failPath)

					err := test.run(s)
					if !errors.Is(err, errInjectedOutlineMutation) {
						t.Fatalf("expected injected failure for %s, got %v", failPath, err)
					}
					assertOutlineMutationSnapshot(t, s, before)
				})
			}
		})
	}
}

func TestReviseFlatOutlineRollsBackEveryFileWrite(t *testing.T) {
	for _, failPath := range []string{"outline.json", "outline.md", "meta/progress.json"} {
		t.Run(failPath, func(t *testing.T) {
			s := NewStore(t.TempDir())
			if err := s.Init(); err != nil {
				t.Fatal(err)
			}
			if err := s.Progress.Init(2); err != nil {
				t.Fatal(err)
			}
			if err := s.Outline.SaveOutline([]domain.OutlineEntry{testOutlineEntry("旧一"), testOutlineEntry("旧二")}); err != nil {
				t.Fatal(err)
			}
			if err := s.RunMeta.SetTargetChapters(0); err != nil {
				t.Fatal(err)
			}
			if err := s.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
				w.PlanningRevisionPending = true
				w.PlanningRevisionRequest = "测试全量回滚"
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			before := captureOutlineMutationSnapshot(t, s)
			installOutlineMutationFailure(t, s, failPath)
			_, err := s.ReviseOutline(2, []domain.OutlineEntry{testOutlineEntry("新二")})
			if !errors.Is(err, errInjectedOutlineMutation) {
				t.Fatalf("expected injected failure for %s, got %v", failPath, err)
			}
			assertOutlineMutationSnapshot(t, s, before)
		})
	}
}

func planningMutationStore(t *testing.T, volumes []domain.VolumeOutline) *Store {
	t.Helper()
	s := setupLayered(t, volumes)
	if err := s.RunMeta.SetTargetChapters(0); err != nil {
		t.Fatal(err)
	}
	if err := s.RunMeta.UpdateWorkbench(func(w *domain.WorkbenchControl) error {
		w.PlanningRevisionPending = true
		w.PlanningRevisionRequest = "测试规划写入回滚"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func installOutlineMutationFailure(t *testing.T, s *Store, failPath string) {
	t.Helper()
	failPath = strings.ReplaceAll(failPath, "\\", "/")
	fired := false
	hook := func(rel string) error {
		if !fired && strings.ReplaceAll(rel, "\\", "/") == failPath {
			fired = true
			return errInjectedOutlineMutation
		}
		return nil
	}
	s.Outline.io.setMutationHookForTest(hook)
	s.Progress.io.setMutationHookForTest(hook)
	s.RunMeta.io.setMutationHookForTest(hook)
}

func captureOutlineMutationSnapshot(t *testing.T, s *Store) []outlineMutationSnapshot {
	t.Helper()
	snapshots, err := s.snapshotOutlineMutationFiles()
	if err != nil {
		t.Fatal(err)
	}
	return snapshots
}

func assertOutlineMutationSnapshot(t *testing.T, s *Store, before []outlineMutationSnapshot) {
	t.Helper()
	after, err := s.snapshotOutlineMutationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("snapshot file count changed: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		if before[i].rel != after[i].rel {
			t.Fatalf("snapshot order changed: before=%s after=%s", before[i].rel, after[i].rel)
		}
		if before[i].exists != after[i].exists {
			t.Fatalf("%s existence changed: before=%v after=%v", before[i].rel, before[i].exists, after[i].exists)
		}
		if before[i].exists && !bytes.Equal(before[i].data, after[i].data) {
			t.Fatalf("%s content changed after rollback", before[i].rel)
		}
	}
}
