package main

import (
	"strings"
	"time"
)

// antigravityBucketGroup 使用上游稳定的 bucketId 优先识别额度池，
// 避免两组都叫 "Weekly Limit Remaining" 时错误混淆。
func antigravityBucketGroup(groupName, bucketName, bucketID string) string {
	id := strings.ToLower(strings.TrimSpace(bucketID))
	label := strings.ToLower(groupName + " " + bucketName)
	switch {
	case strings.Contains(id, "argon") || strings.Contains(label, "argon"):
		return "gemini-argon"
	case strings.HasPrefix(id, "3p-") || strings.Contains(label, "claude") ||
		strings.Contains(label, "gpt") || strings.Contains(label, "third-party"):
		return "claude-gpt"
	case strings.HasPrefix(id, "gemini-") || strings.Contains(label, "gemini"):
		return "gemini"
	default:
		return "" // 未知桶不推断归属：沿用账号级保护
	}
}

// antigravityModelGroup 仅按明确的模型族匹配额度池；别名无法识别时不猜测。
func antigravityModelGroup(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(name, "argon"):
		return "gemini-argon"
	case strings.Contains(name, "gemini"):
		return "gemini"
	case strings.Contains(name, "claude"), strings.Contains(name, "gpt"),
		strings.Contains(name, "oss-"):
		return "claude-gpt"
	default:
		return ""
	}
}

// providerQuotaIdentity 用 bucketId 区分重名的 AGY 配额窗口。
// 旧持久化记录没有 bucketId 时，仍可以用名称验证恢复，但不自动臆测缺失的桶。
func providerQuotaIdentity(window ProviderQuotaWindow) string {
	if window.BucketID != "" {
		return "bucket:" + window.BucketID
	}
	if window.Group != "" {
		return window.Group + ":" + window.Name
	}
	return window.Name
}

func quotaWindowsForGroup(windows []ProviderQuotaWindow, group string) []ProviderQuotaWindow {
	var selected []ProviderQuotaWindow
	for _, window := range windows {
		if window.Group == group {
			selected = append(selected, window)
		}
	}
	return selected
}

// advanceAntigravityGroupPauses 为各模型池分别保留暂停基线。
// 不存在的桶视为未知，既不推断耗尽，也不能被用于证明已暂停桶恢复。
func advanceAntigravityGroupPauses(
	previous map[string]ProviderGroupPause,
	windows []ProviderQuotaWindow,
	cfg Config,
	now time.Time,
) map[string]ProviderGroupPause {
	if !cfg.QuotaProtectionEnabled {
		return previous
	}
	next := make(map[string]ProviderGroupPause, len(previous))
	for group, pause := range previous {
		next[group] = pause
	}
	groups := make(map[string][]ProviderQuotaWindow)
	for _, window := range windows {
		if window.Group == "" {
			continue
		}
		groups[window.Group] = append(groups[window.Group], window)
	}
	for group, groupWindows := range groups {
		if pause, paused := next[group]; paused {
			if cfg.QuotaAutoRecover && providerRecoveryVerified(pause.Windows, groupWindows, cfg, now) {
				delete(next, group)
			}
			continue
		}
		if providerNeedsProtectionPause(groupWindows, cfg) {
			next[group] = ProviderGroupPause{
				Windows: append([]ProviderQuotaWindow(nil), groupWindows...),
			}
		}
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

// migrateLegacyAntigravityPause 仅迁移能证明归属的旧整号暂停。
// 无法识别的低额度桶继续保持旧暂停，杜绝升级后误恢复；可识别的 Argon
// 旧暂停转为独立模型组保护，避免误阻断仍有额度的其他模型。
func migrateLegacyAntigravityPause(
	account *ProviderAccount,
	fresh []ProviderQuotaWindow,
	cfg Config,
) {
	if account == nil || !account.Paused || len(account.PauseWindows) == 0 {
		return
	}
	groupWindows := make(map[string][]ProviderQuotaWindow)
	for _, previous := range account.PauseWindows {
		if previous.RemainingPercent == nil {
			return
		}
		if *previous.RemainingPercent > providerProtectionThresholdForWindow(previous, cfg) {
			continue
		}
		group := antigravityBucketGroup("", previous.Name, previous.BucketID)
		if group == "" {
			return // 旧版重名 Weekly Limit 无法确认归属
		}
		matched := false
		for _, current := range fresh {
			if current.Group != group || current.Name != previous.Name {
				continue
			}
			copyWindow := previous
			copyWindow.Group = group
			copyWindow.BucketID = current.BucketID
			groupWindows[group] = append(groupWindows[group], copyWindow)
			matched = true
			break
		}
		if !matched {
			return // 上游已隐藏原配额桶，不能借升级恢复
		}
	}
	if len(groupWindows) == 0 {
		return
	}
	if account.GroupPauses == nil {
		account.GroupPauses = make(map[string]ProviderGroupPause)
	}
	for group, paused := range groupWindows {
		account.GroupPauses[group] = ProviderGroupPause{Windows: paused}
	}
	account.Paused = false
	account.PauseWindows = nil
}

func antigravityGroupBlocked(acc ProviderAccount, model string, cfg Config) bool {
	group := antigravityModelGroup(model)
	if group == "" {
		// 无法确定请求模型归属时，不让无关的低额度模型桶阻塞整号。
		// 对没有分组标识的旧/未知窗口保留现有全局规则。
		return providerNeedsProtectionPause(quotaWindowsForGroup(acc.Windows, ""), cfg)
	}
	if _, paused := acc.GroupPauses[group]; paused {
		return true
	}
	return providerNeedsProtectionPause(quotaWindowsForGroup(acc.Windows, group), cfg) ||
		providerNeedsProtectionPause(quotaWindowsForGroup(acc.Windows, ""), cfg)
}
