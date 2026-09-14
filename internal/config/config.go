package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalidConfig 标识插件配置解析或校验失败。
	ErrInvalidConfig = errors.New("配置无效")
)

// ActivationTransport 表示配额唤醒请求的宿主传输方式。
type ActivationTransport string

const (
	// ActivationTransportDirectHTTP 通过 host.http.do 直接发送唤醒请求。
	ActivationTransportDirectHTTP ActivationTransport = "direct_http"
	// ActivationTransportSchedulerBoost 通过调度器提升优先级后走宿主模型路径。
	ActivationTransportSchedulerBoost ActivationTransport = "scheduler_boost"
)

// Config 是 quota-activation 插件配置解析后的稳定形态。
type Config struct {
	AutoActivate             bool
	ScanInterval             time.Duration
	ActivationRequestTimeout time.Duration
	MaxConcurrency           int
	ActivationPrompt         string
	StatePath                string
	EnableBeforeActivation   bool
	Enable5hWindow           bool
	// ActivationTransport 默认 direct_http；允许 scheduler_boost。
	ActivationTransport ActivationTransport
	// SchedulerBoostFallback：direct_http 遇传输/宿主类失败时是否回退 legacy scheduler_boost；默认 true。
	// 业务/严格成功判定失败禁止回退（不得伪装成功）。
	SchedulerBoostFallback bool
	ActivationModels       ActivationModels
}

// ActivationModels 定义不同 provider 唤醒请求必须显式配置的模型。
type ActivationModels struct {
	Codex       CodexActivationModels
	Antigravity AntigravityActivationModels
}

// CodexActivationModels 定义 Codex 自动唤醒模型。
type CodexActivationModels struct {
	Models string
}

// AntigravityActivationModels 定义 Antigravity 不同模型组的唤醒模型。
type AntigravityActivationModels struct {
	ModelsGroup string
	Models      string
}

type rawConfig struct {
	Enabled                  *bool                `json:"enabled"`
	Priority                 *int                 `json:"priority"`
	AutoActivate             *bool                `json:"auto_activate"`
	ScanInterval             *string              `json:"scan_interval"`
	ActivationRequestTimeout *string              `json:"activation_request_timeout"`
	MaxConcurrency           *int                 `json:"max_concurrency"`
	ActivationPrompt         *string              `json:"activation_prompt"`
	StatePath                *string              `json:"state_path"`
	EnableBeforeActivation   *bool                `json:"enable_before_activation"`
	Enable5hWindow           *bool                `json:"enable_5h_window"`
	ActivationTransport      *string              `json:"activation_transport"`
	SchedulerBoostFallback   *bool                `json:"scheduler_boost_fallback"`
	ActivationModels         *rawActivationModels `json:"activation_models"`
}

type rawActivationModels struct {
	Codex       *rawCodexActivationModels  `json:"codex"`
	Antigravity *rawAntigravityModelsGroup `json:"antigravity"`
}

type rawCodexActivationModels struct {
	Models *string `json:"models"`
}

type rawAntigravityModelsGroup struct {
	ModelsGroup     *string `json:"models_group"`
	Models          *string `json:"models"`
	Gemini          *string `json:"gemini"`
	ClaudeGPT       *string `json:"claude_gpt"`
	EnableGemini    *bool   `json:"enable_gemini"`
	EnableClaudeGPT *bool   `json:"enable_claude_gpt"`
}

// Default 返回稳定默认配置。
// AutoActivate 默认 false（README 强制：手动唤醒为默认）；EnableBeforeActivation 默认 true，
// 使 disabled Free 可进入 auto 候选并由 activator 启用；Codex 默认模型 gpt-5-mini。
func Default() Config {
	return Config{
		AutoActivate:             false,
		ScanInterval:             30 * time.Minute,
		ActivationRequestTimeout: time.Minute,
		MaxConcurrency:           1,
		ActivationPrompt:         "quota activation ping",
		StatePath:                "quota-activation/state.json",
		EnableBeforeActivation:   true,
		ActivationTransport:      ActivationTransportDirectHTTP,
		SchedulerBoostFallback:   true,
		ActivationModels: ActivationModels{
			Codex: CodexActivationModels{Models: "gpt-5-mini"},
		},
	}
}

// UnmarshalJSON 兼容旧版 activation_models.codex 标量与新版对象结构。
func (raw *rawCodexActivationModels) UnmarshalJSON(data []byte) error {
	var legacy string
	if err := json.Unmarshal(data, &legacy); err == nil {
		trimmed := strings.TrimSpace(legacy)
		raw.Models = &trimmed
		return nil
	}
	type rawCodexAlias rawCodexActivationModels
	var decoded rawCodexAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*raw = rawCodexActivationModels(decoded)
	return nil
}

// Parse 将 CPA 传入的插件配置 JSON 字节解析为已校验配置。
func Parse(data []byte) (Config, error) {
	raw, err := decodeRaw(data)
	if err != nil {
		return Config{}, fmt.Errorf("解析配置: %w", err)
	}
	cfg, err := raw.apply(Default())
	if err != nil {
		return Config{}, fmt.Errorf("校验配置: %w", err)
	}
	return cfg, nil
}

func decodeRaw(data []byte) (rawConfig, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return rawConfig{}, nil
	}
	var raw rawConfig
	if trimmed[0] == '{' {
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		if err := decoder.Decode(&raw); err != nil {
			return rawConfig{}, invalid("config", fmt.Sprintf("必须匹配配置结构: %v", err))
		}
		return raw, nil
	}

	yamlMap, err := parseYAMLMap(trimmed)
	if err != nil {
		return rawConfig{}, err
	}
	encoded, err := json.Marshal(yamlMap)
	if err != nil {
		return rawConfig{}, invalid("config", "YAML 编码错误")
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	if err := decoder.Decode(&raw); err != nil {
		return rawConfig{}, invalid("config", fmt.Sprintf("YAML 不匹配配置结构: %v", err))
	}
	return raw, nil
}

func (raw rawConfig) apply(cfg Config) (Config, error) {
	if raw.AutoActivate != nil {
		cfg.AutoActivate = *raw.AutoActivate
	}
	if raw.ActivationPrompt != nil && strings.TrimSpace(*raw.ActivationPrompt) != "" {
		cfg.ActivationPrompt = *raw.ActivationPrompt
	}
	if raw.StatePath != nil {
		cfg.StatePath = *raw.StatePath
	}
	if raw.EnableBeforeActivation != nil {
		cfg.EnableBeforeActivation = *raw.EnableBeforeActivation
	}
	if raw.Enable5hWindow != nil {
		cfg.Enable5hWindow = *raw.Enable5hWindow
	}
	if raw.SchedulerBoostFallback != nil {
		cfg.SchedulerBoostFallback = *raw.SchedulerBoostFallback
	}
	if raw.ActivationTransport != nil {
		transport, err := parseActivationTransport(*raw.ActivationTransport)
		if err != nil {
			return Config{}, err
		}
		cfg.ActivationTransport = transport
	}
	if raw.MaxConcurrency != nil {
		cfg.MaxConcurrency = *raw.MaxConcurrency
	}
	if cfg.MaxConcurrency < 1 {
		return Config{}, invalid("max_concurrency", "必须大于等于 1")
	}
	durationFields := []struct {
		name        string
		raw         *string
		target      *time.Duration
		missingUnit string
	}{
		{"scan_interval", raw.ScanInterval, &cfg.ScanInterval, "m"},
		{"activation_request_timeout", raw.ActivationRequestTimeout, &cfg.ActivationRequestTimeout, "s"},
	}
	for _, field := range durationFields {
		if field.raw == nil {
			continue
		}
		if strings.TrimSpace(*field.raw) == "" {
			continue
		}
		parsed, err := parseDuration(field.name, *field.raw, field.missingUnit)
		if err != nil {
			return Config{}, err
		}
		*field.target = parsed
	}
	models, err := raw.ActivationModels.parse(cfg.ActivationModels)
	if err != nil {
		return Config{}, err
	}
	cfg.ActivationModels = models
	return cfg, nil
}

func (raw *rawActivationModels) parse(base ActivationModels) (ActivationModels, error) {
	models := base
	if raw != nil && raw.Codex != nil && raw.Codex.Models != nil {
		models.Codex.Models = strings.TrimSpace(*raw.Codex.Models)
	}
	if raw != nil && raw.Antigravity != nil {
		var err error
		models.Antigravity, err = raw.Antigravity.parse(models.Antigravity)
		if err != nil {
			return ActivationModels{}, err
		}
	}
	return models, nil
}

func (raw rawAntigravityModelsGroup) parse(base AntigravityActivationModels) (AntigravityActivationModels, error) {
	models := base
	if raw.ModelsGroup != nil || raw.Models != nil {
		if raw.ModelsGroup != nil {
			models.ModelsGroup = strings.TrimSpace(*raw.ModelsGroup)
		}
		if raw.Models != nil {
			models.Models = strings.TrimSpace(*raw.Models)
		}
		return validateAntigravityModels(models)
	}

	legacy := legacyAntigravityModels{}
	if raw.Gemini != nil {
		legacy.gemini = strings.TrimSpace(*raw.Gemini)
	}
	if raw.ClaudeGPT != nil {
		legacy.claudeGPT = strings.TrimSpace(*raw.ClaudeGPT)
	}
	if raw.EnableGemini != nil {
		legacy.enableGemini = *raw.EnableGemini
	}
	if raw.EnableClaudeGPT != nil {
		legacy.enableClaudeGPT = *raw.EnableClaudeGPT
	}
	if legacy.enableGemini && legacy.enableClaudeGPT {
		return AntigravityActivationModels{}, invalid("activation_models.antigravity", "Gemini 与 Claude/GPT 只能启用一种")
	}
	if legacy.enableGemini && legacy.gemini == "" {
		return AntigravityActivationModels{}, invalid("activation_models.antigravity.gemini", "启用 Gemini 组时必须填写")
	}
	if legacy.enableClaudeGPT && legacy.claudeGPT == "" {
		return AntigravityActivationModels{}, invalid("activation_models.antigravity.claude_gpt", "启用 Claude/GPT 组时必须填写")
	}
	if legacy.enableGemini {
		models.ModelsGroup = "gemini"
		models.Models = legacy.gemini
	}
	if legacy.enableClaudeGPT {
		models.ModelsGroup = "claude_gpt"
		models.Models = legacy.claudeGPT
	}
	return models, nil
}

type legacyAntigravityModels struct {
	gemini          string
	claudeGPT       string
	enableGemini    bool
	enableClaudeGPT bool
}

func validateAntigravityModels(models AntigravityActivationModels) (AntigravityActivationModels, error) {
	switch models.ModelsGroup {
	case "":
		if models.Models != "" {
			return AntigravityActivationModels{}, invalid("activation_models.antigravity.models_group", "填写模型名时必须选择模型组")
		}
	case "gemini", "claude_gpt":
		if models.Models == "" {
			return AntigravityActivationModels{}, invalid("activation_models.antigravity.models", "选择模型组时必须填写")
		}
	default:
		return AntigravityActivationModels{}, invalid("activation_models.antigravity.models_group", "必须是 gemini 或 claude_gpt")
	}
	return models, nil
}

func parseActivationTransport(raw string) (ActivationTransport, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ActivationTransportDirectHTTP, nil
	}
	switch ActivationTransport(value) {
	case ActivationTransportDirectHTTP, ActivationTransportSchedulerBoost:
		return ActivationTransport(value), nil
	default:
		return "", invalid("activation_transport", "必须是 direct_http 或 scheduler_boost")
	}
}

func parseDuration(field string, raw string, missingUnit string) (time.Duration, error) {
	text := strings.TrimSpace(raw)
	if missingUnit == "" {
		missingUnit = "m"
	}
	if _, err := time.ParseDuration(text); err != nil && strings.Contains(err.Error(), "missing unit") {
		text += missingUnit
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		unitHint := "分钟"
		if missingUnit == "s" {
			unitHint = "秒"
		}
		return 0, invalid(field, fmt.Sprintf("必须是可解析的时长字符串，纯数字按%s解析", unitHint))
	}
	if parsed <= 0 {
		return 0, invalid(field, "必须大于 0")
	}
	return parsed, nil
}

func invalid(field string, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidConfig, field, reason)
}
