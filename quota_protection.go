package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"sync"
	"time"
)

const quotaProtectionOwnerKey = "cpa_quota_sentinel_owner"

// authLifecycleMu 串行化新旧托管流程，避免它们同时改写同一个 auth。
var authLifecycleMu sync.Mutex

// QuotaProtectionRecord 同时保存暂停证据和禁用所有权；无 marker 的旧记录绝不获得恢复权限。
type QuotaProtectionRecord struct {
	ManagedDisableRecord
	OwnerToken         string      `json:"owner_token,omitempty"`
	OwnershipLost      bool        `json:"ownership_lost,omitempty"`
	FiveHourRequired   bool        `json:"five_hour_required,omitempty"`
	LongWindowRequired bool        `json:"long_window_required,omitempty"`
	ResetUncertain     bool        `json:"reset_uncertain,omitempty"`
	Quota              ParsedQuota `json:"quota"`
}

// remainingPercent 仅接受有限且合法的百分比，未知值不构成恢复证据。
func remainingPercent(window *QuotaWindow) (float64, bool) {
	if window == nil || window.UsedPercent == nil {
		return 0, false
	}
	used := *window.UsedPercent
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
		return 0, false
	}
	return 100 - used, true
}

// quotaNeedsProtectionPause 任一已知受保护窗口达到下限就阻断调度。
func quotaNeedsProtectionPause(quota ParsedQuota, cfg Config) bool {
	if !cfg.QuotaProtectionEnabled {
		return false
	}
	for _, item := range []struct {
		window    *QuotaWindow
		threshold float64
	}{{quota.FiveHour, cfg.FiveHourRemainingThreshold}, {quota.LongWindow, cfg.LongWindowRemainingThreshold}} {
		if item.window != nil && item.window.Exhausted {
			return true
		}
		if remaining, known := remainingPercent(item.window); known && remaining <= item.threshold {
			return true
		}
	}
	return false
}

// quotaMeetsRecoveryThreshold 按实际返回的窗口验证额度，不将未提供的 5 小时窗口当作耗尽。
// 此入口用于没有历史窗口记录的旧托管流程；已有记录的恢复必须保留原窗口要求。
func quotaMeetsRecoveryThreshold(quota ParsedQuota, cfg Config) bool {
	return protectionWindowsRecovered(quota, cfg, quota.FiveHour != nil, quota.LongWindow != nil)
}

// protectionWindowsRecovered 不以重置时间推断恢复，逐个核实已记录窗口与新出现窗口。
func protectionWindowsRecovered(quota ParsedQuota, cfg Config, fiveRequired, longRequired bool) bool {
	if !fiveRequired && !longRequired {
		return false
	}
	for _, item := range []struct {
		window   *QuotaWindow
		required bool
		floor    float64
	}{{quota.FiveHour, fiveRequired, cfg.FiveHourRemainingThreshold}, {quota.LongWindow, longRequired, cfg.LongWindowRemainingThreshold}} {
		if item.window == nil && !item.required {
			continue
		}
		remaining, known := remainingPercent(item.window)
		if !known || item.window.Exhausted || remaining < cfg.QuotaRecoveryThreshold || remaining <= item.floor {
			return false
		}
	}
	return true
}

// ApplyQuotaProtectionDisable 先保存暂停和随机所有权，再对原本启用的账号写入 marker 与禁用标志。
func (r *QuotaRefresher) ApplyQuotaProtectionDisable(ctx context.Context, authIndex string, quota ParsedQuota) error {
	if r == nil || r.host == nil || r.runtimeStore == nil || authIndex == "" || !quotaNeedsProtectionPause(quota, r.state.Config()) {
		return nil
	}
	authLifecycleMu.Lock()
	defer authLifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	response, err := r.host.GetAuth(authIndex)
	if err != nil {
		return err
	}
	raw, disabled, err := protectionAuthObject(response.JSON)
	if err != nil {
		return err
	}
	persisted, err := r.runtimeStore.PersistentSnapshot()
	if err != nil {
		return err
	}
	if _, managed := persisted.ManagedLifecycle[authIndex]; managed {
		return nil
	}
	if prior, exists := persisted.QuotaProtection[authIndex]; exists && (prior.OwnershipLost || disabled) {
		return nil
	}
	if disabled {
		return nil
	}
	credentials, err := ExtractCodexCredentials(response.JSON)
	if err != nil {
		return err
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	now := r.now()
	record := QuotaProtectionRecord{ManagedDisableRecord: ManagedDisableRecord{DisabledAt: now, Fingerprint: managedAuthFingerprint(authIndex, credentials), NextQuotaCheckAt: now.Add(r.state.Config().QuotaProtectionInterval)}, OwnerToken: hex.EncodeToString(token), FiveHourRequired: quota.FiveHour != nil, LongWindowRequired: quota.LongWindow != nil, Quota: quota}
	record.ResetAt, record.ResetUncertain = protectionResetDeadline(quota, r.state.Config())
	record.Events = []LifecycleEvent{{Kind: "low_quota", At: now, Detail: "受保护窗口剩余额度达到阈值"}}
	if _, err := r.runtimeStore.Update(func(state *PersistentState) error { state.QuotaProtection[authIndex] = record; return nil }); err != nil {
		return err
	}
	raw[quotaProtectionOwnerKey], _ = json.Marshal(record.OwnerToken)
	raw["disabled"] = json.RawMessage("true")
	if err := r.saveProtectionAuth(authIndex, response.Name, response.JSON, raw); err != nil {
		return err
	}
	_, err = r.runtimeStore.Update(func(state *PersistentState) error {
		item := state.QuotaProtection[authIndex]
		item.DisableApplied = true
		state.QuotaProtection[authIndex] = item
		return nil
	})
	if err == nil {
		r.protectionLog("info", "protection.disabled", "额度保护已暂停并托管禁用账号", authIndex)
	}
	return err
}

// protectionResetDeadline 记录触发保护的全部窗口的最晚重置时间，未知时间时禁止自动恢复。
func protectionResetDeadline(quota ParsedQuota, cfg Config) (time.Time, bool) {
	var latest time.Time
	for _, item := range []struct {
		window    *QuotaWindow
		threshold float64
	}{{quota.FiveHour, cfg.FiveHourRemainingThreshold}, {quota.LongWindow, cfg.LongWindowRemainingThreshold}} {
		if item.window == nil {
			continue
		}
		remaining, known := remainingPercent(item.window)
		if !item.window.Exhausted && (!known || remaining > item.threshold) {
			continue
		}
		if item.window.ResetAt.IsZero() {
			return time.Time{}, true
		}
		if item.window.ResetAt.After(latest) {
			latest = item.window.ResetAt
		}
	}
	return latest, latest.IsZero()
}

// RecoverQuotaProtectionAccounts 每轮先推进持久化期限，任何失败都不会造成到期忙循环。
func (r *QuotaRefresher) RecoverQuotaProtectionAccounts(ctx context.Context) error {
	if r == nil || r.host == nil || r.runtimeStore == nil || !r.state.Config().QuotaProtectionEnabled || !r.state.Config().QuotaAutoRecover {
		return nil
	}
	authLifecycleMu.Lock()
	defer authLifecycleMu.Unlock()
	persisted, err := r.runtimeStore.PersistentSnapshot()
	if err != nil {
		return err
	}
	indices := make([]string, 0, len(persisted.QuotaProtection))
	for index, record := range persisted.QuotaProtection {
		if !record.OwnershipLost && !record.NextQuotaCheckAt.After(r.now()) {
			indices = append(indices, index)
		}
	}
	sort.Strings(indices)
	for _, index := range indices {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.deferProtectionCheck(index); err != nil {
			return err
		}
		if err := r.recoverProtectionAccount(ctx, index, persisted.QuotaProtection[index]); err != nil {
			r.protectionLog("warn", "protection.check_failed", "额度保护复核失败，将按检查间隔重试", index)
		}
	}
	return nil
}

// quotaProtectionDeadline 包括待确认写入，但排除失去所有权或禁止恢复的记录。
func (r *QuotaRefresher) quotaProtectionDeadline(now time.Time) time.Time {
	if r == nil || r.runtimeStore == nil || !r.state.Config().QuotaProtectionEnabled || !r.state.Config().QuotaAutoRecover {
		return time.Time{}
	}
	persisted, err := r.runtimeStore.PersistentSnapshot()
	if err != nil {
		return time.Time{}
	}
	var deadline time.Time
	for _, record := range persisted.QuotaProtection {
		if record.OwnershipLost {
			continue
		}
		at := record.NextQuotaCheckAt
		if at.IsZero() || at.Before(now) {
			at = now
		}
		if deadline.IsZero() || at.Before(deadline) {
			deadline = at
		}
	}
	return deadline
}

// protectionAuthObject 严格检查 auth 与禁用字段，畸形值不能被当作启用账号接管。
func protectionAuthObject(data []byte) (map[string]json.RawMessage, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return nil, false, errors.New("额度保护需要有效 auth JSON")
	}
	var disabled bool
	if value, ok := raw["disabled"]; ok {
		if err := json.Unmarshal(value, &disabled); err != nil {
			return nil, false, err
		}
	}
	return raw, disabled, nil
}
