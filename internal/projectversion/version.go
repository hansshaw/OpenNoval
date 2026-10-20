package projectversion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const maxManifestBytes = 32 << 20
const maxFiles = 100000

// Create publishes a verified snapshot by a single directory rename. Any
// failure leaves no partial version in List. Files are copied, never linked.
func Create(ctx context.Context, projectDir, name string) (Manifest, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 80 || strings.ContainsAny(name, "\x00\r\n") {
		return Manifest{}, errors.New("版本名称不能超过 80 字或包含换行")
	}
	root, err := openProject(projectDir)
	if err != nil {
		return Manifest{}, err
	}
	defer root.Close()
	if err := requireRecovered(root); err != nil {
		return Manifest{}, err
	}
	return create(ctx, root, name)
}

func create(ctx context.Context, root *os.Root, name string) (Manifest, error) {
	files, err := collect(ctx, root)
	if err != nil {
		return Manifest{}, err
	}
	id, err := newID()
	if err != nil {
		return Manifest{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if name == "" {
		name = "手动快照 " + time.Now().Format("2006-01-02 15:04:05")
	}
	m := Manifest{Version: 1, ID: id, Name: name, CreatedAt: now, Fingerprint: fingerprint(files), Files: files}
	stage := path.Join(versionsDir, ".creating-"+id)
	if err := root.MkdirAll(path.Join(stage, "files"), 0700); err != nil {
		return Manifest{}, err
	}
	defer root.RemoveAll(stage)
	for _, file := range files {
		if err := copyFile(ctx, root, file.Path, root, path.Join(stage, "files", file.Path), file); err != nil {
			return Manifest{}, err
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if len(data) > maxManifestBytes {
		return Manifest{}, errors.New("项目文件清单过大")
	}
	if err := writeFile(root, path.Join(stage, "manifest.json"), data); err != nil {
		return Manifest{}, err
	}
	// Detect modifications or new/deleted files during copying before publishing.
	current, err := currentFingerprint(ctx, root)
	if err != nil {
		return Manifest{}, err
	}
	if current != m.Fingerprint {
		return Manifest{}, ErrChanged
	}
	if err := syncTree(root, stage); err != nil {
		return Manifest{}, err
	}
	if err := durableRename(root, stage, path.Join(versionsDir, id)); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// List reads manifests newest first. Snapshot payloads are fully verified by
// Compare and Restore; listing does not hash every historical novel again.
func List(projectDir string) ([]Manifest, error) {
	root, err := openProject(projectDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := safeInfo(root, versionsDir); errors.Is(err, fs.ErrNotExist) {
		return []Manifest{}, nil
	} else if err != nil {
		return nil, err
	}
	dir, err := root.Open(versionsDir)
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}
	versions := make([]Manifest, 0, len(entries))
	for _, entry := range entries {
		if _, err := safeInfo(root, path.Join(versionsDir, entry.Name())); err != nil {
			return nil, err
		}
		if !validID(entry.Name()) {
			continue
		}
		m, err := readManifest(root, entry.Name())
		if err != nil {
			return nil, err
		}
		versions = append(versions, m)
	}
	slices.SortFunc(versions, func(a, b Manifest) int {
		if a.CreatedAt == b.CreatedAt {
			return strings.Compare(b.ID, a.ID)
		}
		return strings.Compare(b.CreatedAt, a.CreatedAt)
	})
	return versions, nil
}

func newID() (string, error) {
	var id [16]byte
	_, err := rand.Read(id[:])
	return hex.EncodeToString(id[:]), err
}

func readManifest(root *os.Root, id string) (Manifest, error) {
	if !validID(id) {
		return Manifest{}, ErrInvalidID
	}
	dir := path.Join(versionsDir, id)
	if _, err := safeInfo(root, dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, err
	}
	f, err := openRegular(root, path.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil || len(data) > maxManifestBytes {
		return Manifest{}, ErrCorrupt
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if m.Version != 1 || m.ID != id || len(m.Files) > maxFiles || utf8.RuneCountInString(m.Name) > 80 {
		return Manifest{}, ErrCorrupt
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		return Manifest{}, ErrCorrupt
	}
	previous := ""
	for _, file := range m.Files {
		hash, err := hex.DecodeString(file.SHA256)
		if !validPath(file.Path) || file.Path <= previous || file.Size < 0 || err != nil || len(hash) != 32 || file.SHA256 != strings.ToLower(file.SHA256) {
			return Manifest{}, ErrCorrupt
		}
		previous = file.Path
	}
	if fingerprint(m.Files) != m.Fingerprint {
		return Manifest{}, ErrCorrupt
	}
	return m, nil
}

func verifiedSnapshot(ctx context.Context, root *os.Root, id string) (Manifest, error) {
	m, err := readManifest(root, id)
	if err != nil {
		return Manifest{}, err
	}
	base := path.Join(versionsDir, id, "files")
	if _, err := safeInfo(root, base); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	expected := make(map[string]File, len(m.Files))
	for _, file := range m.Files {
		expected[file.Path] = file
	}
	count := 0
	err = fs.WalkDir(root.FS(), base, func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := safeInfo(root, rel)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := strings.TrimPrefix(rel, base+"/")
		wanted, ok := expected[name]
		if !ok {
			return ErrCorrupt
		}
		got, err := hashFile(ctx, root, rel)
		if err != nil {
			return err
		}
		if got.Size != wanted.Size || got.SHA256 != wanted.SHA256 {
			return ErrCorrupt
		}
		count++
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnsafeFile) {
			return Manifest{}, err
		}
		return Manifest{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if count != len(m.Files) {
		return Manifest{}, ErrCorrupt
	}
	return m, nil
}
