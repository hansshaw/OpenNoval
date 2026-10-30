// Package stylelibrary owns the shared style corpus and its derived profiles.
// Each entry is a small independent simulation workspace; story files are never
// copied into this library.
package stylelibrary

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hansshaw/OpenNovel/internal/domain"
	"github.com/hansshaw/OpenNovel/internal/fileio"
)

var (
	ErrNotFound    = errors.New("style library entry not found")
	ErrInvalidID   = errors.New("invalid style library ID")
	ErrInvalidName = errors.New("style name must contain 1–80 visible characters")
	ErrUnsafePath  = errors.New("unsafe style library path")
)

type Entry struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SourceCount  int       `json:"sourceCount"`
	Ready        bool      `json:"ready"`
	ProfileError string    `json:"profileError,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Library struct {
	mu   sync.Mutex
	root string
}

var opened = struct {
	sync.Mutex
	libraries map[string]*Library
}{libraries: make(map[string]*Library)}

var validID = regexp.MustCompile(`^(?:[a-f0-9]{32}|legacy-[a-f0-9]{24})$`)

func Open(root string) (*Library, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrUnsafePath
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	opened.Lock()
	defer opened.Unlock()
	if err := ensureDirectory(filepath.Join(root, "entries")); err != nil {
		return nil, err
	}
	if library := opened.libraries[root]; library != nil {
		return library, nil
	}
	library := &Library{root: root}
	opened.libraries[root] = library
	return library, nil
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 80 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrInvalidName
	}
	return name, nil
}

func (l *Library) entryDirectory(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", ErrInvalidID
	}
	directory := filepath.Join(l.root, "entries", id)
	if err := checkPath(directory, true); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	return directory, nil
}

func (l *Library) Directory(id string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	directory, err := l.entryDirectory(id)
	if err == nil {
		_, err = l.getLocked(id)
	}
	return directory, err
}

func (l *Library) Get(id string) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.getLocked(id)
}

func (l *Library) getLocked(id string) (Entry, error) {
	directory, err := l.entryDirectory(id)
	if err != nil {
		return Entry{}, err
	}
	metadata := filepath.Join(directory, "entry.json")
	if err := checkPath(metadata, false); err != nil {
		return Entry{}, err
	}
	data, err := os.ReadFile(metadata)
	if err != nil {
		return Entry{}, err
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, fmt.Errorf("decode style entry: %w", err)
	}
	if entry.ID != id {
		return Entry{}, ErrUnsafePath
	}
	if _, err := normalizeName(entry.Name); err != nil {
		return Entry{}, err
	}
	sources, modified, err := sourceFiles(filepath.Join(directory, "simulate"))
	if err != nil {
		return Entry{}, err
	}
	entry.SourceCount, entry.Ready, entry.ProfileError = len(sources), false, ""
	if modified.After(entry.UpdatedAt) {
		entry.UpdatedAt = modified
	}
	profile := filepath.Join(directory, "meta", "simulation_profile.json")
	if err := checkPath(profile, false); err == nil {
		data, err := os.ReadFile(profile)
		if err != nil {
			return Entry{}, err
		}
		var decoded domain.SimulationProfile
		if json.Unmarshal(data, &decoded) != nil || domain.ValidateSimulationProfile(&decoded) != nil {
			entry.ProfileError = "风格画像损坏，请重新上传语料或蒸馏分析修复"
		} else {
			entry.Ready = true
		}
		if stat, err := os.Stat(profile); err == nil && stat.ModTime().After(entry.UpdatedAt) {
			entry.UpdatedAt = stat.ModTime().UTC()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Entry{}, err
	}
	return entry, nil
}

func (l *Library) List() ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := checkPath(filepath.Join(l.root, "entries"), true); err != nil {
		return nil, err
	}
	children, err := os.ReadDir(filepath.Join(l.root, "entries"))
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		if !validID.MatchString(child.Name()) {
			continue // Interrupted private imports are not published entries.
		}
		entry, err := l.getLocked(child.Name())
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(a, b int) bool {
		if entries[a].UpdatedAt.Equal(entries[b].UpdatedAt) {
			return entries[a].ID < entries[b].ID
		}
		return entries[a].UpdatedAt.After(entries[b].UpdatedAt)
	})
	return entries, nil
}

func (l *Library) Create(name string) (Entry, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Entry{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Entry{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.publishLocked(hex.EncodeToString(random[:]), name, nil)
}

func (l *Library) Rename(id, name string) (Entry, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Entry{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.getLocked(id)
	if err != nil {
		return Entry{}, err
	}
	entry.Name, entry.UpdatedAt = name, time.Now().UTC()
	directory, err := l.entryDirectory(id)
	if err != nil {
		return Entry{}, err
	}
	if err := writeEntry(filepath.Join(directory, "entry.json"), entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// ImportLegacy copies only supported corpus files and the derived profile. A
// deterministic ID makes reopening the same old project idempotent. Existing
// library content is never replaced, even if the legacy project later changes.
func (l *Library) ImportLegacy(projectDir, name string) (Entry, bool, error) {
	name, err := normalizeName(name)
	if err != nil {
		return Entry{}, false, err
	}
	projectDir, err = filepath.Abs(projectDir)
	if err != nil {
		return Entry{}, false, err
	}
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}
	digest := sha256.Sum256([]byte(projectDir))
	id := "legacy-" + hex.EncodeToString(digest[:12])
	l.mu.Lock()
	defer l.mu.Unlock()
	if entry, err := l.getLocked(id); err == nil {
		return entry, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Entry{}, false, err
	}
	sources, _, err := sourceFiles(filepath.Join(projectDir, "simulate"))
	if err != nil {
		return Entry{}, false, err
	}
	profile := filepath.Join(projectDir, "meta", "simulation_profile.json")
	hasProfile := false
	if err := checkPath(profile, false); err == nil {
		hasProfile = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, err
	}
	if len(sources) == 0 && !hasProfile {
		return Entry{}, false, nil
	}
	entry, err := l.publishLocked(id, name, func(destination string) error {
		for _, source := range sources {
			relative, err := filepath.Rel(filepath.Join(projectDir, "simulate"), source)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return ErrUnsafePath
			}
			if err := copyFile(source, filepath.Join(destination, "simulate", relative)); err != nil {
				return err
			}
		}
		if hasProfile {
			return copyFile(profile, filepath.Join(destination, "meta", "simulation_profile.json"))
		}
		return nil
	})
	return entry, err == nil, err
}

func (l *Library) publishLocked(id, name string, fill func(string) error) (Entry, error) {
	parent := filepath.Join(l.root, "entries")
	if err := checkPath(parent, true); err != nil {
		return Entry{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".style-import-*")
	if err != nil {
		return Entry{}, err
	}
	defer os.RemoveAll(temporary)
	if err := os.Mkdir(filepath.Join(temporary, "simulate"), 0o700); err != nil {
		return Entry{}, err
	}
	if fill != nil {
		if err := fill(temporary); err != nil {
			return Entry{}, err
		}
	}
	profile := filepath.Join(temporary, "meta", "simulation_profile.json")
	if err := checkPath(profile, false); err == nil {
		data, err := os.ReadFile(profile)
		if err != nil {
			return Entry{}, err
		}
		var decoded domain.SimulationProfile
		if json.Unmarshal(data, &decoded) != nil || domain.ValidateSimulationProfile(&decoded) != nil {
			return Entry{}, fmt.Errorf("invalid legacy simulation profile")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Entry{}, err
	}
	now := time.Now().UTC()
	entry := Entry{ID: id, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := writeEntry(filepath.Join(temporary, "entry.json"), entry); err != nil {
		return Entry{}, err
	}
	if err := os.Rename(temporary, filepath.Join(parent, id)); err != nil {
		return Entry{}, err
	}
	return l.getLocked(id)
}

func sourceFiles(root string) ([]string, time.Time, error) {
	if err := checkPath(root, true); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, time.Time{}, nil
		}
		return nil, time.Time{}, err
	}
	var files []string
	var modified time.Time
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
		if entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".txt", ".md", ".markdown":
		default:
			return nil
		}
		if err := checkPath(path, false); err != nil {
			return err
		}
		stat, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, path)
		if stat.ModTime().After(modified) {
			modified = stat.ModTime().UTC()
		}
		return nil
	})
	return files, modified, err
}

func ensureDirectory(path string) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := ensureDirectory(parent); err != nil {
			return err
		}
	}
	if err := checkPath(path, true); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Mkdir(path, 0o700)
}

func checkPath(path string, directory bool) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := checkPath(parent, true); err != nil {
			return err
		}
	}
	stat, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if stat.Mode()&os.ModeSymlink != 0 || (directory && !stat.IsDir()) || (!directory && !stat.Mode().IsRegular()) {
		return ErrUnsafePath
	}
	return nil
}

func copyFile(source, target string) error {
	if err := checkPath(source, false); err != nil {
		return err
	}
	if err := ensureDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeEntry(path string, entry Entry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".entry-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return fileio.Rename(temporary.Name(), path)
}
