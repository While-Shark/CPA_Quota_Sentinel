package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var providerRoundRobinCounter uint64

var (
	providerRunnerMu   sync.Mutex
	providerRunnerStop chan struct{}
	providerRunnerDone chan struct{}
	providerRunnerWake chan struct{}
)

// providerRecoveryVerified 仅要求曾触发保护的窗口重置，且所有已知窗口达到恢复阈值。
// 缺失窗口、未知重置时间和仍处于旧周期的受保护窗口都不能触发自动恢复。
func providerRecoveryVerified(previous, fresh []ProviderQuotaWindow, cfg Config, now time.Time) bool {
	if len(previous) == 0 || !providerMeetsRecoveryThreshold(fresh, cfg) {
		return false
	}
	byID := make(map[string]ProviderQuotaWindow, len(fresh))
	freshNames := make(map[string]int, len(fresh))
	for _, window := range fresh {
		byID[providerQuotaIdentity(window)] = window
		freshNames[window.Name]++
	}
	protected := false
	for _, window := range previous {
		current, ok := byID[providerQuotaIdentity(window)]
		if !ok && window.Group == "" && window.BucketID == "" {
			if freshNames[window.Name] == 0 {
				return false
			}
			freshNames[window.Name]--
			for _, candidate := range fresh {
				if candidate.Name == window.Name && candidate.RemainingPercent != nil {
					current, ok = candidate, true
					break
				}
			}
		}
		if !ok || current.RemainingPercent == nil || window.RemainingPercent == nil {
			return false
		}
		if *window.RemainingPercent > providerProtectionThresholdForWindow(window, cfg) {
			continue
		}
		protected = true
		if window.ResetAt == nil || *window.ResetAt == "" {
			return false
		}
		reset, err := time.Parse(time.RFC3339, *window.ResetAt)
		if err != nil || now.Before(reset) {
			return false
		}
	}
	return protected
}

func providerProtectionThresholdForWindow(window ProviderQuotaWindow, cfg Config) float64 {
	nameLower := strings.ToLower(window.Name)
	idLower := strings.ToLower(window.BucketID)
	if strings.Contains(nameLower, "5h") || strings.Contains(nameLower, "5小时") ||
		strings.Contains(nameLower, "five hour") || strings.HasSuffix(idLower, "-5h") {
		return cfg.FiveHourRemainingThreshold
	}
	return cfg.LongWindowRemainingThreshold
}

// providerNeedsProtectionPause 检查非 Codex 账号的真实配额窗口是否触发低额度保护暂停。
// 规则：任一已知窗口剩余 <= 对应阈值立即暂停；未知额度账号永不误判为耗尽。
func providerNeedsProtectionPause(windows []ProviderQuotaWindow, cfg Config) bool {
	if !cfg.QuotaProtectionEnabled || len(windows) == 0 {
		return false
	}
	for _, w := range windows {
		if w.RemainingPercent == nil {
			continue
		}
		threshold := providerProtectionThresholdForWindow(w, cfg)
		if *w.RemainingPercent <= threshold {
			return true
		}
	}
	return false
}

// providerMeetsRecoveryThreshold 验证非 Codex 账号是否满足自动恢复条件。
// 规则：必须具有已知窗口，且所有已知窗口必须达到恢复阈值（默认 30%）且高于暂停阈值。
func providerMeetsRecoveryThreshold(windows []ProviderQuotaWindow, cfg Config) bool {
	if len(windows) == 0 {
		return false
	}
	hasKnown := false
	for _, w := range windows {
		if w.RemainingPercent == nil {
			continue
		}
		hasKnown = true
		if *w.RemainingPercent < cfg.QuotaRecoveryThreshold {
			return false
		}
		threshold := providerProtectionThresholdForWindow(w, cfg)
		if *w.RemainingPercent <= threshold {
			return false
		}
	}
	return hasKnown
}

// pickProviderAccount 针对非 Codex 认证提供商执行安全调度选择、额度哨兵保护与多账号轮换。
func pickProviderAccount(req pluginapi.SchedulerPickRequest, now time.Time) PickDecision {
	if len(req.Candidates) == 0 {
		return PickDecision{Reason: "no_candidates"}
	}

	cfg := globalState.Config()
	if !cfg.HandleEnabled {
		return PickDecision{Reason: "scheduler_disabled"}
	}
	var eligible []pluginapi.SchedulerAuthCandidate
	allExcludedByProtection := true
	hadValidCandidates := false

	for _, c := range req.Candidates {
		acc, exists := globalMultiProviderRegistry.Get(c.ID)
		if exists && acc.Disabled {
			continue
		}
		hadValidCandidates = true

		// 额度保护校验
		if cfg.QuotaProtectionEnabled {
			isPaused := exists && acc.Paused
			needsPause := exists && providerNeedsProtectionPause(acc.Windows, cfg)
			if exists && acc.Provider == "antigravity" {
				// 按实际请求模型族检查 AGY 的独立配额桶。
				// Gemini 4 Argon 耗尽不能停掉普通 Gemini 或 Claude/GPT。
				needsPause = antigravityGroupBlocked(acc, req.Model, cfg)
			}
			if isPaused || needsPause {
				continue
			}
		}

		allExcludedByProtection = false
		eligible = append(eligible, c)
	}

	if len(eligible) == 0 {
		if hadValidCandidates && allExcludedByProtection && cfg.QuotaProtectionEnabled {
			return PickDecision{
				Handled: true,
				Reject:  true,
				Reason:  "quota_protection",
			}
		}
		return PickDecision{Reason: "no_eligible_candidates"}
	}

	// CPA 数值越大优先级越高，仅在最高可用层级内轮换。
	bestPriority := eligible[0].Priority
	for _, c := range eligible {
		if c.Priority > bestPriority {
			bestPriority = c.Priority
		}
	}

	var topTier []pluginapi.SchedulerAuthCandidate
	for _, c := range eligible {
		if c.Priority == bestPriority {
			topTier = append(topTier, c)
		}
	}

	idx := atomic.AddUint64(&providerRoundRobinCounter, 1) % uint64(len(topTier))
	selected := topTier[idx]

	return PickDecision{
		AuthID:  selected.ID,
		Handled: true,
		Reject:  false,
		Reason:  "selected",
	}
}

// startProviderBackgroundRunner 启动非 Codex 多 Provider 的后台定期额度探测与恢复轮询。
func startProviderBackgroundRunner() {
	providerRunnerMu.Lock()
	defer providerRunnerMu.Unlock()
	if providerRunnerStop != nil {
		return // 已在运行
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	providerRunnerStop = stop
	providerRunnerDone = done
	wake := make(chan struct{}, 1)
	providerRunnerWake = wake

	// 初始化时载入历史持久化状态
	globalMultiProviderRegistry.loadFromDisk()

	go func() {
		defer close(done)

		// 启动时在后台做一次初次探测
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, _ = RefreshProviderAccounts(ctx, true)
		}()

		for {
			interval := globalState.Config().QuotaProtectionInterval
			if interval <= 0 {
				interval = 5 * time.Minute
			}
			timer := time.NewTimer(interval)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-wake:
				timer.Stop()
				continue
			case <-timer.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				_, _ = RefreshProviderAccounts(ctx, true)
				cancel()
			}
		}
	}()
}

// wakeProviderBackgroundRunner 让新的检查间隔在保存设置后立即重新排期。
func wakeProviderBackgroundRunner() {
	providerRunnerMu.Lock()
	wake := providerRunnerWake
	providerRunnerMu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// stopProviderBackgroundRunner 安全停止后台探测循环。
func stopProviderBackgroundRunner() {
	providerRunnerMu.Lock()
	stop := providerRunnerStop
	done := providerRunnerDone
	providerRunnerStop = nil
	providerRunnerDone = nil
	providerRunnerWake = nil
	providerRunnerMu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
}
