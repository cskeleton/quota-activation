package detector

import (
	"fmt"
	"strings"
)

type antigravityQuota struct {
	Models  map[string]antigravityModel `json:"models"`
	Groups  []antigravityGroup          `json:"groups"`
	Buckets []antigravityBucket         `json:"buckets"`
}

type antigravityModel struct {
	ModelProvider string          `json:"modelProvider"`
	QuotaInfo     antigravityInfo `json:"quotaInfo"`
}

type antigravityInfo struct {
	ResetTime any           `json:"resetTime"`
	Windows   []quotaWindow `json:"windows"`
}

type antigravityGroup struct {
	DisplayName string              `json:"displayName"`
	Description string              `json:"description"`
	Buckets     []antigravityBucket `json:"buckets"`
}

type antigravityBucket struct {
	ModelID   string `json:"modelId"`
	Window    string `json:"window"`
	ResetTime any    `json:"resetTime"`
}

func parseAntigravity(payload []byte, model string, enable5hWindow bool) (parsedCycle, error) {
	modelGroup, ok := inferModelGroup(model)
	if !ok {
		return parsedCycle{}, fmt.Errorf("antigravity model group: %w", ErrUnknownQuota)
	}
	var quota antigravityQuota
	if err := decodePayload(payload, &quota); err != nil {
		return parsedCycle{}, err
	}
	if cycle, ok := parseAntigravityModels(quota.Models, modelGroup, enable5hWindow); ok {
		return cycle, nil
	}
	if cycle, ok := parseAntigravityBuckets(quota.Buckets, modelGroup, enable5hWindow); ok {
		return cycle, nil
	}
	if cycle, ok := parseAntigravityGroups(quota.Groups, modelGroup, enable5hWindow); ok {
		return cycle, nil
	}
	return parsedCycle{}, fmt.Errorf("antigravity reset_at: %w", ErrUnknownQuota)
}

func parseAntigravityModels(models map[string]antigravityModel, group ModelGroup, enable5hWindow bool) (parsedCycle, bool) {
	for modelID, item := range models {
		if !belongsToModelGroup(modelID+" "+item.ModelProvider, group) {
			continue
		}
		windows := item.QuotaInfo.Windows
		if len(windows) == 0 {
			windows = []quotaWindow{{ResetTime: item.QuotaInfo.ResetTime}}
		}
		if cycle, ok := firstAntigravityWindow(windows, group, enable5hWindow); ok {
			return cycle, true
		}
	}
	return parsedCycle{}, false
}

func parseAntigravityBuckets(buckets []antigravityBucket, group ModelGroup, enable5hWindow bool) (parsedCycle, bool) {
	for _, bucket := range buckets {
		if !belongsToModelGroup(bucket.ModelID, group) {
			continue
		}
		window := quotaWindow{ResetTime: bucket.ResetTime, Name: bucket.Window}
		if cycle, ok := antigravityWindow(window, group); ok {
			return cycle, true
		}
	}
	return parsedCycle{}, false
}

func parseAntigravityGroups(groups []antigravityGroup, modelGroup ModelGroup, enable5hWindow bool) (parsedCycle, bool) {
	for _, group := range groups {
		if !belongsToModelGroup(group.DisplayName+" "+group.Description, modelGroup) {
			continue
		}
		if cycle, ok := parseAntigravityBuckets(group.Buckets, modelGroup, enable5hWindow); ok {
			return cycle, true
		}
	}
	return parsedCycle{}, false
}

	func firstAntigravityWindow(windows []quotaWindow, group ModelGroup, enable5hWindow bool) (parsedCycle, bool) {
	return selectAntigravityWindow(windows, group, WindowUnknown, enable5hWindow)
}


	// selectAntigravityWindow 以真源窗口集合为唯一真相。
	// 存在比 previous 更长的窗时选更长（修污染 5h）；否则 previous 同名且不被支配时保持稳定。
	func selectAntigravityWindow(windows []quotaWindow, group ModelGroup, previousWindow Window, enable5hWindow bool) (parsedCycle, bool) {
		var longest parsedCycle
		longestRank := -1
		var matchedPrevious parsedCycle
		hasMatchedPrevious := false
		for _, window := range windows {
			cycle, ok := antigravityWindow(window, group)
			if !ok {
				continue
			}
			rank := windowPreference(cycle.window, enable5hWindow)
			if rank > longestRank {
				longest = cycle
				longestRank = rank
			}
			if previousWindow != WindowUnknown && cycle.window == previousWindow {
				matchedPrevious = cycle
				hasMatchedPrevious = true
			}
		}
		if longestRank < 0 {
			return parsedCycle{}, false
		}
		// 真源有更长窗（weekly/monthly vs 5h）→ 选更长，禁止被污染 previous 锁死。
		if longestRank > windowPreference(previousWindow, enable5hWindow) {
			return longest, true
		}
		if hasMatchedPrevious {
			return matchedPrevious, true
		}
		return longest, true
	}

func antigravityWindow(window quotaWindow, group ModelGroup) (parsedCycle, bool) {
	resetAt, ok := parseAnyTime(window.ResetTime)
	if !ok {
		return parsedCycle{}, false
	}
	cycleWindow := classifyWindow(window)
	if cycleWindow == WindowUnknown {
		return parsedCycle{}, false
	}
	return parsedCycle{provider: ProviderAntigravity, modelGroup: group, window: cycleWindow, resetAt: resetAt}, true
}

func inferModelGroup(model string) (ModelGroup, bool) {
	text := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(text, "claude") || strings.Contains(text, "gpt") || strings.Contains(text, "openai") {
		return ModelGroupClaudeGPT, true
	}
	if strings.Contains(text, "gemini") {
		return ModelGroupGemini, true
	}
	return ModelGroupNone, false
}

func belongsToModelGroup(text string, group ModelGroup) bool {
	groupText := strings.ToLower(strings.TrimSpace(text))
	switch group {
	case ModelGroupClaudeGPT:
		return strings.Contains(groupText, "claude") || strings.Contains(groupText, "gpt") || strings.Contains(groupText, "openai")
	case ModelGroupGemini:
		return strings.Contains(groupText, "gemini") && !strings.Contains(groupText, "claude") && !strings.Contains(groupText, "gpt")
	case ModelGroupNone:
		return false
	default:
		return false
	}
}
