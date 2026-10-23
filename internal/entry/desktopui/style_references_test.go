package desktopui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
	"github.com/hansshaw/AInoval-web-ui/internal/stylelibrary"
)

func referenceTestLibrary(t *testing.T, s *Server) *stylelibrary.Library {
	t.Helper()
	s.mu.Lock()
	library, err := s.sharedStyleLibraryLocked()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return library
}

func referenceTestEntry(t *testing.T, library *stylelibrary.Library, name, voice string) stylelibrary.Entry {
	t.Helper()
	entry, err := library.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := library.Directory(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewStore(directory)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.Save(domain.SimulationProfile{Version: domain.SimulationProfileVersion, Synthesis: domain.SimulationSynthesis{Style: domain.SimulationStyle{NarrativeVoice: []string{voice}}}, SourceReports: []domain.SimulationSourceReport{{Summary: "SOURCE REPORT MUST NOT BECOME PROJECT FACT"}}}); err != nil {
		t.Fatal(err)
	}
	entry, err = library.Get(entry.ID)
	if err != nil || !entry.Ready {
		t.Fatalf("entry not ready: %+v %v", entry, err)
	}
	return entry
}

func referenceTestSelection(t *testing.T, s *Server, directory string, ids []string, status int) []string {
	t.Helper()
	var response struct {
		SelectedIDs []string `json:"selectedIds"`
	}
	result := request(s, http.MethodPost, "/api/style/references", mustJSON(t, map[string]any{"directory": directory, "selectedIds": ids}))
	if result.Code != status {
		t.Fatalf("save references = %d want %d: %s", result.Code, status, result.Body.String())
	}
	if status == http.StatusOK {
		if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
	}
	return response.SelectedIDs
}

func TestSharedStyleSelectionPersistsPerProjectAndLiveProvidersRemainIsolated(t *testing.T) {
	s := newTestServer(t, false)
	first := s.dir
	if err := saveProject(first, projectInfo{Name: "first"}); err != nil {
		t.Fatal(err)
	}
	library := referenceTestLibrary(t, s)
	one := referenceTestEntry(t, library, "shared one", "voice one")
	two := referenceTestEntry(t, library, "shared two", "voice two")
	firstProvider := s.styleReferenceProvider(first)
	if refs, err := firstProvider(); err != nil || len(refs) != 0 {
		t.Fatalf("unselected global entries were automatically applied: %+v %v", refs, err)
	}
	if ids := referenceTestSelection(t, s, first, []string{one.ID}, http.StatusOK); len(ids) != 1 || ids[0] != one.ID {
		t.Fatalf("wrong saved selection: %v", ids)
	}
	second := filepath.Join(s.booksDir, "second")
	if err := saveProject(second, projectInfo{Name: "second"}); err != nil {
		t.Fatal(err)
	}
	if result := request(s, http.MethodPost, "/api/workspaces", mustJSON(t, map[string]string{"path": second})); result.Code != http.StatusOK {
		t.Fatalf("switch project: %d %s", result.Code, result.Body.String())
	}
	secondProvider := s.styleReferenceProvider(second)
	if refs, err := secondProvider(); err != nil || len(refs) != 0 {
		t.Fatalf("first project's selection leaked into second project: %+v %v", refs, err)
	}
	referenceTestSelection(t, s, second, []string{two.ID}, http.StatusOK)
	for name, test := range map[string]struct {
		provider func() ([]domain.NamedStyleReference, error)
		want     string
	}{"first": {firstProvider, one.ID}, "second": {secondProvider, two.ID}} {
		refs, err := test.provider()
		if err != nil || len(refs) != 1 || refs[0].ID != test.want {
			t.Fatalf("%s provider followed the selected workbench instead of its own project: %+v %v", name, refs, err)
		}
	}
	referenceTestSelection(t, s, first, []string{}, http.StatusConflict)
	project, err := readProject(first)
	if err != nil || len(project.StyleReferences) != 1 || project.StyleReferences[0] != one.ID {
		t.Fatalf("stale workbench request changed another project: %+v %v", project, err)
	}
	if ids := referenceTestSelection(t, s, second, []string{}, http.StatusOK); ids == nil || len(ids) != 0 {
		t.Fatalf("clear selection did not return an explicit empty array: %#v", ids)
	}
	project, err = readProject(second)
	if err != nil || project.StyleReferences == nil || len(project.StyleReferences) != 0 {
		t.Fatalf("cleared selection was not persisted distinctly from legacy default: %+v %v", project, err)
	}
	if refs, err := secondProvider(); err != nil || len(refs) != 0 {
		t.Fatalf("live provider ignored selection removal: %+v %v", refs, err)
	}
}

func TestSharedStyleInvalidationKeepsSelectedIDsAndOnlyLoadsReadyProfiles(t *testing.T) {
	s := newTestServer(t, false)
	if err := saveProject(s.dir, projectInfo{Name: "project"}); err != nil {
		t.Fatal(err)
	}
	library := referenceTestLibrary(t, s)
	one := referenceTestEntry(t, library, "one", "voice one")
	two := referenceTestEntry(t, library, "two", "voice two")
	referenceTestSelection(t, s, s.dir, []string{one.ID, two.ID}, http.StatusOK)
	provider := s.styleReferenceProvider(s.dir)
	result := request(s, http.MethodPost, "/api/style/upload", mustJSON(t, map[string]string{"entryId": one.ID, "name": "new.txt", "content": "new corpus invalidates the previously analyzed profile"}))
	if result.Code != http.StatusOK {
		t.Fatalf("upload to shared entry: %d %s", result.Code, result.Body.String())
	}
	refs, err := provider()
	if err == nil || len(refs) != 1 || refs[0].ID != two.ID {
		t.Fatalf("invalidated reference was still used or ready reference discarded: %+v %v", refs, err)
	}
	project, readErr := readProject(s.dir)
	if readErr != nil || len(project.StyleReferences) != 2 || project.StyleReferences[0] != one.ID {
		t.Fatalf("invalidation silently removed selected IDs: %+v %v", project, readErr)
	}
	referenceTestSelection(t, s, s.dir, []string{one.ID}, http.StatusConflict)
	for _, invalid := range [][]string{{"../../outside"}, {two.ID, two.ID}} {
		referenceTestSelection(t, s, s.dir, invalid, http.StatusBadRequest)
	}
	referenceTestSelection(t, s, s.dir, []string{}, http.StatusOK)
	if refs, err := provider(); err != nil || len(refs) != 0 {
		t.Fatalf("clearing invalidated references still applied a profile: %+v %v", refs, err)
	}
}

func TestCorruptLegacyStyleDoesNotBlockReplacingOrClearingReferences(t *testing.T) {
	for _, name := range []string{"replace", "clear"} {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t, false)
			if err := saveProject(s.dir, projectInfo{Name: "旧项目"}); err != nil {
				t.Fatal(err)
			}
			profilePath := filepath.Join(s.dir, "meta", "simulation_profile.json")
			broken := []byte("{interrupted legacy profile")
			if err := os.WriteFile(profilePath, broken, 0o600); err != nil {
				t.Fatal(err)
			}
			projectPath := filepath.Join(s.dir, "meta", "web-project.json")
			projectBefore, err := os.ReadFile(projectPath)
			if err != nil {
				t.Fatal(err)
			}
			entry := referenceTestEntry(t, referenceTestLibrary(t, s), "可用共享风格", "shared voice")
			result := request(s, http.MethodGet, "/api/style/references?directory="+url.QueryEscape(s.dir), "")
			var view struct {
				SelectedIDs                []string             `json:"selectedIds"`
				Entries                    []stylelibrary.Entry `json:"entries"`
				Warnings                   []string             `json:"warnings"`
				SelectionNeedsConfirmation bool                 `json:"selectionNeedsConfirmation"`
			}
			if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &view) != nil || view.SelectedIDs == nil || len(view.SelectedIDs) != 0 || len(view.Warnings) == 0 || len(view.Entries) != 1 || view.Entries[0].ID != entry.ID || !view.SelectionNeedsConfirmation {
				t.Fatalf("damaged legacy profile blocked usable reference choices: %d %s", result.Code, result.Body.String())
			}
			projectAfter, err := os.ReadFile(projectPath)
			if err != nil || !bytes.Equal(projectAfter, projectBefore) {
				t.Fatalf("GET changed old project metadata: %v", err)
			}
			selected := []string{}
			if name == "replace" {
				selected = append(selected, entry.ID)
			}
			referenceTestSelection(t, s, s.dir, selected, http.StatusOK)
			result = request(s, http.MethodGet, "/api/style/references?directory="+url.QueryEscape(s.dir), "")
			if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &view) != nil || view.SelectionNeedsConfirmation {
				t.Fatalf("explicit recovery selection still required confirmation: %d %s", result.Code, result.Body.String())
			}
			project, err := readProject(s.dir)
			if err != nil || project.StyleReferences == nil || len(project.StyleReferences) != len(selected) {
				t.Fatalf("explicit recovery selection was not persisted: %+v %v", project, err)
			}
			refs, err := s.styleReferenceProvider(s.dir)()
			if err != nil || len(refs) != len(selected) || (len(refs) == 1 && refs[0].ID != entry.ID) {
				t.Fatalf("saved recovery selection still used damaged legacy profile: %+v %v", refs, err)
			}
			after, err := os.ReadFile(profilePath)
			if err != nil || !bytes.Equal(after, broken) {
				t.Fatalf("recovery removed or rewrote original legacy profile: %v", err)
			}
		})
	}
}

func TestLegacyStyleMigrationIsReadOnlyAndSharedEntriesSurviveProjectDeletion(t *testing.T) {
	s := newTestServer(t, false)
	if err := saveProject(s.homeDir, projectInfo{Name: "home"}); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(s.booksDir, "legacy")
	if err := saveProject(legacy, projectInfo{Name: strings.Repeat("长", 80)}); err != nil {
		t.Fatal(err)
	}
	st := store.NewStore(legacy)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Simulation.Save(domain.SimulationProfile{Version: domain.SimulationProfileVersion, Synthesis: domain.SimulationSynthesis{Style: domain.SimulationStyle{NarrativeVoice: []string{"legacy voice"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(legacy, "simulate"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(legacy, "simulate", "old.txt")
	if err := os.WriteFile(source, []byte("original legacy source"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte)
	for _, file := range []string{filepath.Join(legacy, "meta", "web-project.json"), filepath.Join(legacy, "meta", "simulation_profile.json"), source} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		before[file] = data
	}
	library := referenceTestLibrary(t, s)
	unrelated := referenceTestEntry(t, library, "unrelated shared entry", "not selected")
	if result := request(s, http.MethodPost, "/api/workspaces", mustJSON(t, map[string]string{"path": legacy})); result.Code != http.StatusOK {
		t.Fatalf("open legacy project: %d %s", result.Code, result.Body.String())
	}
	result := request(s, http.MethodGet, "/api/style/references?directory="+url.QueryEscape(legacy), "")
	if result.Code != http.StatusOK {
		t.Fatalf("long legacy project name prevented reference migration: %d %s", result.Code, result.Body.String())
	}
	var response struct {
		SelectedIDs []string `json:"selectedIds"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil || len(response.SelectedIDs) != 1 || !strings.HasPrefix(response.SelectedIDs[0], "legacy-") || response.SelectedIDs[0] == unrelated.ID {
		t.Fatalf("legacy fallback selected unrelated shared entries: %s %v", result.Body.String(), err)
	}
	legacyID := response.SelectedIDs[0]
	for file, data := range before {
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(after, data) {
			t.Fatalf("GET migration changed old project data: %s %v", file, err)
		}
	}
	provider := s.styleReferenceProvider(legacy)
	if refs, err := provider(); err != nil || len(refs) != 1 || refs[0].ID != legacyID {
		t.Fatalf("legacy project lost previously active profile: %+v %v", refs, err)
	}
	referenceTestSelection(t, s, legacy, []string{}, http.StatusOK)
	if refs, err := provider(); err != nil || len(refs) != 0 {
		t.Fatalf("explicit empty selection re-enabled legacy profile: %+v %v", refs, err)
	}
	if result := request(s, http.MethodPost, "/api/workspaces", mustJSON(t, map[string]string{"path": s.homeDir})); result.Code != http.StatusOK {
		t.Fatalf("return home: %d %s", result.Code, result.Body.String())
	}
	if result := request(s, http.MethodPost, "/api/workspaces/archive", mustJSON(t, map[string]any{"directory": legacy, "archived": true})); result.Code != http.StatusOK {
		t.Fatalf("archive old project: %d %s", result.Code, result.Body.String())
	}
	if result := request(s, http.MethodPost, "/api/workspaces/delete", mustJSON(t, map[string]any{"directory": legacy, "confirmed": true})); result.Code != http.StatusOK {
		t.Fatalf("delete old project: %d %s", result.Code, result.Body.String())
	}
	if entry, err := library.Get(legacyID); err != nil || !entry.Ready {
		t.Fatalf("deleting old novel removed its migrated shared entry: %+v %v", entry, err)
	}
	if entry, err := library.Get(unrelated.ID); err != nil || !entry.Ready {
		t.Fatalf("project deletion affected unrelated global entries: %+v %v", entry, err)
	}
}
