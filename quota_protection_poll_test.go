package main

import (
	"testing"
	"time"
)

func TestProtectionPollSchedulesWithoutRecentTraffic(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.QuotaProtectionEnabled = true
	cfg.QuotaProtectionInterval = 5 * time.Minute
	state := NewPluginState(cfg)
	refresher := NewQuotaRefresher(nil, state, func() time.Time { return now })

	delay, scheduled := refresher.nextRefreshLoopDelay()
	if !scheduled || delay != 5*time.Minute {
		t.Fatalf("inactive account protection poll = %v, %v; want 5m", delay, scheduled)
	}

	now = now.Add(time.Minute)
	refresher.advanceProtectionPollDeadline(now)
	delay, _ = refresher.nextRefreshLoopDelay()
	if delay != 4*time.Minute {
		t.Fatalf("early timer must preserve deadline: %v", delay)
	}

	now = now.Add(4 * time.Minute)
	refresher.advanceProtectionPollDeadline(now)
	delay, _ = refresher.nextRefreshLoopDelay()
	if delay != 5*time.Minute {
		t.Fatalf("due timer must be advanced: %v", delay)
	}

	cfg.QuotaProtectionInterval = 10 * time.Minute
	state.ReplaceConfig(cfg)
	delay, _ = refresher.nextRefreshLoopDelay()
	if delay != 10*time.Minute {
		t.Fatalf("updated protection interval was not applied: %v", delay)
	}

	cfg.QuotaProtectionEnabled = false
	state.ReplaceConfig(cfg)
	if delay, scheduled = refresher.nextRefreshLoopDelay(); scheduled {
		t.Fatalf("disabled protection should not keep polling: %v", delay)
	}
}

func TestProtectionIntervalMakesAccountDueWithoutTraffic(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.QuotaProtectionEnabled = true
	cfg.QuotaProtectionInterval = 5 * time.Minute
	cfg.QuotaRefreshInterval = 30 * time.Minute
	account := AccountState{AuthID: "protected-account", LastSuccessAt: now.Add(-6 * time.Minute)}
	if due, reason := accountRefreshDue(account, cfg, now); !due || reason != "refresh_interval_due" {
		t.Fatalf("protection polling skipped due account: due=%t reason=%s", due, reason)
	}
	cfg.QuotaProtectionEnabled = false
	if due, reason := accountRefreshDue(account, cfg, now); due || reason != "" {
		t.Fatalf("disabled protection shortened normal interval: due=%t reason=%s", due, reason)
	}
}
