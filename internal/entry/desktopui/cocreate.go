package desktopui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hansshaw/OpenNovel/internal/entry/startup"
	"github.com/hansshaw/OpenNovel/internal/fileio"
	"github.com/hansshaw/OpenNovel/internal/host"
	"github.com/hansshaw/OpenNovel/internal/store"
)

const (
	webCoCreateFile       = "web-cocreate.json"
	webCoCreateMaxTurns   = 48
	webCoCreateMaxInput   = 8000
	webCoCreateMaxDraft   = 40000
	webCoCreateMaxTimeout = 30 * time.Minute
)

type coCreateTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// webCoCreateSession keeps both the model-facing history and the readable
// transcript. The former must retain the raw XML draft so each model turn can
// build on the previous one; the latter keeps the UI free of protocol markup.
type webCoCreateSession struct {
	History     []host.CoCreateMessage   `json:"history,omitempty"`
	Checkpoint  *host.CoCreateCheckpoint `json:"checkpoint,omitempty"`
	Transcript  []coCreateTurn           `json:"transcript,omitempty"`
	DraftPrompt string                   `json:"draftPrompt,omitempty"`
	Ready       bool                     `json:"ready"`
	Confirmed   bool                     `json:"confirmed"`
	Suggestions []string                 `json:"suggestions,omitempty"`
	LastError   string                   `json:"lastError,omitempty"`
	CreatedAt   string                   `json:"createdAt,omitempty"`
	UpdatedAt   string                   `json:"updatedAt,omitempty"`
}

func coCreatePath(dir string) string {
	return filepath.Join(dir, "meta", webCoCreateFile)
}

func readWebCoCreate(dir string) (webCoCreateSession, error) {
	var session webCoCreateSession
	b, err := os.ReadFile(coCreatePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return session, nil
	}
	if err != nil {
		return session, err
	}
	if err := json.Unmarshal(b, &session); err != nil {
		return webCoCreateSession{}, err
	}
	normalizeWebCoCreate(&session)
	return session, nil
}

func normalizeWebCoCreate(session *webCoCreateSession) {
	if session == nil {
		return
	}
	session.DraftPrompt = strings.TrimSpace(session.DraftPrompt)
	// Preserve the entire draft. Confirmation validates its size explicitly;
	// silently clipping here could discard an ending or a user's prohibition.
	// History is the durable source of truth. The host projects a budgeted
	// context and persists a validated summary checkpoint; trimming this archive
	// would silently discard early user requirements and invalidate checkpoints.
	if len(session.Transcript) > webCoCreateMaxTurns {
		session.Transcript = append([]coCreateTurn(nil), session.Transcript[len(session.Transcript)-webCoCreateMaxTurns:]...)
	}
	if len(session.Suggestions) > 3 {
		session.Suggestions = session.Suggestions[:3]
	}
}

func saveWebCoCreate(dir string, session webCoCreateSession) error {
	normalizeWebCoCreate(&session)
	meta := filepath.Join(dir, "meta")
	if err := os.MkdirAll(meta, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(meta, "web-cocreate-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return fileio.Rename(f.Name(), coCreatePath(dir))
}

func coCreateView(session webCoCreateSession, started bool) map[string]any {
	canConfirm := session.Ready &&
		strings.TrimSpace(session.DraftPrompt) != "" &&
		!session.Confirmed &&
		!started
	return map[string]any{
		"transcript":  session.Transcript,
		"draftPrompt": session.DraftPrompt,
		"ready":       session.Ready,
		"confirmed":   session.Confirmed,
		"canConfirm":  canConfirm,
		"started":     started,
		"suggestions": session.Suggestions,
		"lastError":   session.LastError,
		"updatedAt":   session.UpdatedAt,
	}
}

func (s *Server) coCreateStartedLocked(dir string) (bool, error) {
	meta, err := store.NewStore(dir).RunMeta.Load()
	if err != nil {
		return false, err
	}
	return meta != nil && strings.TrimSpace(meta.StartPrompt) != "", nil
}

func (s *Server) getCoCreate(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := r.URL.Query().Get("directory"); d != "" && d != s.dir {
		fail(w, http.StatusConflict, "项目已切换")
		return
	}
	session, err := readWebCoCreate(s.dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, "共创会话读取失败")
		return
	}
	started, err := s.coCreateStartedLocked(s.dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, "项目状态读取失败")
		return
	}
	reply(w, http.StatusOK, coCreateView(session, started))
}

func (s *Server) postCoCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
		Text      string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		fail(w, http.StatusBadRequest, "请填写创作想法或对当前问题的回答")
		return
	}
	if utf8.RuneCountInString(req.Text) > webCoCreateMaxInput {
		fail(w, http.StatusBadRequest, "单次回复不能超过 8000 字")
		return
	}

	s.mu.Lock()
	if req.Directory != "" && req.Directory != s.dir {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "当前项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.stopping || s.busy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先等待当前操作结束")
		return
	}
	rt := s.rt
	if rt == nil {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先配置并连接模型服务")
		return
	}
	started, err := s.coCreateStartedLocked(s.dir)
	if err != nil {
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "项目状态读取失败")
		return
	}
	if started {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "此项目已经进入规划或正文阶段")
		return
	}
	session, err := readWebCoCreate(s.dir)
	if err != nil {
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "共创会话读取失败")
		return
	}
	if session.Confirmed {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "创意已经确认，不能继续追加共创问题")
		return
	}

	if err := s.reserveProjectRunLocked(rt); err != nil {
		message := s.projectRunErrorLocked(err)
		s.mu.Unlock()
		fail(w, http.StatusConflict, message)
		return
	}
	now := time.Now().Format(time.RFC3339)
	if session.CreatedAt == "" {
		session.CreatedAt = now
	}
	session.UpdatedAt = now
	session.LastError = ""
	session.Suggestions = nil
	// A new requirement invalidates confirmation of the previous draft. Keep
	// that draft for recovery, but only a successful new reply can mark it ready.
	session.Ready = false
	session.History = append(session.History, host.CoCreateMessage{Role: "user", Content: req.Text})
	session.Transcript = append(session.Transcript, coCreateTurn{Role: "user", Content: req.Text})
	if err := saveWebCoCreate(s.dir, session); err != nil {
		s.releaseProjectRunLocked(rt)
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "共创会话保存失败")
		return
	}
	history := append([]host.CoCreateMessage(nil), session.History...)
	if session.Checkpoint != nil {
		history = append(history, host.CoCreateCheckpointMessage(session.Checkpoint))
	}
	s.busy = true
	s.job = "cocreate"
	ctx, cancel := context.WithTimeout(s.ctx, webCoCreateMaxTimeout)
	s.cancelJob = cancel
	jobDone := make(chan struct{})
	s.jobDone = jobDone
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	defer close(jobDone)

	replyValue, callErr := rt.CoCreateStream(ctx, history, func(kind, text string) {
		s.mu.Lock()
		if s.rt == rt {
			s.broadcastLocked("cocreate-delta", map[string]string{"kind": kind, "text": text})
		}
		s.mu.Unlock()
	})
	cancel()

	s.mu.Lock()
	defer s.mu.Unlock()
	session, readErr := readWebCoCreate(s.dir)
	if readErr == nil {
		if callErr == nil {
			session.Checkpoint = replyValue.Checkpoint
			if raw := strings.TrimSpace(replyValue.Raw); raw != "" {
				session.History = append(session.History, host.CoCreateMessage{Role: "assistant", Content: raw})
			}
			if message := strings.TrimSpace(replyValue.Message); message != "" {
				session.Transcript = append(session.Transcript, coCreateTurn{Role: "assistant", Content: message})
			}
			if draft := strings.TrimSpace(replyValue.Prompt); draft != "" {
				session.DraftPrompt = draft
			}
			session.Ready = replyValue.Ready
			session.Suggestions = append([]string(nil), replyValue.Suggestions...)
			session.LastError = ""
		} else {
			session.LastError = s.redactLocked(callErr.Error())
		}
		session.UpdatedAt = time.Now().Format(time.RFC3339)
		if saveErr := saveWebCoCreate(s.dir, session); saveErr != nil && callErr == nil {
			callErr = fmt.Errorf("共创会话保存失败")
		}
	} else if callErr == nil {
		callErr = fmt.Errorf("共创会话读取失败，生成结果未保存，请重试")
	}
	s.busy = false
	s.job = ""
	s.cancelJob = nil
	s.jobDone = nil
	s.releaseIdleProjectRunLocked(rt)
	view := coCreateView(session, false)
	s.broadcastLocked("cocreate-updated", view)
	if callErr != nil {
		fail(w, http.StatusBadGateway, s.redactLocked(callErr.Error()))
		return
	}
	reply(w, http.StatusOK, view)
}

func (s *Server) confirmCoCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory   string `json:"directory"`
		DraftPrompt string `json:"draftPrompt"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	if req.Directory != "" && req.Directory != s.dir {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "当前项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.stopping || s.busy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先等待当前操作结束")
		return
	}
	rt := s.rt
	if rt == nil {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "请先配置并连接模型服务")
		return
	}
	started, err := s.coCreateStartedLocked(s.dir)
	if err != nil {
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "项目状态读取失败")
		return
	}
	if started {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "此项目已经进入规划或正文阶段")
		return
	}
	session, err := readWebCoCreate(s.dir)
	if err != nil {
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "共创会话读取失败")
		return
	}
	if !session.Ready {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "共创助手还没有完成关键澄清，请继续回答后再确认")
		return
	}
	if draft := strings.TrimSpace(req.DraftPrompt); draft != "" {
		session.DraftPrompt = draft
	}
	if strings.TrimSpace(session.DraftPrompt) == "" {
		s.mu.Unlock()
		fail(w, http.StatusBadRequest, "完整创作指令不能为空")
		return
	}
	if utf8.RuneCountInString(session.DraftPrompt) > webCoCreateMaxDraft {
		s.mu.Unlock()
		fail(w, http.StatusBadRequest, "完整创作指令不能超过 40000 字")
		return
	}
	if err := s.reserveProjectRunLocked(rt); err != nil {
		message := s.projectRunErrorLocked(err)
		s.mu.Unlock()
		fail(w, http.StatusConflict, message)
		return
	}
	session.Confirmed = true
	session.LastError = ""
	session.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := saveWebCoCreate(s.dir, session); err != nil {
		s.releaseProjectRunLocked(rt)
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "创意确认状态保存失败")
		return
	}

	s.busy = true
	s.job = "start-confirmed"
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancelJob = cancel
	jobDone := make(chan struct{})
	s.jobDone = jobDone
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer close(jobDone)
		defer cancel()
		err := s.prepareAndStart(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.busy = false
		s.job = ""
		s.cancelJob = nil
		s.jobDone = nil
		s.releaseIdleProjectRunLocked(rt)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			if saved, readErr := readWebCoCreate(s.dir); readErr == nil {
				saved.LastError = s.redactLocked(err.Error())
				saved.UpdatedAt = time.Now().Format(time.RFC3339)
				_ = saveWebCoCreate(s.dir, saved)
			}
		}
		result := map[string]any{"action": "start-confirmed", "ok": err == nil}
		if err != nil {
			result["error"] = s.redactLocked(err.Error())
		}
		s.broadcastLocked("result", result)
	}()
	reply(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) resetCoCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Directory != "" && req.Directory != s.dir {
		fail(w, http.StatusConflict, "当前项目已切换，请刷新后重试")
		return
	}
	if s.closed || s.stopping || s.busy || (s.rt != nil && s.rt.Snapshot().IsRunning) {
		fail(w, http.StatusConflict, "请先等待当前操作结束")
		return
	}
	if s.rt == nil {
		if _, err := os.Lstat(s.dir); errors.Is(err, os.ErrNotExist) {
			reply(w, http.StatusOK, coCreateView(webCoCreateSession{}, false))
			return
		}
		lease, err := acquireWorkspaceLease(s.dir, false)
		if err != nil {
			fail(w, http.StatusConflict, "项目正被其他实例使用或不可写："+s.redactLocked(err.Error()))
			return
		}
		defer lease.Close()
	}
	started, err := s.coCreateStartedLocked(s.dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, "项目状态读取失败")
		return
	}
	if started {
		fail(w, http.StatusConflict, "此项目已经进入规划或正文阶段，不能清空共创记录")
		return
	}
	if err := os.Remove(coCreatePath(s.dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		fail(w, http.StatusInternalServerError, "共创会话删除失败")
		return
	}
	s.broadcastLocked("cocreate-updated", coCreateView(webCoCreateSession{}, false))
	reply(w, http.StatusOK, coCreateView(webCoCreateSession{}, false))
}

// prepareAndStart converts the user-confirmed creative brief into the existing
// deterministic startup path. Project profile fields remain additive so users
// can still set genre, audience, target length, and agent directives in the
// profile form.
func (s *Server) prepareAndStart(ctx context.Context) error {
	s.mu.Lock()
	dir, rt := s.dir, s.rt
	s.mu.Unlock()
	if rt == nil {
		return errors.New("请先配置并连接模型服务")
	}
	session, err := readWebCoCreate(dir)
	if err != nil {
		return fmt.Errorf("共创会话读取失败: %w", err)
	}
	if !session.Confirmed {
		return errors.New("请先完成规划前的多轮共创并确认完整创作指令")
	}
	project, err := readProject(dir)
	if err != nil {
		return fmt.Errorf("项目资料读取失败: %w", err)
	}
	if err := store.NewStore(dir).RunMeta.SetTargetChapters(project.TargetChapters); err != nil {
		return fmt.Errorf("目标章节数量保存失败: %w", err)
	}
	prompt, err := startup.PrepareQuick(projectBrief(session.DraftPrompt, project))
	if err != nil {
		return err
	}
	if err := rt.PrepareUserRules(prompt); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return rt.StartPrepared(prompt)
}
