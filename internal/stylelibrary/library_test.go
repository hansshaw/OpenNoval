package stylelibrary

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/domain"
)

func writeTestFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testProfile() string {
	data, _ := json.Marshal(domain.SimulationProfile{Version: domain.SimulationProfileVersion})
	return string(data)
}

func TestCreateRenameListAndDynamicAnalysisState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "styles")
	library, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	same, err := Open(root)
	if err != nil || same != library {
		t.Fatal("same root did not share synchronization", err)
	}
	entry, err := library.Create("  都市悬疑  ")
	if err != nil || entry.Name != "都市悬疑" || !validID.MatchString(entry.ID) || entry.Ready || entry.SourceCount != 0 {
		t.Fatalf("invalid new style: %+v %v", entry, err)
	}
	directory, err := library.Directory(entry.ID)
	if err != nil || directory != filepath.Join(root, "entries", entry.ID) {
		t.Fatal("invalid workspace", directory, err)
	}
	writeTestFile(t, filepath.Join(directory, "simulate", "nested", "source.MD"), "短句推进")
	writeTestFile(t, filepath.Join(directory, "simulate", "ignore.json"), "secret unrelated config")
	writeTestFile(t, filepath.Join(directory, "meta", "simulation_profile.json"), testProfile())
	updated, err := library.Get(entry.ID)
	if err != nil || updated.SourceCount != 1 || !updated.Ready || updated.UpdatedAt.Before(entry.UpdatedAt) {
		t.Fatalf("analysis state not reflected: %+v %v", updated, err)
	}
	renamed, err := library.Rename(entry.ID, "新的风格")
	if err != nil || renamed.Name != "新的风格" || renamed.ID != entry.ID || !renamed.Ready {
		t.Fatalf("rename lost entry: %+v %v", renamed, err)
	}
	list, err := library.List()
	if err != nil || len(list) != 1 || list[0].Name != renamed.Name || list[0].SourceCount != 1 {
		t.Fatalf("invalid list: %+v %v", list, err)
	}
	// A fresh instance reads persisted metadata rather than in-memory entries.
	reloaded := &Library{root: root}
	if got, err := reloaded.Get(entry.ID); err != nil || got.Name != renamed.Name {
		t.Fatalf("rename not persistent: %+v %v", got, err)
	}
	for _, name := range []string{"", " ", "bad\nname", strings.Repeat("字", 81), string([]byte{0xff})} {
		if _, err := library.Create(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("invalid name accepted: %q %v", name, err)
		}
	}
	for _, id := range []string{"../outside", "", entry.ID + "/x", strings.Repeat("a", 31)} {
		if _, err := library.Get(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("invalid ID accepted: %q %v", id, err)
		}
	}
	if _, err := library.Get(strings.Repeat("b", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing entry not reported", err)
	}
}

func TestLegacyMigrationCopiesOnlyStyleAndNeverOverwrites(t *testing.T) {
	library, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	writeTestFile(t, filepath.Join(project, "simulate", "nested", "sample.txt"), "原始语料")
	writeTestFile(t, filepath.Join(project, "simulate", "config.json"), "credential-secret")
	writeTestFile(t, filepath.Join(project, "meta", "simulation_profile.json"), testProfile())
	writeTestFile(t, filepath.Join(project, "meta", "run_meta.json"), "story-secret")
	writeTestFile(t, filepath.Join(project, "chapters", "01.md"), "chapter-secret")
	entry, migrated, err := library.ImportLegacy(project, "旧风格")
	if err != nil || !migrated || entry.SourceCount != 1 || !entry.Ready || !strings.HasPrefix(entry.ID, "legacy-") {
		t.Fatalf("migration failed: %+v %v %v", entry, migrated, err)
	}
	directory, _ := library.Directory(entry.ID)
	for _, relative := range []string{"simulate/config.json", "meta/run_meta.json", "chapters/01.md"} {
		if _, err := os.Stat(filepath.Join(directory, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("migration copied unrelated file: %s %v", relative, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(project, "simulate", "nested", "sample.txt"))
	if err != nil || string(data) != "原始语料" {
		t.Fatal("original corpus was modified", err)
	}
	writeTestFile(t, filepath.Join(directory, "simulate", "nested", "sample.txt"), "库内修订")
	writeTestFile(t, filepath.Join(project, "simulate", "nested", "sample.txt"), "项目新内容")
	second, migrated, err := library.ImportLegacy(filepath.Join(project, "."), "不会覆盖名称")
	if err != nil || migrated || second.ID != entry.ID || second.Name != entry.Name {
		t.Fatalf("migration was not idempotent: %+v %v %v", second, migrated, err)
	}
	data, err = os.ReadFile(filepath.Join(directory, "simulate", "nested", "sample.txt"))
	if err != nil || string(data) != "库内修订" {
		t.Fatal("repeat migration overwrote library content", err)
	}
	if empty, migrated, err := library.ImportLegacy(t.TempDir(), "空项目"); err != nil || migrated || empty.ID != "" {
		t.Fatalf("empty project published a style: %+v %v %v", empty, migrated, err)
	}
}

func TestLegacyMigrationSupportsCorpusOrProfileOnlyAndRejectsInvalidProfile(t *testing.T) {
	library, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	corpusOnly, profileOnly, broken := t.TempDir(), t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(corpusOnly, "simulate", "a.md"), "语料")
	writeTestFile(t, filepath.Join(profileOnly, "meta", "simulation_profile.json"), testProfile())
	writeTestFile(t, filepath.Join(broken, "meta", "simulation_profile.json"), "{broken")
	entry, migrated, err := library.ImportLegacy(corpusOnly, "语料")
	if err != nil || !migrated || entry.SourceCount != 1 || entry.Ready {
		t.Fatalf("corpus-only migration: %+v %v %v", entry, migrated, err)
	}
	entry, migrated, err = library.ImportLegacy(profileOnly, "画像")
	if err != nil || !migrated || entry.SourceCount != 0 || !entry.Ready {
		t.Fatalf("profile-only migration: %+v %v %v", entry, migrated, err)
	}
	if _, migrated, err := library.ImportLegacy(broken, "损坏画像"); err == nil || migrated {
		t.Fatal("broken profile was published")
	}
	if entries, err := library.List(); err != nil || len(entries) != 2 {
		t.Fatalf("failed migration left published garbage: %+v %v", entries, err)
	}
}

func TestLibraryRejectsSymlinksInRootEntryCorpusAndMetadata(t *testing.T) {
	outside, parent := t.TempDir(), t.TempDir()
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Open(link); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("symbolic library root accepted", err)
	}
	library, err := Open(filepath.Join(parent, "safe"))
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := library.Create("safe")
	directory, _ := library.Directory(entry.ID)
	if err := os.Symlink(outside, filepath.Join(directory, "simulate", "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Get(entry.ID); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("symbolic corpus path accepted", err)
	}
	project := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, "simulate")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := library.ImportLegacy(project, "unsafe"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("symbolic legacy corpus accepted", err)
	}
	if err := os.Remove(filepath.Join(directory, "simulate", "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "entry.json")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(outside, "entry.json"), `{}`)
	if err := os.Symlink(filepath.Join(outside, "entry.json"), filepath.Join(directory, "entry.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Rename(entry.ID, "unsafe"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("symbolic entry metadata accepted", err)
	}
}

func TestCorruptExistingProfileRemainsVisibleAndRepairable(t *testing.T) {
	library, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	broken, err := library.Create("待修复")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := library.Create("完整风格")
	if err != nil {
		t.Fatal(err)
	}
	directory, _ := library.Directory(broken.ID)
	validDirectory, _ := library.Directory(valid.ID)
	writeTestFile(t, filepath.Join(directory, "meta", "simulation_profile.json"), "{incomplete")
	writeTestFile(t, filepath.Join(validDirectory, "meta", "simulation_profile.json"), testProfile())
	entry, err := library.Get(broken.ID)
	if err != nil || entry.Ready || entry.ProfileError == "" {
		t.Fatalf("broken profile not represented safely: %+v %v", entry, err)
	}
	if got, err := library.Directory(broken.ID); err != nil || got != directory {
		t.Fatalf("broken profile blocked repair directory: %s %v", got, err)
	}
	entries, err := library.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("one corrupt profile hid library entries: %+v %v", entries, err)
	}
	writeTestFile(t, filepath.Join(directory, "meta", "simulation_profile.json"), testProfile())
	if repaired, err := library.Get(broken.ID); err != nil || !repaired.Ready || repaired.ProfileError != "" {
		t.Fatalf("repair did not clear stale error: %+v %v", repaired, err)
	}
}

func TestLibraryConcurrentCreateRenameAndIdempotentMigration(t *testing.T) {
	library, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	writeTestFile(t, filepath.Join(project, "simulate", "one.txt"), "one")
	var wg sync.WaitGroup
	var migrations int
	var mu sync.Mutex
	for worker := range 12 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			entry, err := library.Create(fmt.Sprintf("style-%d", worker))
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := library.Rename(entry.ID, fmt.Sprintf("renamed-%d", worker)); err != nil {
				t.Error(err)
			}
			if _, migrated, err := library.ImportLegacy(project, "shared"); err != nil {
				t.Error(err)
			} else if migrated {
				mu.Lock()
				migrations++
				mu.Unlock()
			}
			if _, err := library.List(); err != nil {
				t.Error(err)
			}
		}(worker)
	}
	wg.Wait()
	if entries, err := library.List(); err != nil || len(entries) != 13 || migrations != 1 {
		t.Fatalf("concurrent writes lost or duplicated: %d migrations=%d error=%v", len(entries), migrations, err)
	}
}
