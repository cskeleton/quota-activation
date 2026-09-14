package detector

import (
	"fmt"
	"time"
)

type codexQuota struct {
	ResetAt            any            `json:"reset_at"`
	ResetAfterSeconds  any            `json:"reset_after_seconds"`
	LimitWindowSeconds any            `json:"limit_window_seconds"`
	Name               any            `json:"name"`
	Type               any            `json:"type"`
	Category           any            `json:"category"`
	Label              any            `json:"label"`
	Bucket             any            `json:"bucket"`
	Scope              any            `json:"scope"`
	Remaining          any            `json:"remaining"`
	UsedPercent        any            `json:"used_percent"`
	RateLimit          codexRateLimit `json:"rate_limit"`
}

type codexRateLimit struct {
	PrimaryWindow   quotaWindow `json:"primary_window"`
	SecondaryWindow quotaWindow `json:"secondary_window"`
}

func parseCodex(payload []byte, observedAt time.Time, enable5hWindow bool) (parsedCycle, error) {
	var quota codexQuota
	if err := decodePayload(payload, &quota); err != nil {
		return parsedCycle{}, err
	}
	windows := []quotaWindow{
		{
			ResetAt:            quota.ResetAt,
			ResetAfterSeconds:  quota.ResetAfterSeconds,
			LimitWindowSeconds: quota.LimitWindowSeconds,
			Name:               quota.Name,
			Type:               quota.Type,
			Category:           quota.Category,
			Label:              quota.Label,
			Bucket:             quota.Bucket,
			Scope:              quota.Scope,
			Remaining:          quota.Remaining,
			UsedPercent:        quota.UsedPercent,
		},
		quota.RateLimit.PrimaryWindow,
		quota.RateLimit.SecondaryWindow,
	}
	// 多窗口时优先 monthly > weekly > 5h，避免 Free 月额度被 5h 窗口抢先匹配。
	var best parsedCycle
	bestRank := -1
	for _, window := range windows {
		resetAt, ok := windowResetAt(observedAt, window)
		if !ok {
			continue
		}
		cycleWindow := classifyWindow(window)
		if cycleWindow == WindowUnknown || (!enable5hWindow && cycleWindow == WindowFiveHour) {
			continue
		}
		rank := windowPreference(cycleWindow, enable5hWindow)
		if rank > bestRank {
			remaining, hasRemaining := extractRemaining(window)
			best = parsedCycle{
				provider:     ProviderCodex,
				window:       cycleWindow,
				resetAt:      resetAt,
				remaining:    remaining,
				hasRemaining: hasRemaining,
			}
			bestRank = rank
		}
	}
	if bestRank < 0 {
		return parsedCycle{}, fmt.Errorf("codex reset_at: %w", ErrUnknownQuota)
	}
	return best, nil
}

// extractRemaining 从窗口解析 remaining；优先 remaining 字段，否则 used_percent>=100 视为耗尽。
func extractRemaining(window quotaWindow) (int64, bool) {
	if remaining, ok := toInt64(window.Remaining); ok {
		return remaining, true
	}
	if pct, ok := toFloat64(window.UsedPercent); ok && pct >= 100 {
		return 0, true
	}
	return 0, false
}
