package projectversion

import (
	"context"
	"crypto/sha256"
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
)

const versionsDir = "meta/versions"

func excluded(rel string) bool {
	if rel == ".ainovel.lock" || strings.HasPrefix(rel, ".ainovel.lock/") || rel == "logs" || strings.HasPrefix(rel, "logs/") ||
		rel == "meta/runtime" || strings.HasPrefix(rel, "meta/runtime/") ||
		rel == versionsDir || strings.HasPrefix(rel, versionsDir+"/") {
		return true
	}
	for _, name := range strings.Split(rel, "/") {
		if strings.HasSuffix(name, ".tmp") || strings.Contains(name, ".tmp-") {
			return true
		}
	}
	return false
}

func openProject(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafeFile
	}
	return os.OpenRoot(dir)
}

func safeInfo(root *os.Root, rel string) (fs.FileInfo, error) {
	for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
		info, err := root.Lstat(parent)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s", ErrUnsafeFile, parent)
		}
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrUnsafeFile, rel)
	}
	return info, nil
}

func openRegular(root *os.Root, rel string) (*os.File, error) {
	info, err := safeInfo(root, rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrUnsafeFile, rel)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		f.Close()
		return nil, fmt.Errorf("%w: %s", ErrUnsafeFile, rel)
	}
	return f, nil
}

// collect also validates excluded trees. A link hidden under logs, runtime or
// versions must never become a route around the project's safety checks.
func collect(ctx context.Context, root *os.Root) ([]File, error) {
	files := make([]File, 0)
	err := fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
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
		if rel == ".ainovel.lock" && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", ErrUnsafeFile, rel)
		}
		if rel == "." || info.IsDir() || excluded(rel) {
			return nil
		}
		if !validPath(rel) {
			return fmt.Errorf("%w: %s", ErrUnsafeFile, rel)
		}
		if len(files) >= maxFiles {
			return errors.New("项目文件数量超过版本上限")
		}
		file, err := hashFile(ctx, root, rel)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	})
	return files, err
}

func hashFile(ctx context.Context, root *os.Root, rel string) (File, error) {
	f, err := openRegular(root, rel)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, f})
	if err != nil {
		return File{}, err
	}
	return File{Path: rel, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func fingerprint(files []File) string {
	h := sha256.New()
	for _, file := range files {
		fmt.Fprintf(h, "%s\x00%d\x00%s\n", file.Path, file.Size, file.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func copyFile(ctx context.Context, source *os.Root, src string, target *os.Root, dst string, expected File) error {
	in, err := openRegular(source, src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := target.MkdirAll(path.Dir(dst), 0700); err != nil {
		return err
	}
	out, err := target.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), contextReader{ctx, in})
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	if n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("%w: %s", ErrChanged, src)
	}
	return nil
}

func writeFile(root *os.Root, rel string, data []byte) error {
	if err := root.MkdirAll(path.Dir(rel), 0700); err != nil {
		return err
	}
	f, err := root.OpenFile(rel, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func validPath(rel string) bool {
	return fs.ValidPath(rel) && rel != "." && !strings.ContainsAny(rel, "\\:\x00") && !excluded(rel)
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}

func currentFingerprint(ctx context.Context, root *os.Root) (string, error) {
	files, err := collect(ctx, root)
	return fingerprint(files), err
}

// Host flushes usage at each idle snapshot and on close. Its updated_at field
// records the flush, not a user-visible state change. Preview tokens normalize
// only that field; snapshot integrity still hashes every original byte.
func previewFingerprint(ctx context.Context, root *os.Root, files []File) (string, error) {
	stable := slices.Clone(files)
	for i := range stable {
		if stable[i].Path != "meta/usage.json" || stable[i].Size > 8<<20 {
			continue
		}
		f, err := openRegular(root, stable[i].Path)
		if err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(contextReader{ctx, f}, 8<<20+1))
		f.Close()
		if err != nil {
			return "", err
		}
		var usage map[string]json.RawMessage
		if len(data) > 8<<20 || json.Unmarshal(data, &usage) != nil || usage == nil {
			continue
		}
		delete(usage, "updated_at")
		canonical, err := json.Marshal(usage)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(canonical)
		stable[i].Size = int64(len(canonical))
		stable[i].SHA256 = hex.EncodeToString(sum[:])
	}
	return fingerprint(stable), nil
}

func usageEqual(root *os.Root, before, after string) (bool, error) {
	var normalized [2][]byte
	for i, rel := range []string{before, after} {
		f, err := openRegular(root, rel)
		if err != nil {
			return false, err
		}
		data, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
		f.Close()
		if err != nil {
			return false, err
		}
		var object map[string]json.RawMessage
		if len(data) > 8<<20 || json.Unmarshal(data, &object) != nil || object == nil {
			return false, nil
		}
		delete(object, "updated_at")
		normalized[i], err = json.Marshal(object)
		if err != nil {
			return false, err
		}
	}
	return string(normalized[0]) == string(normalized[1]), nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// managedRoots reduces a restore to top-level directory renames, except meta,
// whose runtime directory, version history and project lease must stay in place.
func managedRoots(root *os.Root) ([]string, error) {
	var roots []string
	for _, dir := range []string{".", "meta"} {
		f, err := root.Open(dir)
		if errors.Is(err, fs.ErrNotExist) && dir == "meta" {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			rel := path.Join(dir, entry.Name())
			if rel == "meta" || excluded(rel) {
				continue
			}
			if _, err := safeInfo(root, rel); err != nil {
				return nil, err
			}
			roots = append(roots, rel)
		}
	}
	slices.Sort(roots)
	return roots, nil
}
