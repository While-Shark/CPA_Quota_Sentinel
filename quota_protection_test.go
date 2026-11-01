package main

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// protectionTestQuota 构造明确的双窗口额度证据。
func protectionTestQuota(five, long float64) ParsedQuota {
	f, l := 100-five, 100-long
	return ParsedQuota{FiveHour: &QuotaWindow{UsedPercent: &f, ResetAt: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)}, LongWindow: &QuotaWindow{UsedPercent: &l, ResetAt: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)}}
}

// protectionDue 将持久化检查时间推进到当前，避免测试依赖真实等待。
func protectionDue(t *testing.T, r *QuotaRefresher, index string) {
	t.Helper()
	_, err := r.runtimeStore.Update(func(state *PersistentState) error {
		record := state.QuotaProtection[index]
		record.NextQuotaCheckAt = r.now()
		state.QuotaProtection[index] = record
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func protectionEnabledConfig() Config {
	cfg := DefaultConfig()
	cfg.QuotaProtectionEnabled = true
	return cfg
}

func TestProtectionThresholdsAndUnknown(t *testing.T) {
	cfg := protectionEnabledConfig()
	if !quotaNeedsProtectionPause(protectionTestQuota(20, 80), cfg) {
		t.Fatal("equal threshold must pause")
	}
	if quotaNeedsProtectionPause(protectionTestQuota(21, 80), cfg) {
		t.Fatal("above threshold paused")
	}
	if !quotaMeetsRecoveryThreshold(protectionTestQuota(30, 30), cfg) {
		t.Fatal("equal recovery threshold rejected")
	}
	for _, quota := range []ParsedQuota{protectionTestQuota(29, 90), protectionTestQuota(90, 29), {}, {FiveHour: &QuotaWindow{UsedPercent: func() *float64 { v := math.NaN(); return &v }()}}} {
		if quotaMeetsRecoveryThreshold(quota, cfg) {
			t.Fatal("insufficient or unknown evidence recovered")
		}
	}
}

func TestProtectionOwnsMarkerAndRecoversFreshQuota(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r, host, _ := managedDisableTestRefresher(t, now, protectionEnabledConfig())
	if err := r.ApplyQuotaProtectionDisable(context.Background(), "idx-team", protectionTestQuota(20, 80)); err != nil {
		t.Fatal(err)
	}
	host.authJSON["idx-team"] = host.saved["idx-team.json"]
	persisted, _ := r.runtimeStore.PersistentSnapshot()
	record := persisted.QuotaProtection["idx-team"]
	if record.OwnerToken == "" || !record.DisableApplied || !record.Fingerprint.ValidHashes() {
		t.Fatalf("missing ownership: %#v", record)
	}
	host.httpBody = []byte(`{"rate_limit":{"primary_window":{"used_percent":70},"secondary_window":{"used_percent":70}}}`)
	protectionDue(t, r, "idx-team")
	if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(host.saved["idx-team.json"]), `"disabled":false`) {
		t.Fatal("fresh verified recovery not enabled")
	}
	if strings.Contains(string(host.saved["idx-team.json"]), quotaProtectionOwnerKey) {
		t.Fatal("owner marker retained after release")
	}
	persisted, _ = r.runtimeStore.PersistentSnapshot()
	if len(persisted.QuotaProtection) != 0 {
		t.Fatal("ownership not released")
	}
}

func TestProtectionNeverOwnsManualOrUnmarkedPending(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r, host, _ := managedDisableTestRefresher(t, now, protectionEnabledConfig())
	original := host.authJSON["idx-team"]
	var raw map[string]any
	_ = json.Unmarshal(original, &raw)
	raw["disabled"] = true
	host.authJSON["idx-team"], _ = json.Marshal(raw)
	if err := r.ApplyQuotaProtectionDisable(context.Background(), "idx-team", protectionTestQuota(10, 80)); err != nil {
		t.Fatal(err)
	}
	persisted, _ := r.runtimeStore.PersistentSnapshot()
	if len(persisted.QuotaProtection) != 0 || len(host.saved) != 0 {
		t.Fatal("manual disable adopted")
	}
	_, err := r.runtimeStore.Update(func(state *PersistentState) error {
		state.QuotaProtection["idx-team"] = QuotaProtectionRecord{OwnerToken: "unconfirmed", ManagedDisableRecord: ManagedDisableRecord{NextQuotaCheckAt: now}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, _ = r.runtimeStore.PersistentSnapshot()
	if !persisted.QuotaProtection["idx-team"].OwnershipLost || len(host.saved) != 0 {
		t.Fatal("pending record adopted unrelated manual disabled state")
	}
	if !r.quotaProtectionDeadline(now).IsZero() {
		t.Fatal("lost ownership keeps background deadline")
	}
}

func TestProtectionFailureBackoffAndAutoRecoveryOff(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r, host, store := managedDisableTestRefresher(t, now, protectionEnabledConfig())
	if err := r.ApplyQuotaProtectionDisable(context.Background(), "idx-team", protectionTestQuota(10, 80)); err != nil {
		t.Fatal(err)
	}
	host.authJSON["idx-team"] = host.saved["idx-team.json"]
	host.httpStatus = 503
	protectionDue(t, r, "idx-team")
	if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, _ := r.runtimeStore.PersistentSnapshot()
	if !persisted.QuotaProtection["idx-team"].NextQuotaCheckAt.After(now) {
		t.Fatal("failed query left due deadline")
	}
	before := host.httpCalls
	if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if host.httpCalls != before {
		t.Fatal("failure busy loop")
	}
	cfg := store.Config()
	cfg.QuotaAutoRecover = false
	store.ReplaceConfig(cfg)
	protectionDue(t, r, "idx-team")
	if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if host.httpCalls != before || !r.quotaProtectionDeadline(now).IsZero() {
		t.Fatal("auto recovery off still checks")
	}
}

func TestProtectionMarkerCrashResumeAndMissingWindow(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r, host, _ := managedDisableTestRefresher(t, now, protectionEnabledConfig())
	if err := r.ApplyQuotaProtectionDisable(context.Background(), "idx-team", protectionTestQuota(10, 80)); err != nil {
		t.Fatal(err)
	}
	host.authJSON["idx-team"] = host.saved["idx-team.json"]
	_, err := r.runtimeStore.Update(func(state *PersistentState) error {
		record := state.QuotaProtection["idx-team"]
		record.DisableApplied = false
		record.NextQuotaCheckAt = now
		state.QuotaProtection["idx-team"] = record
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	host.httpBody = []byte(`{"rate_limit":{"secondary_window":{"used_percent":0}}}`)
	if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted, _ := r.runtimeStore.PersistentSnapshot()
	record := persisted.QuotaProtection["idx-team"]
	if !record.DisableApplied || record.OwnershipLost {
		t.Fatal("valid crash marker not reconciled")
	}
	if strings.Contains(string(host.saved["idx-team.json"]), `"disabled":false`) {
		t.Fatal("missing protected window recovered")
	}
}

// TestProtectionRestartAndRotation 保证所有权可跨重启保存且凭据轮换后不会启用新账号。
func TestProtectionRestartAndRotation(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r, host, _ := managedDisableTestRefresher(t, now, protectionEnabledConfig())
	if err := r.ApplyQuotaProtectionDisable(context.Background(), "idx-team", protectionTestQuota(10, 80)); err != nil {
		t.Fatal(err)
	}
	host.authJSON["idx-team"] = host.saved["idx-team.json"]
	reloaded := NewStateStore(r.runtimeStore.path, OSFileHooks(), nil)
	state, err := reloaded.PersistentSnapshot()
	if err != nil || state.QuotaProtection["idx-team"].OwnerToken == "" {
		t.Fatalf("reload failed: %v", err)
	}
	r.runtimeStore = reloaded
	var raw map[string]any
	_ = json.Unmarshal(host.authJSON["idx-team"], &raw)
	raw["refresh_token"] = "rotated"
	host.authJSON["idx-team"], _ = json.Marshal(raw)
	protectionDue(t, r, "idx-team")
	if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ = r.runtimeStore.PersistentSnapshot()
	if !state.QuotaProtection["idx-team"].OwnershipLost {
		t.Fatal("credential rotation retained automatic ownership")
	}
	if strings.Contains(string(host.saved["idx-team.json"]), `"disabled":false`) {
		t.Fatal("rotated auth enabled")
	}
}

// TestProtectionMalformedAuthBackoff 解析失败仍必须推进截止时间。
func TestProtectionMalformedAuthBackoff(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	r, host, _ := managedDisableTestRefresher(t, now, protectionEnabledConfig())
	if err := r.ApplyQuotaProtectionDisable(context.Background(), "idx-team", protectionTestQuota(10, 80)); err != nil {
		t.Fatal(err)
	}
	host.authJSON["idx-team"] = json.RawMessage(`broken`)
	protectionDue(t, r, "idx-team")
	if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ := r.runtimeStore.PersistentSnapshot()
	if !state.QuotaProtection["idx-team"].NextQuotaCheckAt.After(now) {
		t.Fatal("malformed auth retries without backoff")
	}
}

// TestProtectionWaitsForResetAndMissingReset 在重置时间前或时间未知时绝不提前启用。
func TestProtectionWaitsForResetAndMissingReset(t *testing.T) {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	for _, reset := range []time.Time{now.Add(time.Hour), {}} {
		r, host, _ := managedDisableTestRefresher(t, now, protectionEnabledConfig())
		quota := protectionTestQuota(10, 80)
		quota.FiveHour.ResetAt = reset
		if err := r.ApplyQuotaProtectionDisable(context.Background(), "idx-team", quota); err != nil {
			t.Fatal(err)
		}
		host.authJSON["idx-team"] = host.saved["idx-team.json"]
		host.httpBody = []byte(`{"rate_limit":{"primary_window":{"used_percent":0},"secondary_window":{"used_percent":0}}}`)
		protectionDue(t, r, "idx-team")
		if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
			t.Fatal(err)
		}
		if host.httpCalls != 0 || strings.Contains(string(host.saved["idx-team.json"]), `"disabled":false`) {
			t.Fatalf("recovered before reset %v", reset)
		}
	}
}
