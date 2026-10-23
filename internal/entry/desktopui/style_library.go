package desktopui

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/store"
	"github.com/hansshaw/AInoval-web-ui/internal/stylelibrary"
)

// The shared library is beside the projects catalogue, outside every novel.
// Resolve lazily so desktop/test workspace setup cannot bind it to a stale root.
func (s *Server) styleLibraryRootLocked() string {
	return filepath.Join(filepath.Dir(s.booksDir), "style-library")
}

func (s *Server) sharedStyleLibraryLocked() (*stylelibrary.Library, error) {
	return stylelibrary.Open(s.styleLibraryRootLocked())
}

func (s *Server) migrateLegacyStyleLibrariesLocked(library *stylelibrary.Library) []string {
	var warnings []string
	for _, directory := range s.projectDirectoriesLocked() {
		project, err := readProject(directory)
		if err != nil {
			continue
		}
		name := project.Name
		if name == "" {
			name = filepath.Base(directory)
		}
		if _, _, err = library.ImportLegacy(directory, legacyStyleName(name)); err != nil {
			warning := "历史语料保留在原目录，迁移暂未完成：" + name + "：" + s.redactLocked(err.Error())
			warnings = append(warnings, warning)
			slog.Warn(warning, "module", "style_library")
		}
	}
	return warnings
}

func legacyStyleName(name string) string {
	const suffix = " · 历史风格"
	runes := []rune(strings.TrimSpace(name))
	if len(runes) > 80-len([]rune(suffix)) {
		runes = runes[:80-len([]rune(suffix))]
	}
	return string(runes) + suffix
}

func selectedStyleIDs(library *stylelibrary.Library, directory string, project projectInfo) ([]string, error) {
	if project.StyleReferences != nil {
		return append([]string{}, project.StyleReferences...), nil
	}
	name := project.Name
	if name == "" {
		name = filepath.Base(directory)
	}
	entry, _, err := library.ImportLegacy(directory, legacyStyleName(name))
	if err != nil {
		return nil, err
	}
	// Retain only the legacy profile that was already used by this novel. New
	// library entries are opt-in. Explicit [] permanently disables this fallback.
	if entry.ID != "" && entry.Ready {
		return []string{entry.ID}, nil
	}
	return []string{}, nil
}

// A Host captures its project directory, never the currently selected view.
// Context reads use the saved selection afresh, so changing the checkboxes only
// affects subsequent requests and cannot rewrite completed chapters.
func (s *Server) styleReferenceProvider(directory string) func() ([]domain.NamedStyleReference, error) {
	return func() ([]domain.NamedStyleReference, error) {
		s.mu.Lock()
		root := s.styleLibraryRootLocked()
		s.mu.Unlock()
		library, err := stylelibrary.Open(root)
		if err != nil {
			return nil, err
		}
		project, err := readProject(directory)
		if err != nil {
			return nil, err
		}
		ids, err := selectedStyleIDs(library, directory, project)
		if err != nil {
			return nil, err
		}
		var refs []domain.NamedStyleReference
		var warnings []error
		for _, id := range ids {
			entry, err := library.Get(id)
			if err != nil {
				warnings = append(warnings, fmt.Errorf("风格引用 %s 不可用: %w", id, err))
				continue
			}
			target, err := library.Directory(id)
			if err != nil {
				warnings = append(warnings, err)
				continue
			}
			profile, err := store.NewStore(target).Simulation.Load()
			if err != nil || profile == nil {
				warnings = append(warnings, fmt.Errorf("风格引用 %s 尚未完成分析或画像已失效", entry.Name))
				continue
			}
			refs = append(refs, domain.NamedStyleReference{ID: id, Name: entry.Name, Profile: domain.CompactSimulationProfile(profile)})
		}
		return refs, errors.Join(warnings...)
	}
}

func (s *Server) styleReferences(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory   string   `json:"directory"`
		SelectedIDs []string `json:"selectedIds"`
	}
	if r.Method == http.MethodPost {
		if !readJSON(w, r, &req) {
			return
		}
		if req.SelectedIDs == nil {
			fail(w, http.StatusBadRequest, "请提供勾选条目，全部取消时提交空数组")
			return
		}
	} else {
		req.Directory = r.URL.Query().Get("directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Directory == "" || req.Directory != s.dir {
		fail(w, http.StatusConflict, "当前项目已切换，请刷新后重试")
		return
	}
	if s.closed {
		fail(w, http.StatusConflict, "工作台正在关闭")
		return
	}
	library, err := s.sharedStyleLibraryLocked()
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	warnings := s.migrateLegacyStyleLibrariesLocked(library)
	if r.Method == http.MethodPost {
		if len(req.SelectedIDs) > 20 {
			fail(w, http.StatusBadRequest, "最多引用 20 个风格条目")
			return
		}
		seen := map[string]bool{}
		selected := make([]string, 0, len(req.SelectedIDs))
		for _, id := range req.SelectedIDs {
			if strings.TrimSpace(id) != id || seen[id] {
				fail(w, http.StatusBadRequest, "风格引用 ID 无效或重复")
				return
			}
			entry, err := library.Get(id)
			if err != nil {
				s.styleLibraryErrorLocked(w, err)
				return
			}
			if !entry.Ready {
				fail(w, http.StatusConflict, "请先完成所选风格的蒸馏分析："+entry.Name)
				return
			}
			seen[id] = true
			selected = append(selected, id)
		}
		save := func() error {
			project, err := readProject(s.dir)
			if err != nil {
				return err
			}
			if project.Archived {
				return fmt.Errorf("归档项目不能修改引用")
			}
			project.StyleReferences = selected
			return saveProject(s.dir, project)
		}
		if s.rt != nil {
			err = save()
		} else {
			err = withWorkspaceLease(s.dir, save)
		}
		if err != nil {
			fail(w, http.StatusConflict, "引用保存失败："+s.redactLocked(err.Error()))
			return
		}
		s.broadcastLocked("style-references-updated", map[string]any{"directory": s.dir, "selectedIds": selected})
	}
	project, err := readProject(s.dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, "项目引用读取失败")
		return
	}
	ids, err := selectedStyleIDs(library, s.dir, project)
	selectionNeedsConfirmation := err != nil
	if err != nil {
		// A damaged legacy profile must not block the UI that lets the user
		// explicitly replace or clear its implicit reference. Keep the original
		// files intact and expose the migration failure alongside usable entries.
		ids = []string{}
		warnings = append(warnings, "原项目风格引用暂不可用，已保留原文件，可重新选择共享条目："+s.redactLocked(err.Error()))
	}
	entries, err := library.List()
	if err != nil {
		s.styleLibraryErrorLocked(w, err)
		return
	}
	if entries == nil {
		entries = []stylelibrary.Entry{}
	}
	reply(w, http.StatusOK, map[string]any{"directory": s.dir, "selectedIds": ids, "entries": entries, "warnings": warnings, "selectionNeedsConfirmation": selectionNeedsConfirmation})
}
