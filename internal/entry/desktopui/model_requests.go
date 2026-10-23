package desktopui

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/hansshaw/AInoval-web-ui/internal/modelrequest"
)

func (s *Server) listModelRequests(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 500 {
			fail(w, http.StatusBadRequest, "记录数量应为 1–500")
			return
		}
	}
	status := r.URL.Query().Get("status")
	if status != "" && !modelrequest.ValidStatus(status) {
		fail(w, http.StatusBadRequest, "无效的模型请求状态")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if directory := r.URL.Query().Get("directory"); directory != "" && directory != s.dir {
		fail(w, http.StatusConflict, "当前项目已切换，请刷新后重试")
		return
	}
	journal, err := modelrequest.Open(s.dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, "模型请求记录读取失败："+s.redactLocked(err.Error()))
		return
	}
	records, err := journal.List(limit, status)
	if err != nil {
		fail(w, http.StatusInternalServerError, "模型请求记录读取失败："+s.redactLocked(err.Error()))
		return
	}
	s.replyModelRequestsLocked(w, map[string]any{"directory": s.dir, "requests": records})
}

func (s *Server) getModelRequest(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if directory := r.URL.Query().Get("directory"); directory != "" && directory != s.dir {
		fail(w, http.StatusConflict, "当前项目已切换，请刷新后重试")
		return
	}
	journal, err := modelrequest.Open(s.dir)
	if err != nil {
		fail(w, http.StatusInternalServerError, "模型请求记录读取失败："+s.redactLocked(err.Error()))
		return
	}
	record, err := journal.Get(r.PathValue("id"))
	if errors.Is(err, modelrequest.ErrNotFound) {
		fail(w, http.StatusNotFound, "模型请求记录不存在")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "模型请求记录读取失败："+s.redactLocked(err.Error()))
		return
	}
	s.replyModelRequestsLocked(w, record)
}

// Redact decoded string values rather than raw JSON bytes: credentials containing
// quotes, backslashes or newlines must still match their configured literal value.
func (s *Server) replyModelRequestsLocked(w http.ResponseWriter, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		fail(w, http.StatusInternalServerError, "模型请求记录无法显示")
		return
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&decoded) != nil {
		fail(w, http.StatusInternalServerError, "模型请求记录无法显示")
		return
	}
	var redact func(any) any
	redact = func(value any) any {
		switch value := value.(type) {
		case string:
			return s.redactLocked(value)
		case []any:
			for i := range value {
				value[i] = redact(value[i])
			}
		case map[string]any:
			out := make(map[string]any, len(value))
			for key, child := range value {
				out[s.redactLocked(key)] = redact(child)
			}
			return out
		}
		return value
	}
	reply(w, http.StatusOK, redact(decoded))
}
