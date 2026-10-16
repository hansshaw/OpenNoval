package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// PlanningFingerprint excludes characters/world facts that evolve during writing.
// Any change to the approved premise or chapter plan requires a new approval.
func (s *Store) PlanningFingerprint() (string, error) {
	h := sha256.New()
	for _, rel := range []string{"premise.md", "outline.json", "layered_outline.json"} {
		b, err := os.ReadFile(filepath.Join(s.dir, rel))
		if os.IsNotExist(err) && rel == "layered_outline.json" {
			continue
		}
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
