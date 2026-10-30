package desktopui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hansshaw/OpenNovel/internal/host"
)

func TestWebCoCreatePersistsFullHistoryAndCheckpoint(t *testing.T) {
	directory := t.TempDir()
	session := webCoCreateSession{DraftPrompt: strings.Repeat("必须保留", 11000), Checkpoint: &host.CoCreateCheckpoint{CoveredMessages: 30, PrefixHash: "original-prefix-hash", Summary: "原始用户明确禁止恋爱线；结局待确认。"}}
	for i := 0; i < 100; i++ {
		session.History = append(session.History, host.CoCreateMessage{Role: "user", Content: fmt.Sprintf("第%d轮的明确约束", i)})
		session.Transcript = append(session.Transcript, coCreateTurn{Role: "user", Content: fmt.Sprintf("第%d轮的明确约束", i)})
	}
	if err := saveWebCoCreate(directory, session); err != nil {
		t.Fatal(err)
	}
	restored, err := readWebCoCreate(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.History) != 100 || restored.History[0].Content != "第0轮的明确约束" {
		t.Fatalf("early requirements lost: %d messages", len(restored.History))
	}
	if restored.Checkpoint == nil || *restored.Checkpoint != *session.Checkpoint {
		t.Fatalf("checkpoint not persisted: %+v", restored.Checkpoint)
	}
	if restored.DraftPrompt != session.DraftPrompt {
		t.Fatal("long draft silently truncated")
	}
	if len(restored.Transcript) != webCoCreateMaxTurns {
		t.Fatalf("UI projection should remain bounded: %d", len(restored.Transcript))
	}
}

func TestWebCoCreateFailedRevisionCannotConfirmStaleDraft(t *testing.T) {
	for _, failure := range []string{"upstream", "session_read"} {
		t.Run(failure, func(t *testing.T) {
			directory := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"data":[]}`)
					return
				}
				if failure == "upstream" {
					http.Error(w, `{"error":{"message":"invalid request"}}`, http.StatusBadRequest)
					return
				}
				if err := os.WriteFile(coCreatePath(<-directory), []byte("broken persisted session"), 0600); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"content":"<reply>完整新回复</reply><draft>完整新草稿</draft><ready>true</ready><suggestions></suggestions>"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
			}))
			defer upstream.Close()
			s := newTestServer(t, true)
			s.mu.Lock()
			provider := s.baseCfg.Providers["local"]
			provider.BaseURL = upstream.URL + "/v1"
			s.baseCfg.Providers["local"] = provider
			s.rt.Close()
			s.rt = nil
			s.initLocked()
			dir, ready, hostError := s.dir, s.rt != nil, s.hostError
			s.mu.Unlock()
			if !ready {
				t.Fatalf("host initialization: %s", hostError)
			}
			directory <- dir
			initial := webCoCreateSession{DraftPrompt: "旧草稿：主角复仇", Ready: true, History: []host.CoCreateMessage{{Role: "user", Content: "写复仇故事"}}}
			if err := saveWebCoCreate(dir, initial); err != nil {
				t.Fatal(err)
			}
			response := request(s, http.MethodPost, "/api/cocreate", `{"text":"改为放弃复仇，先讨论后再确认。"}`)
			if response.Code != http.StatusBadGateway {
				t.Fatalf("failed co-create returned success: %d %s", response.Code, response.Body.String())
			}
			if failure == "session_read" {
				if !strings.Contains(response.Body.String(), "读取失败") {
					t.Fatalf("missing storage error: %s", response.Body.String())
				}
				return
			}
			saved, err := readWebCoCreate(dir)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Ready || saved.DraftPrompt != initial.DraftPrompt || saved.LastError == "" || len(saved.History) != 2 {
				t.Fatalf("bad failed revision state: %+v", saved)
			}
			if view := coCreateView(saved, false); view["canConfirm"] != false {
				t.Fatal("old draft still confirmable after failed revision")
			}
			confirm := request(s, http.MethodPost, "/api/cocreate/confirm", `{}`)
			if confirm.Code != http.StatusConflict {
				t.Fatalf("stale draft confirmed: %d %s", confirm.Code, confirm.Body.String())
			}
		})
	}
}
