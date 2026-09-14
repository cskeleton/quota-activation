package runtime

import (
	"context"
	"strings"
	"sync"
	"time"

	"quota-activation/internal/activator"
	"quota-activation/internal/config"
	"quota-activation/internal/detector"
	"quota-activation/internal/host"
	"quota-activation/internal/state"
)

const maxSchedulerMissCooldown = 5 * time.Minute
const schedulerMissCooldownReason = "调度未选中目标凭证（已冷却）"
const schedulerMissErrorMessage = "调度器未选中目标凭证"
const hostExecutorUnavailableCooldownReason = "宿主模型执行器不可用（已冷却）"
const hostExecutorUnavailableMessage = "宿主模型执行器不可用（宿主未就绪或回调失败，非凭证失效）"

// autoScanIntervalSlack 允许微早触发仍认领本轮，避免 timer 抖动静默丢 tick。
const autoScanIntervalSlack = 2 * time.Second

// autoScanStartupDelay 首轮 scan 前等待，给宿主 auth 索引就绪时间。
const autoScanStartupDelay = 3 * time.Second

type autoScanSnapshot struct {
	host                       host.Client
	activator                  *activator.Activator
	store                      *state.Store
	config                     config.Config
	now                        func() time.Time
	shouldSkipActivateCooldown func(authID string) bool
	noteActivateCooldown       func(authID, reason string)
	cooldownSkipReason         func(authID string) string
	rememberUsable             func(state.Record)
}

type autoCandidate struct {
	authID   string
	provider detector.Provider
	model    string
	disabled bool
	payload  []byte
}

func (r *Runtime) runAutoScanLoop(ctx context.Context) {
	if delay := r.firstScanDelay(); delay > 0 {
		if !r.sleepCtx(ctx, delay) {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		start := r.now()
		r.scanAutoCandidates(ctx)
		if err := ctx.Err(); err != nil {
			return
		}
		interval := r.autoScannerInterval()
		elapsed := r.now().Sub(start)
		wait := interval - elapsed
		if wait > 0 {
			if !r.sleepCtx(ctx, wait) {
				return
			}
		}
	}
}

func (r *Runtime) sleepCtx(ctx context.Context, d time.Duration) bool {
	if r.sleep != nil {
		return r.sleep(ctx, d)
	}
	return sleepWithContext(ctx, d)
}

func (r *Runtime) firstScanDelay() time.Duration {
	if r != nil && r.startupDelay != nil {
		return *r.startupDelay
	}
	return autoScanStartupDelay
}

func (r *Runtime) autoScannerInterval() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.autoScanInterval <= 0 {
		return config.Default().ScanInterval
	}
	return r.autoScanInterval
}

func (r *Runtime) scanAutoCandidates(ctx context.Context) {
	snapshot, ok := r.autoSnapshot()
	if !ok {
		return
	}
	if !r.claimAutoScanSlot() {
		return
	}
	defer r.releaseAutoScanSlot()

	files, err := snapshot.host.ListAuthFiles(ctx)
	if err != nil {
		r.snapshotScanSummary(scanSummaryInput{
			Attempted: 0, Succeeded: 0, Failed: 1, Skipped: 0,
			FailReasons: map[string]int{"列举凭证失败": 1},
		})
		return
	}

	// 先串行收集候选（含 runtime auth 读取），再 worker 池并发 activate。
	candidates := make([]autoCandidate, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			r.snapshotScanSummary(newScanSummaryAccumulator().toInput())
			return
		}
		runtimeAuth := snapshot.runtimeAuthFile(ctx, file)
		if err := ctx.Err(); err != nil {
			r.snapshotScanSummary(newScanSummaryAccumulator().toInput())
			return
		}
		candidate, ok := snapshot.autoCandidate(file, runtimeAuth)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}

	acc := newScanSummaryAccumulator()
	workers := maxConcurrencyWorkers(snapshot.config.MaxConcurrency)
	// 缓冲 = 候选数，避免 ctx 取消后 worker 退出导致派发送死锁。
	jobs := make(chan autoCandidate, len(candidates))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				if err := ctx.Err(); err != nil {
					// 排空剩余 job，保证 close 后 sender/wg 不挂死。
					continue
				}
				detail := snapshot.activateCandidateDetail(ctx, candidate)
				acc.add(string(candidate.provider), detail)
			}
		}()
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			break
		}
		select {
		case <-ctx.Done():
		case jobs <- candidate:
		}
		if err := ctx.Err(); err != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	// 每个 auto tick 只写一条合并摘要，禁止同 tick 再写 activation。
	r.snapshotScanSummary(acc.toInput())
}

func maxConcurrencyWorkers(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func (r *Runtime) autoSnapshot() (autoScanSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shutdown || !r.config.AutoActivate || r.host == nil || r.activator == nil || r.store == nil {
		return autoScanSnapshot{}, false
	}
	return autoScanSnapshot{
		host: r.host, activator: r.activator, store: r.store, config: r.config, now: r.now,
		shouldSkipActivateCooldown: r.activateCooldownActive,
		noteActivateCooldown:       r.markActivateCooldownWithReason,
		cooldownSkipReason:         r.activateCooldownReason,
		rememberUsable:             r.rememberUsableSuccess,
	}, true
}

func (r *Runtime) claimAutoScanSlot() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.autoScanRunning {
		return false
	}
	interval := r.autoScanInterval
	if interval <= 0 {
		interval = r.config.ScanInterval
	}
	if interval <= 0 {
		interval = config.Default().ScanInterval
	}
	// 阈值 = interval - slack，允许 timer 微早触发仍认领，避免静默丢整轮。
	minGap := interval - autoScanIntervalSlack
	if minGap < 0 {
		minGap = 0
	}
	now := r.now()
	if !r.lastAutoScanAt.IsZero() && now.Sub(r.lastAutoScanAt) < minGap {
		return false
	}
	r.lastAutoScanAt = now
	r.autoScanRunning = true
	return true
}

func (r *Runtime) releaseAutoScanSlot() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoScanRunning = false
}

func (r *Runtime) schedulerMissCooldownDuration() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.schedulerMissCooldownDurationLocked()
}

func (r *Runtime) schedulerMissCooldownDurationLocked() time.Duration {
	interval := r.autoScanInterval
	if interval <= 0 {
		interval = r.config.ScanInterval
	}
	if interval <= 0 {
		interval = config.Default().ScanInterval
	}
	if interval > maxSchedulerMissCooldown {
		return maxSchedulerMissCooldown
	}
	if interval <= 0 {
		return maxSchedulerMissCooldown
	}
	return interval
}

func (r *Runtime) activateCooldownActive(authID string) bool {
	if authID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.schedulerMissUntil[authID]
	if !ok {
		return false
	}
	now := r.now()
	if now.Before(until) {
		return true
	}
	delete(r.schedulerMissUntil, authID)
	delete(r.activateCooldownReasonByAuth, authID)
	return false
}

func (r *Runtime) activateCooldownReason(authID string) string {
	if authID == "" {
		return schedulerMissCooldownReason
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if reason, ok := r.activateCooldownReasonByAuth[authID]; ok && reason != "" {
		return reason
	}
	return schedulerMissCooldownReason
}

func (r *Runtime) markActivateCooldownWithReason(authID, reason string) {
	if authID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.schedulerMissUntil == nil {
		r.schedulerMissUntil = make(map[string]time.Time)
	}
	if r.activateCooldownReasonByAuth == nil {
		r.activateCooldownReasonByAuth = make(map[string]string)
	}
	cooldown := r.schedulerMissCooldownDurationLocked()
	r.schedulerMissUntil[authID] = r.now().Add(cooldown)
	if reason == "" {
		reason = schedulerMissCooldownReason
	}
	r.activateCooldownReasonByAuth[authID] = reason
}

func (r *Runtime) schedulerMissCooldownActive(authID string) bool {
	return r.activateCooldownActive(authID)
}

func (r *Runtime) markSchedulerMiss(authID string) {
	r.markActivateCooldownWithReason(authID, schedulerMissCooldownReason)
}

func (r *Runtime) clearSchedulerMissCooldown(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.schedulerMissUntil, authID)
	delete(r.activateCooldownReasonByAuth, authID)
}

func (s autoScanSnapshot) runtimeAuthFile(ctx context.Context, file host.AuthFile) autoRuntimeAuthFile {
	authIndex := firstNonBlankAuto(file.AuthIndex)
	if authIndex == "" {
		return autoRuntimeAuthFile{}
	}
	runtimeFile, err := s.host.GetRuntimeAuthFile(ctx, authIndex)
	if err != nil {
		return autoRuntimeAuthFile{}
	}
	return autoRuntimeAuthFile{file: runtimeFile, ok: true}
}

type autoScanOutcome int

const (
	autoScanOutcomeSkipped autoScanOutcome = iota
	autoScanOutcomeSucceeded
	autoScanOutcomeFailed
)

type autoScanDetail struct {
	outcome    autoScanOutcome
	reason     string
	errMessage string
	err        error
	warning    string
}

func (s autoScanSnapshot) activateCandidate(ctx context.Context, candidate autoCandidate) error {
	return s.activateCandidateDetail(ctx, candidate).err
}

func (s autoScanSnapshot) activateCandidateOutcome(ctx context.Context, candidate autoCandidate) (autoScanOutcome, string) {
	d := s.activateCandidateDetail(ctx, candidate)
	return d.outcome, d.reason
}

func (s autoScanSnapshot) activateCandidateWithOutcome(ctx context.Context, candidate autoCandidate) (autoScanOutcome, string, error) {
	d := s.activateCandidateDetail(ctx, candidate)
	return d.outcome, d.reason, d.err
}

func (s autoScanSnapshot) activateCandidateDetail(ctx context.Context, candidate autoCandidate) autoScanDetail {
	observedAt := s.now().UTC()
	previous := previousStateFromStore(s.store, candidate.authID, candidate.provider)
	decision, err := detector.EvaluateWithPrevious(detector.ProbeInput{
		AuthID: candidate.authID, Provider: candidate.provider, Model: candidate.model,
		ObservedAt: observedAt, Payload: candidate.payload,
		Enable5hWindow: s.config.Enable5hWindow,
	}, previous)
	if err != nil {
		msg := localizeHistoryMessage(err.Error())
		return autoScanDetail{outcome: autoScanOutcomeFailed, reason: msg, errMessage: msg, err: err}
	}
	if !decision.Activate {
		return autoScanDetail{outcome: autoScanOutcomeSkipped, reason: decision.Reason}
	}
	// 硬闸：同 auth+provider 已有 success 且 ResetAt 仍晚于 now，且非 remaining 恢复 → 强制 skip。
	// 防止 synthetic 窗漂移导致 CycleKey 变化后反复唤醒。
	if skip, reason := shouldHardSkipActiveCycle(s.store, candidate, decision, observedAt); skip {
		return autoScanDetail{outcome: autoScanOutcomeSkipped, reason: reason}
	}
	if s.shouldSkipActivateCooldown != nil && s.shouldSkipActivateCooldown(candidate.authID) {
		reason := schedulerMissCooldownReason
		if s.cooldownSkipReason != nil {
			if r := s.cooldownSkipReason(candidate.authID); r != "" {
				reason = r
			}
		}
		return autoScanDetail{outcome: autoScanOutcomeSkipped, reason: reason}
	}
	result, err := s.activator.Activate(ctx, activator.Request{
		AuthID: candidate.authID, Provider: candidate.provider,
		ModelGroup: decision.Observation.ModelGroup, Window: decision.Observation.Window,
		CycleKey: decision.CycleKey, Model: candidate.model, Disabled: candidate.disabled,
		ObservedAt: observedAt, ResetAt: decision.Observation.ResetAt,
		Remaining: decision.Observation.Remaining, HasRemaining: decision.Observation.HasRemaining,
	})
	failText := result.LastError
	if failText == "" && err != nil {
		failText = err.Error()
	}
	s.maybeNoteCooldown(candidate.authID, failText)
	if err != nil {
		msg := localizeHistoryMessage(failText)
		if msg == "" {
			msg = localizeHistoryMessage(err.Error())
		}
		return autoScanDetail{outcome: autoScanOutcomeFailed, reason: msg, errMessage: msg, err: err}
	}
	if result.Status == activator.StatusSuccess && result.Success {
		if s.rememberUsable != nil {
			s.rememberUsable(usableRecordFromResult(result))
		}
		detail := autoScanDetail{outcome: autoScanOutcomeSucceeded}
		if result.Warning != "" {
			detail.warning = result.Warning
		}
		return detail
	}
	if result.Status == activator.StatusSkipped || result.Status == activator.StatusBusy {
		reason := decision.Reason
		if reason == "" {
			reason = string(result.Status)
		}
		if result.LastError != "" {
			reason = result.LastError
		}
		return autoScanDetail{outcome: autoScanOutcomeSkipped, reason: reason}
	}
	msg := localizeHistoryMessage(result.LastError)
	if msg == "" {
		msg = "唤醒失败"
	}
	return autoScanDetail{outcome: autoScanOutcomeFailed, reason: msg, errMessage: msg}
}

func (s autoScanSnapshot) maybeNoteCooldown(authID, failText string) {
	if s.noteActivateCooldown == nil || failText == "" {
		return
	}
	switch {
	case isSchedulerMissError(failText):
		s.noteActivateCooldown(authID, schedulerMissCooldownReason)
	case isHostExecutorUnavailableError(failText):
		s.noteActivateCooldown(authID, hostExecutorUnavailableCooldownReason)
	}
}

func isSchedulerMissError(message string) bool {
	if message == "" {
		return false
	}
	if message == schedulerMissErrorMessage || message == schedulerMissCooldownReason {
		return true
	}
	lower := strings.ToLower(message)
	return strings.Contains(message, "调度器未选中目标凭证") ||
		strings.Contains(lower, "activation scheduler did not select")
}

func isHostExecutorUnavailableError(message string) bool {
	if message == "" {
		return false
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "host model executor is unavailable") {
		return true
	}
	if strings.Contains(lower, "host_call_failed") && strings.Contains(lower, "executor") {
		return true
	}
	if strings.Contains(lower, "host callback host.model.execute") && strings.Contains(lower, "unavailable") {
		return true
	}
	return strings.Contains(message, hostExecutorUnavailableMessage) ||
		strings.Contains(message, hostExecutorUnavailableCooldownReason)
}

func previousStateFromStore(store *state.Store, authID string, provider detector.Provider) detector.PreviousState {
	if store == nil {
		return detector.PreviousState{}
	}
	cycleKey, remaining, hasRemaining, ok := store.LatestSuccessCycle(authID, string(provider))
	if !ok {
		return detector.PreviousState{}
	}
	if !hasRemaining && remaining != 0 {
		hasRemaining = true
	}
	return detector.PreviousState{
		CycleKey: cycleKey, Remaining: remaining, HasRemaining: hasRemaining,
	}
}

// shouldHardSkipActiveCycle 在 detector 已判 Activate 时二次拦截：
// store 有同 auth+provider UsableLatestSuccess，ResetAt.After(now)，且本次不是「额度已恢复」。
// Codex 的 5h success 不参与硬闸。
const autoCycleAlreadyProcessedReason = "额度周期已处理"

func shouldHardSkipActiveCycle(store *state.Store, candidate autoCandidate, decision detector.Decision, observedAt time.Time) (bool, string) {
	if store == nil {
		return false, ""
	}
	// 真实 payload 证明 remaining 恢复时允许激活。
	if decision.Reason == "额度已恢复" {
		return false, ""
	}
	if decision.Observation.HasRemaining && decision.Observation.Remaining > 0 {
		// 有 remaining 证据时，仅当 previous 也能证明恢复才走到 Activate；
		// 若 previous 无 remaining 快照，detector 已会 skip；此处再保险：
		// 若 detector 因 CycleKey 漂移误 Activate，且 success 仍在周期内，则 skip。
		record, ok := store.UsableLatestSuccess(candidate.authID, string(candidate.provider))
		if !ok || record.ResetAt.IsZero() || !record.ResetAt.UTC().After(observedAt.UTC()) {
			return false, ""
		}
		// remaining 恢复路径：previous 有快照且从 0 回升 / 耗尽后恢复 → detector reason 已是 额度已恢复。
		// 此处仅拦截「无恢复证据」的漂移 Activate。
		if recordHasRemainingSnapshot(record) {
			// 有 previous remaining 时信任 detector 结果。
			return false, ""
		}
		// success 无 remaining 快照 + 周期未结束 + detector 却 Activate → 强制 skip。
		return true, autoCycleAlreadyProcessedReason
	}
	record, ok := store.UsableLatestSuccess(candidate.authID, string(candidate.provider))
	if !ok {
		return false, ""
	}
	if record.ResetAt.IsZero() || !record.ResetAt.UTC().After(observedAt.UTC()) {
		return false, ""
	}
	// 无真实 remaining 证明恢复，且周期未结束 → 禁止再唤醒。
	return true, autoCycleAlreadyProcessedReason
}
