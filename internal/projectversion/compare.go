package projectversion

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxChanges   = 500
	maxDiffBytes = 128 << 10
	maxFileDiff  = 16 << 10
	maxTextBytes = 256 << 10
)

// Compare verifies the saved payload before describing what restoring it will
// change. Its fingerprint is a required optimistic lock for Restore.
func Compare(ctx context.Context, projectDir, id string) (Comparison, error) {
	root, err := openProject(projectDir)
	if err != nil {
		return Comparison{}, err
	}
	defer root.Close()
	if err := requireRecovered(root); err != nil {
		return Comparison{}, err
	}
	m, err := verifiedSnapshot(ctx, root, id)
	if err != nil {
		return Comparison{}, err
	}
	current, err := collect(ctx, root)
	if err != nil {
		return Comparison{}, err
	}
	token, err := previewFingerprint(ctx, root, current)
	if err != nil {
		return Comparison{}, err
	}
	out := Comparison{Snapshot: m, Fingerprint: token, Changes: []Change{}}
	before := make(map[string]File, len(current))
	after := make(map[string]File, len(m.Files))
	names := make([]string, 0, len(current)+len(m.Files))
	for _, file := range current {
		before[file.Path] = file
		names = append(names, file.Path)
	}
	for _, file := range m.Files {
		after[file.Path] = file
		if _, ok := before[file.Path]; !ok {
			names = append(names, file.Path)
		}
	}
	slices.Sort(names)
	budget := maxDiffBytes
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return Comparison{}, err
		}
		old, oldOK := before[name]
		new, newOK := after[name]
		if oldOK && newOK && old.SHA256 == new.SHA256 && old.Size == new.Size {
			continue
		}
		if name == "meta/usage.json" && oldOK && newOK {
			equal, err := usageEqual(root, name, path.Join(versionsDir, id, "files", name))
			if err != nil {
				return Comparison{}, err
			}
			if equal {
				continue
			}
		}
		if len(out.Changes) >= maxChanges {
			out.Truncated = true
			break
		}
		change := Change{Path: name, Kind: "modified", OldSize: old.Size, NewSize: new.Size}
		if !oldOK {
			change.Kind = "added"
		} else if !newOK {
			change.Kind = "deleted"
		}
		if budget > 0 {
			oldText, oldIsText, err := readText(root, name, oldOK)
			if err != nil {
				return Comparison{}, err
			}
			newText, newIsText, err := readText(root, path.Join(versionsDir, id, "files", name), newOK)
			if err != nil {
				return Comparison{}, err
			}
			if oldIsText && newIsText {
				change.Diff, change.Truncated = textDiff(name, oldText, newText, min(maxFileDiff, budget))
				budget -= len(change.Diff)
			} else if old.Size > maxTextBytes || new.Size > maxTextBytes {
				change.Truncated = true
			}
		} else {
			change.Truncated = true
		}
		out.Truncated = out.Truncated || change.Truncated
		out.Changes = append(out.Changes, change)
	}
	return out, nil
}

func readText(root *os.Root, rel string, exists bool) (string, bool, error) {
	if !exists {
		return "", true, nil
	}
	f, err := openRegular(root, rel)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTextBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > maxTextBytes || !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
		return "", false, nil
	}
	return string(data), true, nil
}

// A bounded unified-style hunk avoids quadratic LCS work on long novels while
// retaining unchanged prefix/suffix context around the changed region.
func textDiff(name, before, after string, limit int) (string, bool) {
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	start := max(0, prefix-3)
	aEnd, bEnd := len(a)-max(0, suffix-3), len(b)-max(0, suffix-3)
	var out strings.Builder
	fmt.Fprintf(&out, "--- current/%s\n+++ snapshot/%s\n@@ -%d,%d +%d,%d @@\n", name, name, start+1, aEnd-start, start+1, bEnd-start)
	truncated := false
	writeLine := func(mark byte, line string) bool {
		if out.Len()+len(line)+2 > limit {
			truncated = true
			return false
		}
		out.WriteByte(mark)
		out.WriteString(line)
		out.WriteByte('\n')
		return true
	}
	for i := start; i < prefix; i++ {
		if !writeLine(' ', a[i]) {
			break
		}
	}
	if !truncated {
		for i := prefix; i < len(a)-suffix; i++ {
			if !writeLine('-', a[i]) {
				break
			}
		}
	}
	if !truncated {
		for i := prefix; i < len(b)-suffix; i++ {
			if !writeLine('+', b[i]) {
				break
			}
		}
	}
	if !truncated {
		for i := len(b) - suffix; i < bEnd; i++ {
			if !writeLine(' ', b[i]) {
				break
			}
		}
	}
	result := out.String()
	if len(result) > limit {
		return "", true
	}
	return result, truncated
}
