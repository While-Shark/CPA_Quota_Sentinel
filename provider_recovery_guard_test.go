package main

import (
	"testing"
	"time"
)

func TestProviderRecoveryOnlyRequiresExhaustedWindowReset(t *testing.T) {
	cfg := DefaultConfig()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute).Format(time.RFC3339)
	future := now.Add(5 * 24 * time.Hour).Format(time.RFC3339)
	low, high := 15.0, 80.0
	previous := []ProviderQuotaWindow{
		{Name: "5小时窗口 (5h)", RemainingPercent: &low, ResetAt: &past},
		{Name: "7天窗口 (7d)", RemainingPercent: &high, ResetAt: &future},
	}
	fresh := []ProviderQuotaWindow{
		{Name: "5小时窗口 (5h)", RemainingPercent: &high, ResetAt: &future},
		{Name: "7天窗口 (7d)", RemainingPercent: &high, ResetAt: &future},
	}
	if !providerRecoveryVerified(previous, fresh, cfg, now) {
		t.Fatal("healthy long window should not prevent recovery after protected five-hour reset")
	}
	noReset := append([]ProviderQuotaWindow(nil), previous...)
	noReset[0].ResetAt = nil
	if providerRecoveryVerified(noReset, fresh, cfg, now) {
		t.Fatal("unknown protected window reset must not enable account")
	}
	beforeReset := now.Add(-time.Hour)
	if providerRecoveryVerified(previous, fresh, cfg, beforeReset) {
		t.Fatal("protected window has not reached reset")
	}
	incomplete := fresh[:1]
	if providerRecoveryVerified(previous, incomplete, cfg, now) {
		t.Fatal("disappearing known quota window must prevent recovery")
	}
}
