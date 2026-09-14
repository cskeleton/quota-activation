package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// SchemaVersion 是当前 quota activation 状态文件结构版本。
	SchemaVersion = 1
	// RedactedValue 是状态落盘时替换敏感内容的固定占位符。
	RedactedValue = "[REDACTED]"
)

var (
	// ErrCorruptState 表示状态文件不是可安全读取的 JSON 文档。
	ErrCorruptState = errors.New("state: corrupt")
	sensitivePair   = regexp.MustCompile(`(?i)(authorization|bearer|token|api_key|apikey|access_token|accesstoken|secret)\s*[:=]\s*[^\s,;}\]"]+`)
	sensitiveWord   = regexp.MustCompile(`(?i)authorization|bearer|token|api_key|apikey|access_token|accesstoken|secret`)
)

// RecordKey 唯一定位一个 auth/provider/window/cycle 状态记录。
type RecordKey struct {
	AuthID   string
	Provider string
	Window   string
	CycleKey string
}

// Record 是 quota activation 调度和管理接口可复用的脱敏状态记录。
// Remaining/HasRemaining 为可选字段（omitempty），旧 schema 文件无此字段时 HasRemaining=false。
type Record struct {
	AuthID       string    `json:"auth_id"`
	Provider     string    `json:"provider"`
	Window       string    `json:"window"`
	CycleKey     string    `json:"cycle_key"`
	ObservedAt   time.Time `json:"observed_at"`
	ResetAt      time.Time `json:"reset_at"`
	LastResult   string    `json:"last_result"`
	LastError    string    `json:"last_error"`
	Remaining    int64     `json:"remaining,omitempty"`
	HasRemaining bool      `json:"has_remaining,omitempty"`
}

// Document 是状态文件的 JSON 顶层结构。
type Document struct {
	SchemaVersion int      `json:"schema_version"`
	Records       []Record `json:"records"`
}

// Store 持有一个状态文件路径和内存中的记录快照。
type Store struct {
	mu      sync.RWMutex
	path    string
	records map[RecordKey]Record
}

// NewStore 返回空状态库，后续 SaveAtomic 写入指定路径。
func NewStore(path string) *Store {
	return &Store{path: path, records: map[RecordKey]Record{}}
}

// Load 从 path 读取状态文件；文件不存在时返回空状态库。
func Load(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("load state context: %w", err)
	}
	store := NewStore(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return store, nil
	}
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode state %s: %w", path, errors.Join(ErrCorruptState, err))
	}
	if document.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("decode state %s: schema_version %d: %w", path, document.SchemaVersion, ErrCorruptState)
	}
	for _, record := range document.Records {
		redacted := redactRecord(record)
		store.records[redacted.key()] = redacted
	}
	return store, nil
}

// Upsert 写入或替换一条记录，保存前会再次脱敏。
func (s *Store) Upsert(record Record) {
	redacted := redactRecord(record)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[redacted.key()] = redacted
}

// Record 返回指定状态记录的副本。
func (s *Store) Record(key RecordKey) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[redactKey(key)]
	return record, ok
}

// LatestSuccess 返回同 auth_id+provider 下最近一次 success 记录（按 ObservedAt，其次 ResetAt）。
// 供自动唤醒在无真实 quota_payload 时推断计量窗口，以及 Evaluate previous remaining 恢复判定。
func (s *Store) LatestSuccess(authID string, provider string) (Record, bool) {
	return s.pickLatestSuccess(authID, provider, false)
}

// UsableLatestSuccess 返回同 auth_id+provider 下可用于推断/硬闸的最近一次 success。
// 选择规则与 LatestSuccess 相同：LastResult=success、非空 Window、非零 ResetAt，按 ObservedAt 再 ResetAt 取最新。
// Codex 会跳过规范化后等于 5h 的窗口；Antigravity 及其它 provider 保持 LatestSuccess 语义（5h 仍可返回）。
func (s *Store) UsableLatestSuccess(authID string, provider string) (Record, bool) {
	return s.pickLatestSuccess(authID, provider, false)
}

func (s *Store) pickLatestSuccess(authID string, provider string, skipCodexFiveHour bool) (Record, bool) {
	if s == nil {
		return Record{}, false
	}
	authID = Redact(strings.TrimSpace(authID))
	provider = Redact(strings.TrimSpace(provider))
	if authID == "" || provider == "" {
		return Record{}, false
	}
	skipFiveHour := skipCodexFiveHour && strings.EqualFold(provider, "codex")
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best Record
	found := false
	for _, record := range s.records {
		if record.AuthID != authID || record.Provider != provider {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(record.LastResult), "success") {
			continue
		}
		if record.Window == "" || record.ResetAt.IsZero() {
			continue
		}
		if skipFiveHour && isFiveHourWindow(record.Window) {
			continue
		}
		if !found || record.ObservedAt.After(best.ObservedAt) || (record.ObservedAt.Equal(best.ObservedAt) && record.ResetAt.After(best.ResetAt)) {
			best = record
			found = true
		}
	}
	return best, found
}

func isFiveHourWindow(window string) bool {
	normalized := strings.ToLower(strings.TrimSpace(window))
	normalized = strings.ReplaceAll(normalized, " ", "")
	switch normalized {
	case "5h", "fivehour", "5hour":
		return true
	default:
		return false
	}
}

// LatestSuccessCycle 返回最近一次 success 的 cycle key 与 remaining 快照（只读 helper）。
// 无 success 时 ok=false。HasRemaining 兼容 omitempty：若标记为 false 但 Remaining!=0，
// 仍视为已写入 remaining（旧记录/序列化丢失 has_remaining 时避免恢复路径失效）。
func (s *Store) LatestSuccessCycle(authID string, provider string) (cycleKey string, remaining int64, hasRemaining bool, ok bool) {
	record, found := s.LatestSuccess(authID, provider)
	if !found {
		return "", 0, false, false
	}
	hasRemaining = record.HasRemaining
	if !hasRemaining && record.Remaining != 0 {
		hasRemaining = true
	}
	return record.CycleKey, record.Remaining, hasRemaining, true
}

// SaveAtomic 使用同目录临时文件加 rename 原子写入状态 JSON。
func (s *Store) SaveAtomic(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save state context: %w", err)
	}
	document := s.document()
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create state temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			err = errors.Join(err, os.Remove(tmpName))
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write state temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod state temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state temp: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save state context: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("rename state temp: %w", err)
	}
	cleanup = false
	return nil
}

func (s *Store) document() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, redactRecord(record))
	}
	return Document{SchemaVersion: SchemaVersion, Records: records}
}

func (r Record) key() RecordKey {
	return RecordKey{AuthID: r.AuthID, Provider: r.Provider, Window: r.Window, CycleKey: r.CycleKey}
}

func redactKey(key RecordKey) RecordKey {
	return RecordKey{
		AuthID:   Redact(key.AuthID),
		Provider: Redact(key.Provider),
		Window:   Redact(key.Window),
		CycleKey: Redact(key.CycleKey),
	}
}

func redactRecord(record Record) Record {
	return Record{
		AuthID:       Redact(record.AuthID),
		Provider:     Redact(record.Provider),
		Window:       Redact(record.Window),
		CycleKey:     Redact(record.CycleKey),
		ObservedAt:   record.ObservedAt.UTC(),
		ResetAt:      record.ResetAt.UTC(),
		LastResult:   Redact(record.LastResult),
		LastError:    Redact(record.LastError),
		Remaining:    record.Remaining,
		HasRemaining: record.HasRemaining,
	}
}

// Redact 清洗状态字段中可能携带的敏感凭证片段。
func Redact(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	redacted := sensitivePair.ReplaceAllString(trimmed, RedactedValue)
	if sensitiveWord.MatchString(redacted) {
		return RedactedValue
	}
	return redacted
}
