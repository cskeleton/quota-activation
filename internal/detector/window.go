package detector

import "strings"

func classifyWindow(window quotaWindow) Window {
	if seconds, ok := toInt64(window.LimitWindowSeconds); ok {
		switch {
		case seconds == 3*60*60 || seconds == 5*60*60:
			return WindowFiveHour
		case seconds == 7*24*60*60:
			return WindowWeekly
		case seconds >= 28*24*60*60 && seconds <= 31*24*60*60:
			return WindowMonthly
		}
	}
	for _, text := range windowTexts(window) {
		if strings.Contains(text, "monthly") || strings.Contains(text, "month") || strings.Contains(text, "30d") || strings.Contains(text, "30 day") {
			return WindowMonthly
		}
		if strings.Contains(text, "weekly") || strings.Contains(text, "week") || strings.Contains(text, "7d") {
			return WindowWeekly
		}
		if strings.Contains(text, "5h") || strings.Contains(text, "5 hour") || strings.Contains(text, "5 hr") || strings.Contains(text, "3h") {
			return WindowFiveHour
		}
	}
	return WindowUnknown
}

// windowPreference 用于多窗口时优先长周期（自动唤醒不关心 5h）。
func windowPreference(window Window, enable5hWindow bool) int {
	if enable5hWindow {
		switch window {
		case WindowFiveHour:
			return 4
		case WindowMonthly:
			return 3
		case WindowWeekly:
			return 2
		default:
			return 0
		}
	}
	switch window {
	case WindowMonthly:
		return 3
	case WindowWeekly:
		return 2
	case WindowFiveHour:
		return 1
	default:
		return 0
	}
}

func windowTexts(window quotaWindow) []string {
	fields := []any{window.Name, window.Type, window.Category, window.Label, window.Bucket, window.Scope}
	texts := make([]string, 0, len(fields))
	for _, field := range fields {
		text, ok := field.(string)
		if !ok {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(text))
		normalized = strings.ReplaceAll(normalized, "_", " ")
		normalized = strings.ReplaceAll(normalized, "-", " ")
		if normalized != "" {
			texts = append(texts, normalized)
		}
	}
	return texts
}
