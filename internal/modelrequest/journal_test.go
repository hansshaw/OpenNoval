package modelrequest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func testRecord(id string, offset int) Record {
	return Record{ID: id, Status: "running", StartedAt: time.Unix(1720000000+int64(offset), 0).UTC(), Request: json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`)}
}

func TestJournalUpsertIsolationOrderingAndFilters(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	same, err := Open(filepath.Join(dir, "."))
	if err != nil || j != same {
		t.Fatalf("same directory did not reuse journal: %v", err)
	}
	for index, id := range []string{"first", "second", "third"} {
		record := testRecord(id, index)
		if index != 0 {
			record.Status = "success"
		}
		if err := j.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	active, _ := j.Get("first")
	if active.Status != "running" {
		t.Fatal("reading active journal marked a live request interrupted")
	}
	record := testRecord("second", 1)
	record.Status, record.Chunks = "error", 3
	if err := j.Save(record); err != nil {
		t.Fatal(err)
	}
	all, err := j.List(100, "")
	if err != nil || len(all) != 3 || all[0].ID != "third" || all[1].ID != "second" || all[1].Chunks != 3 {
		t.Fatalf("wrong upsert/order: %+v %v", all, err)
	}
	if len(all[0].Request) != 0 {
		t.Fatal("list unnecessarily includes request payload")
	}
	filtered, err := j.List(1, "error")
	if err != nil || len(filtered) != 1 || filtered[0].ID != "second" {
		t.Fatalf("wrong status filter: %+v %v", filtered, err)
	}
	if limited, _ := j.List(1, ""); len(limited) != 1 || limited[0].ID != "third" {
		t.Fatalf("wrong limit: %+v", limited)
	}
	if _, err := j.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ID: %v", err)
	}
	if _, err := j.List(10, "made-up"); err == nil {
		t.Fatal("invalid status accepted")
	}
	j2, err := Open(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j2.Get("second"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project request visible: %v", err)
	}
	if err := j.Save(testRecord("../bad", 0)); err == nil {
		t.Fatal("unsafe ID accepted")
	}
}

func TestJournalRestartRecoversRunningAndCorruptTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "model-requests.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	running := testRecord("old-running", 0)
	done := testRecord("done", 1)
	done.Status = "success"
	first, _ := json.Marshal(running)
	second, _ := json.Marshal(done)
	data := append(append(append(first, '\n'), second...), []byte("\n{\"id\":\"torn")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	all, err := j.List(10, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("valid records lost after torn tail: %+v %v", all, err)
	}
	recovered, err := j.Get("old-running")
	if err != nil || recovered.Status != "interrupted" || recovered.FinishedAt.IsZero() || recovered.Error == "" {
		t.Fatalf("stale request not recovered: %+v %v", recovered, err)
	}
	if err := j.Save(testRecord("new", 2)); err != nil {
		t.Fatal(err)
	}
	// A separate in-memory instance simulates a new process and also verifies
	// repaired JSONL supports appending after a damaged tail.
	reloaded := &Journal{path: path, records: make(map[string]Record)}
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if record, err := reloaded.Get("done"); err != nil || record.Status != "success" || len(record.Request) == 0 {
		t.Fatalf("completed history lost: %+v %v", record, err)
	}
	if records, _ := reloaded.List(10, ""); len(records) != 3 {
		t.Fatalf("append after tail repair lost: %+v", records)
	}
}

func TestJournalRepairsMissingFinalNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "model-requests.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	record := testRecord("complete", 1)
	record.Status = "success"
	data, _ := json.Marshal(record)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Save(testRecord("next", 2)); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil || len(strings.Split(strings.TrimSpace(string(bytes)), "\n")) != 2 {
		t.Fatalf("missing newline was not repaired: %s %v", bytes, err)
	}
}

func TestJournalRedactsBeforePersistenceAndBoundsJSON(t *testing.T) {
	dir := t.TempDir()
	secret := "secret-\"with\\escapes\nline"
	j, err := Open(dir, "sk-literal-secret", secret)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(map[string]any{
		"api_key":  "not-a-configured-key",
		"messages": []any{map[string]any{"content": "sk-literal-secret " + secret + " https://user:password@api.example.test/v1?token=query-secret#fragment-secret"}},
		"options":  map[string]any{"max_output_tokens": 123, "headers": map[string]any{"X-Auth-Token": "header-secret"}},
	})
	record := testRecord("redact", 1)
	record.Request = request
	record.Endpoint = "https://name:pass@api.example.test/v1?key=endpoint-secret"
	record.Error = "failed https://alice:pwd@api.example.test/?token=error-query Authorization: Bearer bearer-secret api_key=plain-secret"
	record.Response, _ = json.Marshal(map[string]any{"text": strings.Repeat("正文", 50000), "password": "response-secret"})
	if err := j.Save(record); err != nil {
		t.Fatal(err)
	}
	got, err := j.Get(record.ID)
	if err != nil || !got.Truncated || !json.Valid(got.Request) || !json.Valid(got.Response) || len(got.Response) > maxContentBytes || !utf8.Valid(got.Response) {
		t.Fatalf("bounded/redacted JSON invalid: %+v %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "logs", "model-requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"sk-literal-secret", "not-a-configured-key", "query-secret", "fragment-secret", "header-secret", "endpoint-secret", "error-query", "bearer-secret", "plain-secret", "response-secret", "name:pass", "alice:pwd", "user:password"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("journal exposed %q", forbidden)
		}
	}
	var requestValue map[string]any
	if err := json.Unmarshal(got.Request, &requestValue); err != nil {
		t.Fatal(err)
	}
	if requestValue["options"].(map[string]any)["max_output_tokens"] != float64(123) {
		t.Fatal("token budget was redacted as a credential")
	}
	if content := requestValue["messages"].([]any)[0].(map[string]any)["content"].(string); strings.Contains(content, secret) {
		t.Fatal("JSON-escaped configured secret was not removed before persistence")
	}
	if value := j.Redact("URL: https://u:p@site.example/path?a=hidden Bearer unknown-secret"); strings.Contains(value, "hidden") || strings.Contains(value, "unknown-secret") || strings.Contains(value, "u:p") {
		t.Fatalf("free text leaked: %s", value)
	}
	if _, err := Open(dir, "added-secret"); err != nil {
		t.Fatal(err)
	}
	if value := j.Redact("added-secret"); value != redacted {
		t.Fatalf("re-open did not merge secrets: %s", value)
	}
}

func TestJournalSkipsOversizedCorruptLineWithoutLosingLaterRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "model-requests.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	record := testRecord("after-corrupt-line", 0)
	record.Status = "success"
	line, _ := json.Marshal(record)
	data := append([]byte(strings.Repeat("x", 2<<20)+"\n"), line...)
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Get(record.ID); err != nil {
		t.Fatalf("valid record after long damaged line was lost: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() > 4096 {
		t.Fatalf("damaged line was not compacted: %+v %v", info, err)
	}
}

func TestJournalConcurrentUpdatesAndRetention(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := range 12 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for index := range 5 {
				record := testRecord(fmt.Sprintf("%d-%d", worker, index), worker*10+index)
				if err := j.Save(record); err != nil {
					t.Error(err)
				}
				record.Status = "success"
				if err := j.Save(record); err != nil {
					t.Error(err)
				}
				_, _ = j.List(10, "")
				_, _ = j.Get(record.ID)
			}
		}(worker)
	}
	wg.Wait()
	if records, _ := j.List(500, ""); len(records) != 60 {
		t.Fatalf("concurrent records lost: %d", len(records))
	}
	for index := range maxRecords + 10 {
		record := testRecord(fmt.Sprintf("retained-%04d", index), 10000+index)
		record.Status = "success"
		if err := j.Save(record); err != nil {
			t.Fatal(err)
		}
	}
	if records, _ := j.List(1000, ""); len(records) != maxRecords || records[0].ID != "retained-0509" {
		t.Fatalf("retention/order wrong: %d %+v", len(records), records[0])
	}
	if info, err := os.Stat(j.path); err != nil || info.Size() > maxJournalBytes {
		t.Fatalf("journal grew past limit: %+v %v", info, err)
	}
}
