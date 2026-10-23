package desktopui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/voocel/ainovel-cli/internal/chaptermedia"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Image decompression is bounded separately from model concurrency. Refuse an
// upload burst before buffering bodies rather than queueing unbounded decoders.
var chapterMediaUploads = make(chan struct{}, 2)

type chapterMediaItemView struct {
	chaptermedia.Item
	URL string `json:"url"`
}

type chapterMediaListView struct {
	Directory string                 `json:"directory"`
	Chapter   int                    `json:"chapter"`
	Items     []chapterMediaItemView `json:"items"`
}

func (s *Server) chapterMediaDirectoryLocked(w http.ResponseWriter, directory string, chapter int, mutation bool) bool {
	if directory == "" || directory != s.dir {
		fail(w, 409, "当前项目已切换，请刷新后重试")
		return false
	}
	if chapter < 1 || chapter > 1_000_000 {
		fail(w, 400, "无效章节")
		return false
	}
	if s.closed || (mutation && (s.busy || s.stopping || s.backgroundProjectBusyLocked(directory))) {
		fail(w, 409, "项目正在处理其他操作，请稍后重试")
		return false
	}
	project, err := readProject(directory)
	if err != nil || (mutation && project.Archived) {
		fail(w, 409, "项目资料不可用或项目已归档")
		return false
	}
	st := store.NewStore(directory)
	outline, err := st.Outline.LoadOutlineView()
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return false
	}
	for _, entry := range outline {
		if entry.Chapter == chapter {
			return true
		}
	}
	progress, err := st.Progress.Load()
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return false
	}
	if progress != nil && (slices.Contains(progress.CompletedChapters, chapter) || progress.InProgressChapter == chapter) {
		return true
	}
	fail(w, 404, "章节不存在，请先保存章节规划")
	return false
}

func (s *Server) withChapterMediaWriteLocked(write func() error) error {
	if s.rt != nil {
		return s.rt.WithProjectAttachmentWrite(write)
	}
	return withWorkspaceLease(s.dir, write)
}

func (s *Server) chapterMediaErrorLocked(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chaptermedia.ErrInvalid):
		fail(w, 400, err.Error())
	case errors.Is(err, chaptermedia.ErrUnsafe):
		fail(w, 400, "配图路径包含链接或不安全文件，未执行操作")
	case errors.Is(err, chaptermedia.ErrNotFound), os.IsNotExist(err):
		fail(w, 404, "配图不存在")
	case errors.Is(err, chaptermedia.ErrCorrupt):
		fail(w, 409, "配图文件损坏或已变化，请重新导入")
	case errors.Is(err, host.ErrProjectSnapshotBusy), errors.Is(err, host.ErrProjectDeleting):
		fail(w, 409, "项目正在使用、切换或关闭，请稍后重试")
	default:
		fail(w, 500, "配图操作失败："+s.redactLocked(err.Error()))
	}
}

func (s *Server) chapterMediaListLocked(chapter int) (chapterMediaListView, error) {
	view := chapterMediaListView{Directory: s.dir, Chapter: chapter, Items: []chapterMediaItemView{}}
	items, err := chaptermedia.List(s.dir, chapter)
	if err != nil {
		return view, err
	}
	for _, item := range items {
		view.Items = append(view.Items, chapterMediaItemView{Item: item, URL: fmt.Sprintf("/api/chapters/%d/media/%s/content?directory=%s", chapter, item.ID, url.QueryEscape(s.dir))})
	}
	return view, nil
}

func (s *Server) chapterMedia(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || chapter < 1 || chapter > 1_000_000 {
		fail(w, 400, "无效章节")
		return
	}
	if r.Method == http.MethodPost {
		s.uploadChapterMedia(w, r, chapter)
		return
	}
	directory := r.URL.Query().Get("directory")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.chapterMediaDirectoryLocked(w, directory, chapter, false) {
		return
	}
	view, err := s.chapterMediaListLocked(chapter)
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return
	}
	reply(w, 200, view)
}

func (s *Server) uploadChapterMedia(w http.ResponseWriter, r *http.Request, chapter int) {
	select {
	case chapterMediaUploads <- struct{}{}:
		defer func() { <-chapterMediaUploads }()
	default:
		fail(w, 429, "正在处理其他图片，请稍后重试")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, chaptermedia.MaxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
		fail(w, 400, "无法读取图片，单张图片不能超过 20 MiB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		fail(w, 400, "请每次上传一张图片")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请选择图片文件")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, chaptermedia.MaxImageBytes+1))
	if err != nil {
		fail(w, 400, "图片读取失败")
		return
	}
	upload, err := chaptermedia.Prepare(data, header.Filename)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	directory, caption, placement := r.FormValue("directory"), r.FormValue("caption"), r.FormValue("placement")
	if placement == "" {
		placement = "chapter-end"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.chapterMediaDirectoryLocked(w, directory, chapter, true) {
		return
	}
	err = s.withChapterMediaWriteLocked(func() error { _, err := chaptermedia.Add(directory, chapter, upload, caption, placement); return err })
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return
	}
	view, err := s.chapterMediaListLocked(chapter)
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return
	}
	s.broadcastLocked("chapter-media", map[string]any{"directory": directory, "chapter": chapter})
	reply(w, http.StatusCreated, view)
}

func (s *Server) editChapterMedia(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || chapter < 1 {
		fail(w, 400, "无效章节")
		return
	}
	var req struct {
		Directory string `json:"directory"`
		chaptermedia.Update
	}
	if r.Method == http.MethodPatch {
		if !readJSON(w, r, &req) {
			return
		}
		if req.Caption == nil && req.Placement == nil && req.Order == nil {
			fail(w, 400, "没有可保存的配图修改")
			return
		}
	} else {
		req.Directory = r.URL.Query().Get("directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.chapterMediaDirectoryLocked(w, req.Directory, chapter, true) {
		return
	}
	err = s.withChapterMediaWriteLocked(func() error {
		if r.Method == http.MethodDelete {
			return chaptermedia.Remove(req.Directory, chapter, r.PathValue("media"))
		}
		return chaptermedia.Edit(req.Directory, chapter, r.PathValue("media"), req.Update)
	})
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return
	}
	view, err := s.chapterMediaListLocked(chapter)
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return
	}
	s.broadcastLocked("chapter-media", map[string]any{"directory": req.Directory, "chapter": chapter})
	reply(w, 200, view)
}

func (s *Server) chapterMediaContent(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || chapter < 1 {
		fail(w, 400, "无效章节")
		return
	}
	s.mu.Lock()
	if !s.chapterMediaDirectoryLocked(w, r.URL.Query().Get("directory"), chapter, false) {
		s.mu.Unlock()
		return
	}
	item, data, err := chaptermedia.Read(s.dir, chapter, r.PathValue("media"))
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", item.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", `"`+item.SHA256+`"`)
	w.Header().Set("Content-Disposition", "inline")
	created, _ := time.Parse(time.RFC3339Nano, item.CreatedAt)
	http.ServeContent(w, r, item.ID, created, bytes.NewReader(data))
}
