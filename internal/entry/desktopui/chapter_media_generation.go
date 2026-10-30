package desktopui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hansshaw/OpenNovel/internal/chaptermedia"
	"github.com/hansshaw/OpenNovel/internal/projectversion"
	"github.com/hansshaw/OpenNovel/internal/store"
)

type chapterGeneration struct {
	chaptermedia.Generation
	directory string
	cancel    context.CancelFunc
}
type chapterGenerationView struct {
	chaptermedia.Generation
	URL string `json:"url,omitempty"`
}
type generationRequest struct {
	Directory    string `json:"directory"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	Instructions string `json:"instructions"`
	Placement    string `json:"placement"`
}

func generationSource(directory string, chapter int, source string) (string, error) {
	st := store.NewStore(directory)
	if source == "draft" {
		return st.Drafts.LoadDraft(chapter)
	}
	return st.Drafts.LoadChapterText(chapter)
}
func generationHash(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])
}
func (s *Server) generationViewLocked(g chaptermedia.Generation, directory string) chapterGenerationView {
	v := chapterGenerationView{Generation: g}
	if g.Status == "running" {
		if active := s.mediaGenerations[g.ID]; active != nil && active.directory == directory {
			v.Generation = active.Generation
		} else {
			v.Status = "failed"
			v.Error = "上次生成因应用退出而中断，请重新生成；服务商可能已计费"
		}
	}
	if v.Status == "completed" {
		v.URL = fmt.Sprintf("/api/chapters/%d/media/generate/%s/content?directory=%s", g.Chapter, g.ID, url.QueryEscape(directory))
	}
	return v
}
func (s *Server) chapterGenerations(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil || chapter < 1 || chapter > 1_000_000 {
		fail(w, 400, "无效章节")
		return
	}
	if r.Method == http.MethodPost {
		s.startChapterGeneration(w, r, chapter)
		return
	}
	directory := r.URL.Query().Get("directory")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.chapterMediaDirectoryLocked(w, directory, chapter, false) {
		return
	}
	jobs, err := chaptermedia.ListGenerations(directory, chapter)
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return
	}
	views := make([]chapterGenerationView, 0, len(jobs))
	for _, g := range jobs {
		if active := s.mediaGenerations[g.ID]; active != nil && active.directory == directory {
			g = active.Generation
		}
		views = append(views, s.generationViewLocked(g, directory))
	}
	reply(w, 200, map[string]any{"directory": directory, "chapter": chapter, "jobs": views})
}
func (s *Server) startChapterGeneration(w http.ResponseWriter, r *http.Request, chapter int) {
	var req generationRequest
	if !readJSONLimit(w, r, &req, 16<<10) {
		return
	}
	if req.Source == "" {
		req.Source = "final"
	}
	if req.Placement == "" {
		req.Placement = "chapter-end"
	}
	if !validMediaKind(req.Kind) || (req.Source != "final" && req.Source != "draft") || !validMediaText(req.Instructions, 2000, true) || (req.Placement != "chapter-start" && req.Placement != "chapter-end") {
		fail(w, 400, "请选择有效媒体类型、正文来源和配图位置；补充要求不能超过 2000 字")
		return
	}
	configPath, configMu, ok := s.mediaModelConfigFile(w)
	if !ok {
		return
	}
	configMu.RLock()
	cfg, err := loadMediaModelConfig(configPath)
	configMu.RUnlock()
	if err != nil {
		fail(w, 500, "媒体模型配置读取失败")
		return
	}
	var connection mediaModelConnection
	for _, c := range cfg.Connections {
		if c.ID == cfg.Selected[req.Kind] && c.Kind == req.Kind {
			connection = c
			break
		}
	}
	if connection.ID == "" {
		fail(w, 400, "请先在对应类型的模型设置中选择默认连接")
		return
	}
	if connection.Protocol != "openai-compatible" {
		fail(w, 400, "该连接使用自定义协议，尚不能生成；请选择支持 Images、Videos 或 Audio Speech API 的 OpenAI 兼容连接")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.chapterMediaDirectoryLocked(w, req.Directory, chapter, true) {
		return
	}
	activeCount := 0
	for _, g := range s.mediaGenerations {
		if g.cancel == nil {
			continue
		}
		activeCount++
		if g.directory == req.Directory && g.Chapter == chapter && g.Kind == req.Kind {
			fail(w, 409, "本章同类型媒体正在生成，请等待或取消后重试")
			return
		}
	}
	if activeCount >= 2 {
		fail(w, 429, "最多同时生成两个媒体任务，请等待当前任务完成")
		return
	}
	var body string
	var job chaptermedia.Generation
	err = s.withChapterMediaWriteLocked(func() error {
		existing, e := chaptermedia.ListGenerations(req.Directory, chapter)
		if e != nil {
			return e
		}
		if len(existing) >= chaptermedia.MaxGenerationJobs {
			return fmt.Errorf("%w：本章最多保留 100 个生成记录，请先删除旧记录", chaptermedia.ErrInvalid)
		}
		body, e = generationSource(req.Directory, chapter, req.Source)
		if e != nil {
			return e
		}
		if strings.TrimSpace(body) == "" {
			return fmt.Errorf("%w：所选章节尚无已保存的正文，请先完成或保存正文", chaptermedia.ErrInvalid)
		}
		if !utf8.ValidString(body) || utf8.RuneCountInString(body) > 100000 || (req.Kind != "audio" && utf8.RuneCountInString(body)+utf8.RuneCountInString(req.Instructions) > 30000) {
			return fmt.Errorf("%w：图片/视频正文及要求限 30000 字，朗读正文限 100000 字；内容不会自动截断", chaptermedia.ErrInvalid)
		}
		var id [16]byte
		if _, e = rand.Read(id[:]); e != nil {
			return e
		}
		job = chaptermedia.Generation{ID: hex.EncodeToString(id[:]), Chapter: chapter, Kind: req.Kind, Source: req.Source, SourceSHA256: generationHash(body), Model: redactMediaModelName(connection.Model, connection.APIKey), Status: "running", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		return chaptermedia.SaveGeneration(req.Directory, job, nil)
	})
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Minute)
	if s.mediaGenerations == nil {
		s.mediaGenerations = make(map[string]*chapterGeneration)
	}
	s.mediaGenerations[job.ID] = &chapterGeneration{Generation: job, directory: req.Directory, cancel: cancel}
	s.wg.Add(1)
	go s.runChapterGeneration(ctx, connection, req, job, body)
	reply(w, 202, s.generationViewLocked(job, req.Directory))
}
func (s *Server) runChapterGeneration(ctx context.Context, c mediaModelConnection, req generationRequest, job chaptermedia.Generation, body string) {
	defer s.wg.Done()
	update := func(percent int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if g := s.mediaGenerations[job.ID]; g != nil && g.Status == "running" {
			g.Progress = max(0, min(percent, 99))
		}
	}
	data, mime, err := generateChapterMedia(ctx, c, req, body, update)
	var image chaptermedia.Upload
	if err == nil && req.Kind == "image" {
		image, err = chaptermedia.Prepare(data, fmt.Sprintf("第%d章生成配图", job.Chapter))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.mediaGenerations[job.ID]
	if active == nil {
		return
	}
	defer func() { active.cancel(); active.cancel = nil }()
	if active.Status == "cancelled" || errors.Is(ctx.Err(), context.Canceled) {
		job.Status = "cancelled"
		job.Error = "已停止本地生成；服务商已接收的任务可能继续处理或计费"
	} else if err != nil {
		job.Status = "failed"
		job.Error = mediaGenerationError(err, c.APIKey)
	} else if s.closed {
		job.Status = "cancelled"
		job.Error = "应用已关闭，任务已中断"
	} else {
		// Only attach to the exact saved text that was used to create the request.
		err = s.withGenerationWriteLocked(req.Directory, func() error {
			pending, e := projectversion.HasPendingRecovery(req.Directory)
			if e != nil {
				return e
			}
			if pending {
				return errors.New("项目正在恢复版本，生成结果未写入")
			}
			current, e := generationSource(req.Directory, job.Chapter, req.Source)
			if e != nil {
				return e
			}
			if generationHash(current) != job.SourceSHA256 {
				return errors.New("章节正文已变化，生成结果未关联新正文，请重新生成")
			}
			project, e := readProject(req.Directory)
			if e != nil {
				return e
			}
			if project.Archived {
				return errors.New("项目已归档，生成结果未写入")
			}
			job.Status = "completed"
			job.Progress = 100
			job.MIME = mime
			job.Bytes = len(data)
			digest := sha256.Sum256(data)
			job.SHA256 = hex.EncodeToString(digest[:])
			if req.Kind == "image" {
				item, e := chaptermedia.Add(req.Directory, job.Chapter, image, "AI 根据章节正文生成", req.Placement)
				if e != nil {
					return e
				}
				job.ImageID = item.ID
				job.MIME = item.MIME
				if e := chaptermedia.SaveGeneration(req.Directory, job, nil); e != nil {
					_ = chaptermedia.Remove(req.Directory, job.Chapter, item.ID)
					job.ImageID = ""
					return e
				}
				return nil
			}
			return chaptermedia.SaveGeneration(req.Directory, job, data)
		})
		if err != nil {
			job.Status = "failed"
			job.Error = mediaGenerationError(err, c.APIKey)
			job.MIME = ""
			job.Bytes = 0
			job.SHA256 = ""
		}
	}
	if job.Status != "completed" {
		// Never use the new project's Host to write a previous project's result.
		_ = s.withGenerationWriteLocked(req.Directory, func() error { return chaptermedia.SaveGeneration(req.Directory, job, nil) })
	}
	active.Generation = job
	s.broadcastLocked("chapter-media", map[string]any{"directory": req.Directory, "chapter": job.Chapter})
}
func mediaGenerationError(err error, key string) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "媒体生成超时，请检查服务商任务状态后再重试，避免重复计费"
	}
	message := err.Error()
	if key != "" {
		message = strings.ReplaceAll(message, key, "[redacted]")
	}
	if len([]rune(message)) > 600 {
		message = string([]rune(message)[:600])
	}
	return message
}
func (s *Server) deleteChapterGeneration(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil {
		fail(w, 400, "无效章节")
		return
	}
	directory := r.URL.Query().Get("directory")
	id := r.PathValue("generation")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.chapterMediaDirectoryLocked(w, directory, chapter, true) {
		return
	}
	if active := s.mediaGenerations[id]; active != nil && active.directory == directory && active.Chapter == chapter && active.cancel != nil {
		active.Status = "cancelled"
		active.Error = "正在停止本地任务；服务商任务可能仍继续或计费"
		active.cancel()
		reply(w, 200, s.generationViewLocked(active.Generation, directory))
		return
	}
	err = s.withChapterMediaWriteLocked(func() error { return chaptermedia.RemoveGeneration(directory, chapter, id) })
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		return
	}
	delete(s.mediaGenerations, id)
	reply(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) chapterGenerationContent(w http.ResponseWriter, r *http.Request) {
	chapter, err := strconv.Atoi(r.PathValue("chapter"))
	if err != nil {
		fail(w, 400, "无效章节")
		return
	}
	s.mu.Lock()
	if !s.chapterMediaDirectoryLocked(w, r.URL.Query().Get("directory"), chapter, false) {
		s.mu.Unlock()
		return
	}
	g, data, err := chaptermedia.ReadGeneration(s.dir, chapter, r.PathValue("generation"))
	if err != nil {
		s.chapterMediaErrorLocked(w, err)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", g.MIME)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	ext := map[string]string{"audio/mpeg": ".mp3", "video/mp4": ".mp4", "image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}[g.MIME]
	http.ServeContent(w, r, fmt.Sprintf("chapter-%d-%s%s", chapter, g.ID, ext), time.Time{}, bytes.NewReader(data))
}

// The job keeps its original project identity while the workbench changes views.
// Each write is short; no project lease or server mutex is held over provider I/O.
func (s *Server) withGenerationWriteLocked(directory string, write func() error) error {
	if s.dir == directory && s.rt != nil {
		return s.rt.WithProjectAttachmentWrite(write)
	}
	if view := s.projectRuntimes[projectKey(directory)]; view != nil && view.rt != nil {
		return view.rt.WithProjectAttachmentWrite(write)
	}
	return withWorkspaceLease(directory, write)
}

func redactMediaModelName(name, key string) string {
	if key != "" {
		return strings.ReplaceAll(name, key, "[redacted]")
	}
	return name
}
