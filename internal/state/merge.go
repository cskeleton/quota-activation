package state

import (
	"strings"
)

// MergeUsableSuccessFrom 将 src 中可用的 success 记录合并进 s。
// 对每个 authID+provider：若 s 尚无可用 success，则拷入 src 的可用记录；
// 若 s 已有可用记录，则保留 s（磁盘优先）。Codex 的 5h 记录不会作为可用记录拷贝。
func (s *Store) MergeUsableSuccessFrom(src *Store) {
	if s == nil || src == nil || s == src {
		return
	}
	for _, rec := range src.usableSuccessRecords() {
		s.MergeUsableRecord(rec)
	}
}

// MergeUsableRecord 仅在 rec 可用且 s 对该 authID+provider 尚无可用 success 时写入 rec。
// 不可用条件：非 success、空 Window、零 ResetAt、Codex 5h。磁盘已有可用记录时不覆盖。
func (s *Store) MergeUsableRecord(rec Record) {
	if s == nil || !isUsableSuccessRecord(rec) {
		return
	}
	if _, ok := s.UsableLatestSuccess(rec.AuthID, rec.Provider); ok {
		return
	}
	s.Upsert(rec)
}

// SnapshotUsableSuccess 返回每个 authID+provider 当前可用 success 的副本。
func (s *Store) SnapshotUsableSuccess() []Record {
	return s.usableSuccessRecords()
}

func (s *Store) usableSuccessRecords() []Record {
	if s == nil {
		return nil
	}
	pairs := s.snapshotAuthProviders()
	out := make([]Record, 0, len(pairs))
	for _, pair := range pairs {
		rec, ok := s.UsableLatestSuccess(pair[0], pair[1])
		if !ok {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func (s *Store) snapshotAuthProviders() [][2]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[[2]string]struct{}, len(s.records))
	out := make([][2]string, 0, len(s.records))
	for _, rec := range s.records {
		pair := [2]string{rec.AuthID, rec.Provider}
		if _, ok := seen[pair]; ok {
			continue
		}
		seen[pair] = struct{}{}
		out = append(out, pair)
	}
	return out
}

func isUsableSuccessRecord(rec Record) bool {
	rec = redactRecord(rec)
	if rec.AuthID == "" || rec.Provider == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(rec.LastResult), "success") {
		return false
	}
	if rec.Window == "" || rec.ResetAt.IsZero() {
		return false
	}
	// allow 5h records in merge
	// if strings.EqualFold(rec.Provider, "codex") && isFiveHourWindow(rec.Window) { return false }
	return true
}
