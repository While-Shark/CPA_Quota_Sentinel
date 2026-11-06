package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Weekly-only accounts can use primary as the weekly window. The position
// must never invent a five-hour quota or apply the five-hour pause threshold.
func TestWeeklyOnlyCodexQuotaSources(t *testing.T) {
	now := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.QuotaProtectionEnabled = true
	cfg.FiveHourRemainingThreshold = 20
	cfg.LongWindowRemainingThreshold = 10

	t.Run("polled primary weekly", func(t *testing.T) {
		quota, err := ParseCodexUsagePayload([]byte(`{"rate_limit":{"primary_window":{"used_percent":85,"limit_window_seconds":604800,"reset_after_seconds":172800}}}`), now)
		if err != nil {
			t.Fatal(err)
		}
		checkWeeklyOnlyQuota(t, quota, cfg)
	})
	t.Run("polled primary window minutes", func(t *testing.T) {
		quota, err := ParseCodexUsagePayload([]byte(`{"rate_limit":{"primary_window":{"used_percent":85,"window_minutes":10080,"reset_after_seconds":172800}}}`), now)
		if err != nil {
			t.Fatal(err)
		}
		checkWeeklyOnlyQuota(t, quota, cfg)
	})
	t.Run("polled primary without duration", func(t *testing.T) {
		quota, err := ParseCodexUsagePayload([]byte(`{"rate_limit":{"primary_window":{"used_percent":85,"reset_after_seconds":172800}}}`), now)
		if err != nil {
			t.Fatal(err)
		}
		if quota.FiveHour != nil || quota.LongWindow != nil || quotaNeedsProtectionPause(quota, cfg) {
			t.Fatalf("unknown duration must not create a five-hour or weekly quota: %#v", quota)
		}
	})
	t.Run("SSE primary weekly replaces stale five-hour", func(t *testing.T) {
		store := weeklyOnlyObservationStore(now)
		frame := []byte("event: codex.rate_limits\ndata: " +
			`{"rate_limits":{"primary":{"used_percent":85,"window_minutes":10080,"reset_after_seconds":172800}}}` + "\n\n")
		applyObservedRateLimits(store, "team", "idx-team", frame, now)
		account := weeklyOnlyAccountByAuthID(t, store.Snapshot(now), "team")
		checkWeeklyOnlyQuota(t, account.Quota, cfg)
		if account.Family != AccountFamilyWeekly {
			t.Fatalf("family = %q, want weekly", account.Family)
		}
		groups := quotaFetchResponseForAuth(store, pluginapi.QuotaFetchRequest{Provider: "codex", AuthID: "team"}, now).Groups
		if len(groups) != 1 || groups[0].Buckets[0].Window != "7d" || groups[0].Buckets[0].RemainingFraction != .15 {
			t.Fatalf("quota fetch must expose weekly group only: %#v", groups)
		}
	})
	t.Run("headers primary weekly replaces stale five-hour", func(t *testing.T) {
		store := weeklyOnlyObservationStore(now)
		applyObservedCodexHeaders(store, pluginapi.UsageRecord{
			Provider: "codex", AuthID: "team", AuthIndex: "idx-team",
			ResponseHeaders: map[string][]string{
				"X-Codex-Primary-Used-Percent":        {"85"},
				"X-Codex-Primary-Window-Minutes":      {"10080"},
				"X-Codex-Primary-Reset-After-Seconds": {"172800"},
			},
		}, now)
		checkWeeklyOnlyQuota(t, weeklyOnlyAccountByAuthID(t, store.Snapshot(now), "team").Quota, cfg)
	})
	t.Run("SSE dual-window stays dual", func(t *testing.T) {
		store := NewPluginState(DefaultConfig())
		store.UpsertQuota(AccountState{AuthID: "team", AuthIndex: "idx-team", Provider: "codex"})
		frame := []byte("event: codex.rate_limits\ndata: " +
			`{"rate_limits":{"primary":{"used_percent":30,"window_minutes":300,"reset_after_seconds":3600},"secondary":{"used_percent":40,"window_minutes":10080,"reset_after_seconds":86400}}}` + "\n\n")
		applyObservedRateLimits(store, "team", "idx-team", frame, now)
		quota := weeklyOnlyAccountByAuthID(t, store.Snapshot(now), "team").Quota
		if quota.FiveHour == nil || quota.LongWindow == nil || quota.Family != AccountFamilyWeekly ||
			*quota.FiveHour.UsedPercent != 30 || *quota.LongWindow.UsedPercent != 40 {
			t.Fatalf("dual-window parsing regressed: %#v", quota)
		}
	})
	t.Run("SSE unknown duration ignored", func(t *testing.T) {
		store := NewPluginState(DefaultConfig())
		store.UpsertQuota(AccountState{AuthID: "team", AuthIndex: "idx-team", Provider: "codex"})
		frame := []byte("event: codex.rate_limits\ndata: " +
			`{"rate_limits":{"primary":{"used_percent":85,"reset_after_seconds":172800}}}` + "\n\n")
		applyObservedRateLimits(store, "team", "idx-team", frame, now)
		quota := weeklyOnlyAccountByAuthID(t, store.Snapshot(now), "team").Quota
		if quota.FiveHour != nil || quota.LongWindow != nil {
			t.Fatalf("duration-less frame invented quota: %#v", quota)
		}
	})
}

func TestMonthlyOnlyCodexObservedPrimary(t *testing.T) {
	now := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)
	store := NewPluginState(DefaultConfig())
	store.UpsertQuota(AccountState{AuthID: "monthly", Provider: "codex"})
	frame := []byte("event: codex.rate_limits\ndata: " +
		`{"rate_limits":{"primary":{"used_percent":35,"window_minutes":43200,"reset_after_seconds":864000}}}` + "\n\n")
	applyObservedRateLimits(store, "monthly", "", frame, now)
	quota := weeklyOnlyAccountByAuthID(t, store.Snapshot(now), "monthly").Quota
	if quota.FiveHour != nil || quota.LongWindow == nil || quota.LongWindow.Kind != WindowMonthly || quota.Family != AccountFamilyMonthly {
		t.Fatalf("monthly-only account must not have five-hour quota: %#v", quota)
	}
}

func weeklyOnlyObservationStore(now time.Time) *PluginState {
	store := NewPluginState(DefaultConfig())
	fakeFiveUsed, staleWeekUsed := 85.0, 0.0
	store.UpsertQuota(AccountState{
		AuthID: "team", AuthIndex: "idx-team", Provider: "codex", Family: AccountFamilyWeekly,
		Quota: ParsedQuota{
			Family:     AccountFamilyWeekly,
			FiveHour:   &QuotaWindow{Kind: WindowFiveHour, UsedPercent: &fakeFiveUsed, ResetAt: now.Add(time.Hour)},
			LongWindow: &QuotaWindow{Kind: WindowWeekly, UsedPercent: &staleWeekUsed, ResetAt: now.Add(24 * time.Hour)},
		},
	})
	return store
}

func weeklyOnlyAccountByAuthID(t *testing.T, snapshot StateSnapshot, authID string) AccountState {
	t.Helper()
	for _, account := range snapshot.Accounts {
		if account.AuthID == authID {
			return account
		}
	}
	t.Fatalf("account %q not found", authID)
	return AccountState{}
}

func checkWeeklyOnlyQuota(t *testing.T, quota ParsedQuota, cfg Config) {
	t.Helper()
	if quota.FiveHour != nil || quota.LongWindow == nil || quota.LongWindow.Kind != WindowWeekly ||
		quota.Family != AccountFamilyWeekly || quota.LongWindow.UsedPercent == nil ||
		*quota.LongWindow.UsedPercent != 85 {
		t.Fatalf("expected only a 15%% remaining weekly quota: %#v", quota)
	}
	if quotaNeedsProtectionPause(quota, cfg) {
		t.Fatal("15% weekly remainder must not trigger the 20% five-hour pause threshold")
	}
}
