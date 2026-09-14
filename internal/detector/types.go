package detector

import (
	"errors"
	"time"
)

var (
	// ErrMalformedQuota 表示 quota payload 不是合法 JSON。
	ErrMalformedQuota = errors.New("quota payload 畸形")
	// ErrUnknownQuota 表示 quota payload 缺少可安全激活的周期证据。
	ErrUnknownQuota = errors.New("quota 周期未知")
)

// Provider 标识 quota 输入来自的上游能力域。
type Provider string

const (
	// ProviderUnknown 表示未识别 provider，必须保持 unknown 且不得自动激活。
	ProviderUnknown Provider = "unknown"
	// ProviderCodex 表示 Codex/wham usage quota 输入。
	ProviderCodex Provider = "codex"
	// ProviderAntigravity 表示 Antigravity quota summary 输入。
	ProviderAntigravity Provider = "antigravity"
)

// Window 标识本次 quota 证据所属计量窗口。
type Window string

const (
	// WindowUnknown 表示无法识别计量窗口。
	WindowUnknown Window = "unknown"
	// WindowFiveHour 表示短周期 5 小时计量窗口（自动唤醒默认不使用该窗口判定）。
	WindowFiveHour Window = "5h"
	// WindowWeekly 表示周计量窗口（Antigravity 模型组）。
	WindowWeekly Window = "weekly"
	// WindowMonthly 表示月计量窗口（Codex Free 等长周期）。
	WindowMonthly Window = "monthly"
)

// ModelGroup 标识 Antigravity 独立计量的模型组。
type ModelGroup string

const (
	// ModelGroupNone 表示 provider 没有独立模型组维度。
	ModelGroupNone ModelGroup = ""
	// ModelGroupGemini 表示 Antigravity Gemini 模型组。
	ModelGroupGemini ModelGroup = "gemini"
	// ModelGroupClaudeGPT 表示 Antigravity Claude/GPT 模型组。
	ModelGroupClaudeGPT ModelGroup = "claude_gpt"
)

// Status 标识 detector 对本次 quota 观察的可用性结论。
type Status string

const (
	// StatusUnknown 表示没有得到可信 reset_at，必须保持不激活。
	StatusUnknown Status = "unknown"
	// StatusReady 表示已得到可用于周期去重的 reset_at。
	StatusReady Status = "ready"
)

// CycleKey 是按凭证、provider、窗口与 reset_at 去重的周期标识。
type CycleKey string

// String 返回周期键的稳定字符串表示。
func (k CycleKey) String() string {
	return string(k)
}

// ProbeInput 是 detector 解析 quota payload 所需的最小输入。
type ProbeInput struct {
	AuthID     string
	Provider   Provider
	Model      string
	ObservedAt time.Time
	Payload    []byte
	Enable5hWindow bool
}

// ProbeObservation 是从 quota payload 中解析出的脱敏周期证据。
type ProbeObservation struct {
	Provider     Provider
	ModelGroup   ModelGroup
	Window       Window
	ResetAt      time.Time
	Remaining    int64 // 当前窗口 remaining；仅当 HasRemaining 时有效
	HasRemaining bool  // payload 是否解析到 remaining（或可推断的耗尽）
}

// PreviousState 是上次成功激活时的周期状态，用于同 CycleKey 下的 remaining 恢复判定。
// CycleKey 仍不含 remaining。
// HasRemaining=false 且 CycleKey 已匹配时视为本周期已处理（幂等 skip），
// 仅当 HasRemaining 且 remaining 从耗尽恢复或回升时再唤醒。
type PreviousState struct {
	CycleKey     string
	Remaining    int64
	HasRemaining bool
}

// Decision 是一次周期判定的安全输出。
type Decision struct {
	Status      Status
	Activate    bool
	CycleKey    CycleKey
	Observation ProbeObservation
	Reason      string
}

type quotaWindow struct {
	ResetAt            any `json:"reset_at"`
	ResetAfterSeconds  any `json:"reset_after_seconds"`
	LimitWindowSeconds any `json:"limit_window_seconds"`
	Name               any `json:"name"`
	Type               any `json:"type"`
	Category           any `json:"category"`
	Label              any `json:"label"`
	Bucket             any `json:"bucket"`
	Scope              any `json:"scope"`
	ResetTime          any `json:"resetTime"`
	Remaining          any `json:"remaining"`
	UsedPercent        any `json:"used_percent"`
}

type parsedCycle struct {
	provider     Provider
	modelGroup   ModelGroup
	window       Window
	resetAt      time.Time
	remaining    int64
	hasRemaining bool
}
