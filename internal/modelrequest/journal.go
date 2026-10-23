// Package modelrequest stores bounded, redacted model request diagnostics per project.
package modelrequest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hansshaw/AInoval-web-ui/internal/fileio"
)

const (
	maxRecords      = 500
	maxContentBytes = 64 << 10
	maxJournalBytes = 16 << 20
	maxStringBytes  = 16 << 10
	redacted        = "[已隐藏]"
)

var ErrNotFound = errors.New("model request not found")

// Record is one model invocation. Repeated Save calls replace the same ID.
// Request and Response contain redacted diagnostic snapshots, never wire headers.
type Record struct {
	ID              string          `json:"id"`
	Provider        string          `json:"provider"`
	Model           string          `json:"model"`
	Agent           string          `json:"agent"`
	Endpoint        string          `json:"endpoint"`
	Stream          bool            `json:"stream"`
	StartedAt       time.Time       `json:"startedAt"`
	FinishedAt      time.Time       `json:"finishedAt,omitempty"`
	FirstChunkAt    time.Time       `json:"firstChunkAt,omitempty"`
	LastChunkAt     time.Time       `json:"lastChunkAt,omitempty"`
	DurationMs      int64           `json:"durationMs"`
	FirstChunkMs    int64           `json:"firstChunkMs"`
	IdleTimeoutMs   int64           `json:"idleTimeoutMs"`
	MaxOutputTokens int             `json:"maxOutputTokens"`
	MessageCount    int             `json:"messageCount"`
	ToolCount       int             `json:"toolCount"`
	Chunks          int             `json:"chunks"`
	OutputBytes     int             `json:"outputBytes"`
	InputTokens     int             `json:"inputTokens"`
	OutputTokens    int             `json:"outputTokens"`
	StopReason      string          `json:"stopReason"`
	Status          string          `json:"status"`
	Error           string          `json:"error,omitempty"`
	Request         json.RawMessage `json:"request,omitempty"`
	Response        json.RawMessage `json:"response,omitempty"`
	Truncated       bool            `json:"truncated"`
}

type Journal struct {
	mu      sync.Mutex
	path    string
	records map[string]Record
	secrets []string
	size    int64
	writes  int
}

var journals = struct {
	sync.Mutex
	byDirectory map[string]*Journal
}{byDirectory: make(map[string]*Journal)}

// Open shares a journal within this process. Only its first open recovers stale
// running entries, so an HTTP history read cannot interrupt an active invocation.
func Open(directory string, secrets ...string) (*Journal, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(directory); err == nil {
		directory = resolved
	}
	journals.Lock()
	defer journals.Unlock()
	if j := journals.byDirectory[directory]; j != nil {
		j.mu.Lock()
		j.addSecretsLocked(secrets)
		j.mu.Unlock()
		return j, nil
	}
	j := &Journal{path: filepath.Join(directory, "logs", "model-requests.jsonl"), records: make(map[string]Record)}
	j.addSecretsLocked(secrets)
	if err := j.load(); err != nil {
		return nil, err
	}
	journals.byDirectory[directory] = j
	return j, nil
}

func (j *Journal) addSecretsLocked(values []string) {
	seen := make(map[string]bool, len(j.secrets))
	for _, value := range j.secrets {
		seen[value] = true
	}
	for _, value := range values {
		if value != "" && !seen[value] {
			j.secrets = append(j.secrets, value)
			seen[value] = true
		}
	}
	sort.Slice(j.secrets, func(a, b int) bool { return len(j.secrets[a]) > len(j.secrets[b]) })
}

func (j *Journal) load() error {
	f, err := os.Open(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64<<10)
	dirty := false
	var line []byte
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		j.size += int64(len(fragment))
		if !oversized {
			if len(line)+len(fragment) > 1<<20 {
				oversized, line = true, nil
			} else {
				line = append(line, fragment...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(line) != 0 && line[len(line)-1] != '\n' {
			dirty = true // Repair a complete last record without a newline, too.
		}
		if oversized {
			dirty = true
		} else if len(bytes.TrimSpace(line)) != 0 {
			var record Record
			if json.Unmarshal(line, &record) != nil || record.ID == "" {
				dirty = true // A killed process may leave an incomplete final line.
			} else {
				record = j.sanitizeLocked(record)
				j.records[record.ID] = record
			}
		}
		line, oversized = line[:0], false
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	for id, record := range j.records {
		if record.Status == "running" {
			record.Status = "interrupted"
			record.Error = "服务重启，未收到该请求的完整结果"
			record.FinishedAt = time.Now().UTC()
			j.records[id] = record
			dirty = true
		}
	}
	if dirty || len(j.records) > maxRecords || j.size > maxJournalBytes {
		return j.compactLocked()
	}
	return nil
}

func (j *Journal) Save(record Record) error {
	if strings.TrimSpace(record.ID) == "" || len(record.ID) > 160 || strings.ContainsAny(record.ID, "/\\\x00\r\n") {
		return fmt.Errorf("invalid model request ID")
	}
	if !ValidStatus(record.Status) {
		return fmt.Errorf("invalid model request status")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	record = j.sanitizeLocked(record)
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	n, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	j.records[record.ID] = record
	j.size += int64(n)
	j.writes++
	if j.size > maxJournalBytes || len(j.records) > maxRecords || j.writes >= 256 {
		return j.compactLocked()
	}
	return nil
}

func ValidStatus(status string) bool {
	switch status {
	case "running", "success", "error", "canceled", "interrupted":
		return true
	}
	return false
}

// List returns newest first. Large payloads are available through Get only.
func (j *Journal) List(limit int, status string) ([]Record, error) {
	if status != "" && !ValidStatus(status) {
		return nil, fmt.Errorf("invalid model request status")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxRecords {
		limit = maxRecords
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	all := j.sortedLocked()
	result := make([]Record, 0, min(limit, len(all)))
	for _, record := range all {
		if status != "" && record.Status != status {
			continue
		}
		record.Request, record.Response = nil, nil
		record = j.sanitizeLocked(record)
		result = append(result, record)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (j *Journal) Get(id string) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return j.sanitizeLocked(record), nil
}

func (j *Journal) sortedLocked() []Record {
	records := make([]Record, 0, len(j.records))
	for _, record := range j.records {
		records = append(records, record)
	}
	sort.Slice(records, func(a, b int) bool {
		if records[a].StartedAt.Equal(records[b].StartedAt) {
			return records[a].ID > records[b].ID
		}
		return records[a].StartedAt.After(records[b].StartedAt)
	})
	return records
}

func (j *Journal) compactLocked() error {
	records := j.sortedLocked()
	var data bytes.Buffer
	keep := make(map[string]Record)
	for _, record := range records {
		record = j.sanitizeLocked(record)
		line, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if len(keep) >= maxRecords || data.Len()+len(line)+1 > maxJournalBytes {
			break
		}
		data.Write(line)
		data.WriteByte('\n')
		keep[record.ID] = record
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(j.path), ".model-requests-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fileio.Rename(f.Name(), j.path); err != nil {
		return err
	}
	j.records, j.size, j.writes = keep, int64(data.Len()), 0
	return nil
}

var urlPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
var authPattern = regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[a-z0-9._~+/=-]+`)
var credentialPattern = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|api[_-]?key|access[_-]?token|password|client[_-]?secret)\s*["']?\s*[:=]\s*["']?[^"'\s,;}]+`)

func (j *Journal) Redact(value string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.redactLocked(value)
}

func (j *Journal) redactLocked(value string) string {
	for _, secret := range j.secrets {
		value = strings.ReplaceAll(value, secret, redacted)
	}
	value = urlPattern.ReplaceAllStringFunc(value, func(value string) string {
		u, err := url.Parse(value)
		if err != nil {
			return "[URL 已隐藏]"
		}
		u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
		return u.String()
	})
	value = authPattern.ReplaceAllString(value, redacted)
	return credentialPattern.ReplaceAllString(value, "$1="+redacted)
}

func (j *Journal) sanitizeLocked(record Record) Record {
	fields := []*string{&record.ID, &record.Provider, &record.Model, &record.Agent, &record.Endpoint, &record.StopReason, &record.Error}
	for _, field := range fields {
		*field = j.redactLocked(*field)
		if len(*field) > maxStringBytes {
			*field = boundString(*field, maxStringBytes)
			record.Truncated = true
		}
	}
	var cut bool
	record.Request, cut = j.sanitizeJSONLocked(record.Request)
	record.Truncated = record.Truncated || cut
	record.Response, cut = j.sanitizeJSONLocked(record.Response)
	record.Truncated = record.Truncated || cut
	return record
}

func sensitiveKey(key string) bool {
	k := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
	return k == "key" || k == "token" || strings.Contains(k, "authorization") || strings.Contains(k, "apikey") || strings.Contains(k, "authtoken") || strings.Contains(k, "accesstoken") || strings.Contains(k, "refreshtoken") || strings.Contains(k, "secret") || strings.Contains(k, "password") || strings.Contains(k, "cookie") || strings.Contains(k, "credential")
}

func (j *Journal) sanitizeJSONLocked(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return json.RawMessage(`{"unavailable":"invalid or incomplete JSON"}`), true
	}
	budget, truncated := maxContentBytes/2, false
	var sanitize func(any, int) any
	sanitize = func(value any, depth int) any {
		if depth > 30 || budget <= 0 {
			truncated = true
			return "[内容已截断]"
		}
		switch value := value.(type) {
		case string:
			value = j.redactLocked(value)
			if len(value) > min(budget, maxStringBytes) {
				value = boundString(value, min(budget, maxStringBytes))
				truncated = true
			}
			budget -= len(value)
			return value
		case map[string]any:
			out := make(map[string]any)
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if budget <= 0 {
					out["_truncated"], truncated = true, true
					break
				}
				budget -= len(key) + 16
				if sensitiveKey(key) {
					out[j.redactLocked(key)] = redacted
				} else {
					out[j.redactLocked(key)] = sanitize(value[key], depth+1)
				}
			}
			return out
		case []any:
			out := make([]any, 0, min(len(value), 1000))
			for _, item := range value {
				if budget <= 0 || len(out) >= 1000 {
					out, truncated = append(out, "[内容已截断]"), true
					break
				}
				budget -= 8
				out = append(out, sanitize(item, depth+1))
			}
			return out
		default:
			budget -= 16
			return value
		}
	}
	data, err := json.Marshal(sanitize(value, 0))
	if err != nil || len(data) > maxContentBytes {
		// Escape-heavy content can grow after encoding. Keep valid JSON rather
		// than cutting through a string or a tool argument.
		return json.RawMessage(`{"unavailable":"content exceeded diagnostic limit"}`), true
	}
	return data, truncated
}

func boundString(value string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value + "…[内容已截断]"
}
