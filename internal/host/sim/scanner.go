// Modified by hansshaw for AI Novel Studio, 2026-10-24.
// Original third-party attribution and license terms are retained in NOTICE.

package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hansshaw/AInoval-web-ui/internal/domain"
	"github.com/hansshaw/AInoval-web-ui/internal/utils"
)

type scannedSource struct {
	domain.SimulationSource
	absPath string
	content string
}

func scanSources(root string) ([]scannedSource, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("source dir is required")
	}
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("simulate directory not found: %s", root)
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("simulate path is not a directory: %s", root)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("simulate directory must not be a symbolic link")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, err
	}

	var out []scannedSource
	err = filepath.WalkDir(rootAbs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != rootAbs {
			rel, err := filepath.Rel(rootAbs, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("unsafe simulation path")
			}
			if !utf8.ValidString(rel) || strings.IndexFunc(rel, unicode.IsControl) >= 0 {
				return fmt.Errorf("simulation path contains invalid characters")
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !isSupportedSource(path) {
			return nil
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		relToRoot, err := filepath.Rel(rootReal, real)
		if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
			return fmt.Errorf("simulation source escapes project directory")
		}
		data, err := os.ReadFile(real)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rootAbs, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		out = append(out, scannedSource{
			SimulationSource: domain.SimulationSource{
				RelativePath: rel,
				SHA256:       sha,
				Fingerprint:  domain.SimulationSourceFingerprint(rel, sha),
				SizeBytes:    info.Size(),
				ModTime:      info.ModTime().Format(time.RFC3339),
			},
			absPath: path,
			// 指纹算在原始字节上（文件身份，增量去重稳定）；content 解码后供
			// LLM 分析——GBK 语料直接当 UTF-8 读是乱码，画像会被静默喂垃圾。
			content: utils.DecodeText(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelativePath < out[j].RelativePath
	})
	return out, nil
}

func isSupportedSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".md", ".markdown":
		return true
	default:
		return false
	}
}
