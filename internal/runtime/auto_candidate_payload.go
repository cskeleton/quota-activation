package runtime

import (
	"context"
	"strings"
	"time"

	"quota-activation/internal/detector"
	"quota-activation/internal/host"
	"quota-activation/internal/planclaim"
	"quota-activation/internal/quotapayload"
)

// keepTrueAutoPayload 决定是否采用 runtime/file 真源。Codex 的 5h / 无法 Evaluate 视为缺失。
func keepTrueAutoPayload(provider detector.Provider, payload []byte, observedAt time.Time, enable5hWindow bool) bool {
	if provider != detector.ProviderCodex {
		return true
	}
	decision, err := detector.Evaluate(detector.ProbeInput{
		AuthID:     "auto-true-payload",
		Provider:   detector.ProviderCodex,
		ObservedAt: observedAt,
		Payload:    payload,
		Enable5hWindow: enable5hWindow,
	}, "")
	if err != nil {
		return false
	}
	if enable5hWindow {
		return true
	}
	return decision.Observation.Window != detector.WindowFiveHour
}

func syntheticCodexPlanPayload(file host.AuthFile, model string, observedAt time.Time, hostClient host.Client) ([]byte, bool) {
	plan := planclaim.FromAuthJSON(file.Data)
	if plan == planclaim.TypeUnknown {
		if physical := physicalAuthJSON(hostClient, file); len(physical) > 0 {
			plan = planclaim.FromAuthJSON(physical)
		}
	}
	name, limitSeconds, resetAt, ok := quotapayload.CodexWindowForPlan(plan, observedAt)
	if !ok {
		return nil, false
	}
	return marshalAutoQuotaPayload(detector.ProviderCodex, model, name, limitSeconds, resetAt)
}

func physicalAuthJSON(hostClient host.Client, file host.AuthFile) []byte {
	if hostClient == nil {
		return nil
	}
	seen := make(map[string]struct{}, 3)
	for _, key := range []string{file.AuthIndex, file.Name, file.ID} {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		physical, err := hostClient.GetAuthFile(context.Background(), key)
		if err != nil || len(physical.Data) == 0 {
			continue
		}
		return physical.Data
	}
	return nil
}

func autoQuotaPayload(file host.AuthFile, _ autoAuthFileDocument) ([]byte, bool) {
	return quotapayload.TrueFromFile(file)
}

func (d autoAuthFileDocument) availableModels() []string {
	models := append([]string(nil), d.AvailableModels...)
	models = append(models, d.AvailableModelsUpper...)
	models = append(models, d.Models...)
	models = append(models, d.RecentModels...)
	return uniqueAutoModels(models)
}

func autoRuntimeModels(file host.AuthFile) []string {
	models := append([]string(nil), file.Models...)
	models = append(models, file.RecentModels...)
	models = append(models, autoStringSliceFromMap(file.Metadata, "available_models")...)
	models = append(models, autoStringSliceFromMap(file.Metadata, "availableModels")...)
	models = append(models, autoStringSliceFromMap(file.Metadata, "models")...)
	models = append(models, autoStringSliceFromMap(file.Attributes, "available_models")...)
	models = append(models, autoStringSliceFromMap(file.Attributes, "availableModels")...)
	models = append(models, autoStringSliceFromMap(file.Attributes, "models")...)
	return uniqueAutoModels(models)
}

func autoStringSliceFromMap(values map[string]any, key string) []string {
	if len(values) == 0 {
		return nil
	}
	switch raw := values[key].(type) {
	case []string:
		return raw
	case []any:
		items := make([]string, 0, len(raw))
		for _, item := range raw {
			if value, ok := item.(string); ok {
				items = append(items, value)
			}
		}
		return items
	case string:
		return strings.Split(raw, ",")
	default:
		return nil
	}
}

func uniqueAutoModels(models []string) []string {
	choices := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		choices = append(choices, trimmed)
	}
	return choices
}

func hasAutoModel(models []string, target string) bool {
	for _, model := range models {
		if strings.TrimSpace(model) == target {
			return true
		}
	}
	return false
}
