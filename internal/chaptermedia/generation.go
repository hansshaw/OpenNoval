package chaptermedia

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/hansshaw/OpenNovel/internal/fileio"
)

const MaxGeneratedBytes = 100 << 20
const MaxGenerationJobs = 100

// Generation stores provenance independently from prose and illustration layout.
// Requests and credentials are deliberately not persisted in the project.
type Generation struct {
	ID           string `json:"id"`
	Chapter      int    `json:"chapter"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	SourceSHA256 string `json:"sourceSHA256"`
	Model        string `json:"model"`
	Status       string `json:"status"`
	Progress     int    `json:"progress"`
	Error        string `json:"error,omitempty"`
	CreatedAt    string `json:"createdAt"`
	MIME         string `json:"mime,omitempty"`
	Bytes        int    `json:"bytes,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	ImageID      string `json:"imageID,omitempty"`
}

func generationDir(chapter int) string { return path.Join(chapterDir(chapter), "generated") }
func generationExt(mime string) string {
	switch mime {
	case "audio/mpeg":
		return ".mp3"
	case "video/mp4":
		return ".mp4"
	}
	return ""
}
func validGeneration(g Generation) bool {
	return validID(g.ID) && g.Chapter > 0 && g.Chapter <= 1_000_000 && (g.Kind == "image" || g.Kind == "video" || g.Kind == "audio") && (g.Source == "final" || g.Source == "draft") && len(g.SourceSHA256) == 64 && len(g.Model) <= 800 && len(g.Error) <= 4096 && g.Progress >= 0 && g.Progress <= 100 && (g.Status == "running" || g.Status == "completed" || g.Status == "failed" || g.Status == "cancelled") && g.Bytes >= 0 && g.Bytes <= MaxGeneratedBytes && (g.MIME == "" || generationExt(g.MIME) != "" || (g.Kind == "image" && extension(g.MIME) != "")) && (g.ImageID == "" || validID(g.ImageID))
}
func ListGenerations(directory string, chapter int) ([]Generation, error) {
	root, err := openProject(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir := generationDir(chapter)
	if chapter < 1 || chapter > 1_000_000 {
		return nil, ErrInvalid
	}
	info, err := safeInfo(root, dir)
	if os.IsNotExist(err) {
		return []Generation{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrUnsafe
	}
	f, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	jobs := []Generation{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validID(id) {
			continue
		}
		data, err := readRegular(root, path.Join(dir, entry.Name()), 16<<10)
		if err != nil {
			return nil, err
		}
		var g Generation
		if json.Unmarshal(data, &g) != nil || !validGeneration(g) || g.ID != id || g.Chapter != chapter {
			return nil, ErrCorrupt
		}
		jobs = append(jobs, g)
		if len(jobs) > MaxGenerationJobs {
			return nil, ErrCorrupt
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt > jobs[j].CreatedAt })
	return jobs, nil
}
func SaveGeneration(directory string, g Generation, data []byte) error {
	if !validGeneration(g) {
		return ErrInvalid
	}
	root, err := openProject(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	dir := generationDir(g.Chapter)
	if err := ensureDir(root, dir); err != nil {
		return err
	}
	published := false
	if data != nil {
		if generationExt(g.MIME) == "" || len(data) == 0 || len(data) > MaxGeneratedBytes || len(data) != g.Bytes {
			return ErrInvalid
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != g.SHA256 {
			return ErrInvalid
		}
		artifact := path.Join(dir, g.ID+generationExt(g.MIME))
		if err := writeNew(root, artifact, data); err != nil {
			return err
		}
		defer func() {
			if !published {
				_ = root.Remove(artifact)
			}
		}()
	}
	rel := path.Join(dir, g.ID+".json")
	if info, err := safeInfo(root, rel); err == nil && !info.Mode().IsRegular() {
		return ErrUnsafe
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	encoded, err := json.Marshal(g)
	if err != nil {
		return err
	}
	tmp := rel + ".tmp"
	// Serialized by the project lease; a stale temp from an interrupted write is removable.
	if info, err := safeInfo(root, tmp); err == nil {
		if !info.Mode().IsRegular() {
			return ErrUnsafe
		}
		if err := root.Remove(tmp); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeNew(root, tmp, encoded); err != nil {
		return err
	}
	defer root.Remove(tmp)
	if err := fileio.RootRename(root, tmp, rel); err != nil {
		return err
	}
	published = true
	return nil
}
func ReadGeneration(directory string, chapter int, id string) (Generation, []byte, error) {
	if !validID(id) {
		return Generation{}, nil, ErrInvalid
	}
	jobs, err := ListGenerations(directory, chapter)
	if err != nil {
		return Generation{}, nil, err
	}
	for _, g := range jobs {
		if g.ID != id {
			continue
		}
		if g.Status != "completed" {
			return g, nil, ErrNotFound
		}
		if g.Kind == "image" {
			_, data, err := Read(directory, chapter, g.ImageID)
			return g, data, err
		}
		root, err := openProject(directory)
		if err != nil {
			return g, nil, err
		}
		defer root.Close()
		data, err := readRegular(root, path.Join(generationDir(chapter), g.ID+generationExt(g.MIME)), MaxGeneratedBytes)
		if err != nil {
			return g, nil, err
		}
		hash := sha256.Sum256(data)
		if len(data) != g.Bytes || hex.EncodeToString(hash[:]) != g.SHA256 {
			return g, nil, ErrCorrupt
		}
		return g, data, nil
	}
	return Generation{}, nil, ErrNotFound
}
func RemoveGeneration(directory string, chapter int, id string) error {
	if !validID(id) {
		return ErrInvalid
	}
	jobs, err := ListGenerations(directory, chapter)
	if err != nil {
		return err
	}
	for _, g := range jobs {
		if g.ID != id {
			continue
		}
		root, err := openProject(directory)
		if err != nil {
			return err
		}
		defer root.Close()
		if err := root.Remove(path.Join(generationDir(chapter), id+".json")); err != nil {
			return err
		}
		if ext := generationExt(g.MIME); ext != "" {
			_ = root.Remove(path.Join(generationDir(chapter), id+ext))
		}
		// Imported/generated images share the chapter layout and have their own delete action.
		return nil
	}
	return fmt.Errorf("%w：生成记录", ErrNotFound)
}
