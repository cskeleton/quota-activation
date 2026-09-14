package detector

import (
	"fmt"
	"strings"
	"time"
)

// Evaluate 解析 quota 输入并按 previousCycleKey 判断当前周期是否需要激活。
// 仅传入 cycle key 时无法识别「同 reset_at + remaining 恢复」；完整语义见 EvaluateWithPrevious。
func Evaluate(input ProbeInput, previousCycleKey string) (Decision, error) {
	return EvaluateWithPrevious(input, PreviousState{CycleKey: previousCycleKey})
}

// EvaluateWithPrevious 在 CycleKey 去重之外，支持 Codex Free remaining 耗尽→恢复的再唤醒。
//
// Activate = true when:
//   (A) computedCycleKey != previous.CycleKey, OR
//   (B) 同 CycleKey 且 current remaining>0，并满足其一：
//       - previous.HasRemaining && previous.Remaining<=0（明确耗尽后恢复）
//       - previous.HasRemaining && current remaining > previous remaining（额度回升）
//
// Activate = false when:
//   - 同 cycle 已 success 且 remaining 持续 >0 且未回升（幂等）
//   - 同 cycle 已 success 但 previous 无 remaining 快照（!HasRemaining）→ 视为已处理，避免反复激活
// CycleKey 故意不含 remaining，避免正常消耗破坏幂等。
func EvaluateWithPrevious(input ProbeInput, previous PreviousState) (Decision, error) {
	authID := strings.TrimSpace(input.AuthID)
	if authID == "" {
		return unknown("缺少凭证标识"), fmt.Errorf("auth id: %w", ErrUnknownQuota)
	}
	cycle, err := parseCycle(input)
	if err != nil {
		return unknown("额度周期未知"), err
	}
	key := buildCycleKey(authID, cycle)
	activate, reason := shouldActivate(key, cycle, previous)
	return Decision{
		Status:   StatusReady,
		Activate: activate,
		CycleKey: key,
		Observation: ProbeObservation{
			Provider:     cycle.provider,
			ModelGroup:   cycle.modelGroup,
			Window:       cycle.window,
			ResetAt:      cycle.resetAt,
			Remaining:    cycle.remaining,
			HasRemaining: cycle.hasRemaining,
		},
		Reason: reason,
	}, nil
}

// shouldActivate 实现周期去重与 remaining 恢复合同（不修改 CycleKey 组成）。
func shouldActivate(key CycleKey, cycle parsedCycle, previous PreviousState) (bool, string) {
	prevKey := strings.TrimSpace(previous.CycleKey)
	if key.String() != prevKey {
		return true, "额度周期可用"
	}
	// 同 CycleKey 且本周期已有 success：仅在能证明 remaining 从耗尽恢复或回升时再唤醒。
	if cycle.hasRemaining && cycle.remaining > 0 {
		if !previous.HasRemaining {
			// 已成功但 store 无 remaining 快照 → 幂等跳过，禁止一律 Activate 导致反复唤醒。
			return false, "额度周期已处理"
		}
		if previous.Remaining <= 0 {
			return true, "额度已恢复"
		}
		if cycle.remaining > previous.Remaining {
			return true, "额度已恢复"
		}
		return false, "额度周期已处理"
	}
	return false, "额度周期已处理"
}

func parseCycle(input ProbeInput) (parsedCycle, error) {
	switch input.Provider {
	case ProviderCodex:
		return parseCodex(input.Payload, input.ObservedAt, input.Enable5hWindow)
	case ProviderAntigravity:
		return parseAntigravity(input.Payload, input.Model, input.Enable5hWindow)
	case ProviderUnknown:
		return parsedCycle{}, fmt.Errorf("provider unknown: %w", ErrUnknownQuota)
	default:
		return parsedCycle{}, fmt.Errorf("provider unsupported: %w", ErrUnknownQuota)
	}
}

func buildCycleKey(authID string, cycle parsedCycle) CycleKey {
	provider := string(cycle.provider)
	if cycle.modelGroup != ModelGroupNone {
		provider += "/" + string(cycle.modelGroup)
	}
	return CycleKey(authID + ":" + provider + ":" + string(cycle.window) + ":" + cycle.resetAt.UTC().Format(time.RFC3339))
}

func unknown(reason string) Decision {
	return Decision{Status: StatusUnknown, Reason: reason}
}
