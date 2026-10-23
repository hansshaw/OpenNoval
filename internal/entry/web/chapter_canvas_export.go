package web

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/chaptermedia"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

const maxChapterCanvasImageBytes = 256 << 20

var errChapterCanvasTooLarge = errors.New("本章配图总量超过 256 MiB，请减少配图后再导出画布")

type chapterCanvasNode struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Position map[string]int `json:"position"`
	Width    float64        `json:"width"`
	Height   float64        `json:"height"`
	Metadata map[string]any `json:"metadata"`
}

type chapterCanvasFile struct {
	StorageKey string `json:"storageKey"`
	Path       string `json:"path"`
	MIMEType   string `json:"mimeType"`
	Bytes      int    `json:"bytes"`
}

// chapterCanvasExport snapshots all input before sending any ZIP bytes. This
// avoids sending a partial archive or mixing an old paragraph with a new image.
func (s *Server) chapterCanvasExport(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || chapter < 1 || chapter > 1_000_000 {
		fail(w, http.StatusBadRequest, "无效章节")
		return
	}
	s.mu.Lock()
	if !s.versionDirectoryLocked(w, r.URL.Query().Get("directory")) {
		s.mu.Unlock()
		return
	}
	if s.projectRunningLocked(s.dir) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先暂停并等待本项目操作结束，再导出当前章节画布")
		return
	}
	var archive *os.File
	err = s.withVersionSnapshotLocked(func() error {
		view, readErr := readChapterEdit(store.NewStore(s.dir), chapter)
		if readErr != nil {
			return readErr
		}
		archive, readErr = createChapterCanvasArchive(r.Context(), s.dir, view)
		return readErr
	})
	if err != nil {
		switch {
		case errors.Is(err, errChapterCanvasTooLarge):
			fail(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, chaptermedia.ErrUnsafe), errors.Is(err, chaptermedia.ErrCorrupt), errors.Is(err, chaptermedia.ErrNotFound):
			s.chapterMediaErrorLocked(w, err)
		default:
			s.chapterEditErrorLocked(w, err)
		}
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	defer func() {
		_ = archive.Close()
		_ = os.Remove(archive.Name())
	}()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="chapter-%06d-canvas.zip"`, chapter))
	http.ServeContent(w, r, fmt.Sprintf("chapter-%06d-canvas.zip", chapter), time.Time{}, archive)
}

func createChapterCanvasArchive(ctx context.Context, directory string, view chapterEditView) (_ *os.File, err error) {
	items, err := chaptermedia.List(directory, view.Chapter)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, item := range items {
		total += int64(item.Bytes)
		if total > maxChapterCanvasImageBytes {
			return nil, errChapterCanvasTooLarge
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	id := "chapter-" + hex.EncodeToString(random[:])
	now := time.Now().UTC().Format(time.RFC3339Nano)
	title := fmt.Sprintf("第 %d 章 · %s", view.Chapter, view.Title)
	nodes := []chapterCanvasNode{}
	if view.HasContent {
		nodes = append(nodes, chapterCanvasTextNode(id+"-text", "当前正式正文 · "+view.Title, view.Content, 40))
	}
	if view.HasPlan {
		plan := fmt.Sprintf("卷名：%s\n章名：%s\n核心事件：%s\n悬念：%s", view.VolumeTitle, view.Title, view.CoreEvent, view.Hook)
		if len(view.Scenes) > 0 {
			plan += "\n场景：\n- " + strings.Join(view.Scenes, "\n- ")
		}
		y := 40
		if view.HasContent {
			y = 740
		}
		nodes = append(nodes, chapterCanvasTextNode(id+"-plan", "当前保存的章节规划", plan, y))
	}
	archive, err := os.CreateTemp("", "ainovel-chapter-canvas-*.zip")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = archive.Close()
			_ = os.Remove(archive.Name())
		}
	}()
	writer := zip.NewWriter(archive)
	defer writer.Close()
	files := make([]chapterCanvasFile, 0, len(items))
	for index, listed := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, data, err := chaptermedia.Read(directory, view.Chapter, listed.ID)
		if err != nil {
			return nil, err
		}
		if item != listed {
			return nil, chaptermedia.ErrCorrupt
		}
		nodeID := id + "-" + item.ID
		storageKey := "image:" + nodeID
		ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}[item.MIME]
		filePath := "projects/" + id + "/files/" + nodeID + ext
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: filePath, Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(data); err != nil {
			return nil, err
		}
		files = append(files, chapterCanvasFile{StorageKey: storageKey, Path: filePath, MIMEType: item.MIME, Bytes: len(data)})
		scale := 480 / math.Max(float64(item.Width), float64(item.Height))
		label := item.Caption
		if strings.TrimSpace(label) == "" {
			label = item.Name
		}
		nodes = append(nodes, chapterCanvasNode{
			ID: nodeID, Type: "image", Title: label,
			Position: map[string]int{"x": 620 + index%3*540, "y": 40 + index/3*540},
			Width:    float64(item.Width) * scale, Height: float64(item.Height) * scale,
			Metadata: map[string]any{"content": "", "storageKey": storageKey, "mimeType": item.MIME, "bytes": len(data), "naturalWidth": item.Width, "naturalHeight": item.Height, "status": "success"},
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	project := map[string]any{
		"id": id, "title": title, "createdAt": now, "updatedAt": now, "nodes": nodes,
		"connections": []any{}, "chatSessions": []any{}, "activeChatId": nil,
		"backgroundMode": "lines", "viewport": map[string]int{"x": 0, "y": 0, "k": 1},
	}
	payload := map[string]any{
		"app": "infinite-canvas", "version": 3, "exportedAt": now,
		"projects": []any{map[string]any{"project": project, "files": files}},
		"source":   map[string]any{"app": "ainovel-workbench", "chapter": view.Chapter, "contentSHA256": domain.ChapterContentSHA256(view.Content), "planningFingerprint": view.PlanningFingerprint},
	}
	entry, err := writer.CreateHeader(&zip.FileHeader{Name: "projects.json", Method: zip.Deflate})
	if err != nil {
		return nil, err
	}
	if err := json.NewEncoder(entry).Encode(payload); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return archive, nil
}

func chapterCanvasTextNode(id, title, content string, y int) chapterCanvasNode {
	return chapterCanvasNode{
		ID: id, Type: "text", Title: title, Position: map[string]int{"x": 40, "y": y}, Width: 520, Height: 640,
		Metadata: map[string]any{"content": content, "fontSize": 18, "status": "success"},
	}
}
