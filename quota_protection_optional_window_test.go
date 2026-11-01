package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestProtectionWithoutFiveHourWindow 验证只有周或月窗口的账号可正常暂停并经真实查询恢复。
func TestProtectionWithoutFiveHourWindow(t *testing.T) {
	for _, seconds := range []int{weekSeconds, minMonthSeconds} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
			r, host, _ := managedDisableTestRefresher(t, now, protectionEnabledConfig())
			// 长窗口可能位于 primary_window，必须按实际时长识别，而不是当作 5 小时。
			body := fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":80,"limit_window_seconds":%d,"reset_after_seconds":-1}}}`, seconds)
			quota, err := ParseCodexUsagePayload([]byte(body), now)
			if err != nil || quota.FiveHour != nil || quota.LongWindow == nil {
				t.Fatalf("unexpected window classification: %#v, %v", quota, err)
			}
			if err := r.ApplyQuotaProtectionDisable(context.Background(), "idx-team", quota); err != nil {
				t.Fatal(err)
			}
			state, err := r.runtimeStore.PersistentSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			record, exists := state.QuotaProtection["idx-team"]
			if !exists || record.FiveHourRequired || !record.LongWindowRequired || !record.DisableApplied {
				t.Fatalf("unexpected protected windows: %#v", record)
			}
			host.authJSON["idx-team"] = host.saved["idx-team.json"]
			host.httpBody = []byte(fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":70,"limit_window_seconds":%d,"reset_after_seconds":3600}}}`, seconds))
			protectionDue(t, r, "idx-team")
			if err := r.RecoverQuotaProtectionAccounts(context.Background()); err != nil {
				t.Fatal(err)
			}
			if host.httpCalls != 1 || !strings.Contains(string(host.saved["idx-team.json"]), `"disabled":false`) {
				t.Fatal("long-only account did not recover after fresh quota verification")
			}
			state, err = r.runtimeStore.PersistentSnapshot()
			if err != nil || len(state.QuotaProtection) != 0 {
				t.Fatalf("ownership not released: %v", err)
			}
		})
	}
}

// TestProtectionOptionalWindowEvidence 区分原本不提供窗口与恢复时缺失已有窗口。
func TestProtectionOptionalWindowEvidence(t *testing.T) {
	cfg := protectionEnabledConfig()
	longOnly := protectionTestQuota(80, 30)
	longOnly.FiveHour = nil
	if !quotaMeetsRecoveryThreshold(longOnly, cfg) {
		t.Fatal("long-only account should meet recovery threshold")
	}
	if !protectionWindowsRecovered(longOnly, cfg, false, true) {
		t.Fatal("nonexistent five-hour window should not block recovery")
	}
	if protectionWindowsRecovered(longOnly, cfg, true, true) {
		t.Fatal("previously known five-hour window must not silently disappear")
	}
	longOnly.LongWindow.UsedPercent = nil
	if quotaMeetsRecoveryThreshold(longOnly, cfg) {
		t.Fatal("present window with unknown remaining quota must not recover")
	}
	if quotaMeetsRecoveryThreshold(ParsedQuota{}, cfg) {
		t.Fatal("empty quota payload must not count as an unlimited account")
	}
}
