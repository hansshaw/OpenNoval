package desktopui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/voocel/ainovel-cli/internal/chaptermedia"
	"github.com/voocel/ainovel-cli/internal/mediaworkspace"
	"github.com/voocel/ainovel-cli/internal/store"
)

// chapterMediaExport snapshots all input before sending any ZIP bytes. This
// avoids sending a partial archive or mixing an old paragraph with a new image.
func (s *Server) chapterMediaExport(w http.ResponseWriter, r *http.Request) {
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
		fail(w, http.StatusConflict, "请先暂停并等待本项目操作结束，再导出当前章节配图包")
		return
	}
	var archive *os.File
	err = s.withVersionSnapshotLocked(func() error {
		view, readErr := readChapterEdit(store.NewStore(s.dir), chapter)
		if readErr != nil {
			return readErr
		}
		archive, readErr = createChapterMediaArchive(r.Context(), s.dir, view)
		return readErr
	})
	if err != nil {
		switch {
		case errors.Is(err, mediaworkspace.ErrTooLarge):
			fail(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, mediaworkspace.ErrCorrupt), errors.Is(err, mediaworkspace.ErrInvalid):
			fail(w, http.StatusConflict, "配图数据已变化，请刷新后重试")
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
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="chapter-%06d-media.zip"`, chapter))
	http.ServeContent(w, r, fmt.Sprintf("chapter-%06d-media.zip", chapter), time.Time{}, archive)
}

func createChapterMediaArchive(ctx context.Context, directory string, view chapterEditView) (*os.File, error) {
	items, err := chaptermedia.List(directory, view.Chapter)
	if err != nil {
		return nil, err
	}
	chapter := mediaworkspace.Chapter{Number: view.Chapter, Title: view.Title, VolumeTitle: view.VolumeTitle}
	if view.HasContent {
		chapter.Body = &view.Content
	}
	if view.HasPlan {
		chapter.Plan = &mediaworkspace.Plan{CoreEvent: view.CoreEvent, Hook: view.Hook, Scenes: append([]string{}, view.Scenes...), Fingerprint: view.PlanningFingerprint}
	}
	images := make([]mediaworkspace.Image, 0, len(items))
	snapshot := make(map[string]chaptermedia.Item, len(items))
	for _, item := range items {
		snapshot[item.ID] = item
		images = append(images, mediaworkspace.Image{
			ID: item.ID, Name: item.Name, MIME: item.MIME, Bytes: item.Bytes,
			Width: item.Width, Height: item.Height, Caption: item.Caption,
			Placement: item.Placement, Order: item.Order, CreatedAt: item.CreatedAt, SHA256: item.SHA256,
		})
	}
	return mediaworkspace.CreateArchive(ctx, chapter, images, func(ctx context.Context, image mediaworkspace.Image) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, data, err := chaptermedia.Read(directory, view.Chapter, image.ID)
		if err != nil {
			return nil, err
		}
		if item != snapshot[image.ID] {
			return nil, chaptermedia.ErrCorrupt
		}
		return data, nil
	})
}
