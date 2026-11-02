package main

import (
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestProviderPriorityAndRecovery 验证 CPA 优先级和恢复证据，避免低层抢占或缺失窗口误恢复。
func TestProviderPriorityAndRecovery(t *testing.T) {
	original := globalState.Config()
	cfg := DefaultConfig()
	globalState.ReplaceConfig(cfg)
	t.Cleanup(func() { globalState.ReplaceConfig(original) })
	decision := pickProviderAccount(pluginapi.SchedulerPickRequest{Candidates: []pluginapi.SchedulerAuthCandidate{
		{ID: "priority-low", Priority: 1}, {ID: "priority-high", Priority: 9},
	}}, time.Now())
	if decision.AuthID != "priority-high" {
		t.Fatalf("wrong priority: %+v", decision)
	}
	remaining := 80.0
	lowRemaining := 15.0
	reset := time.Now().Add(time.Hour).Format(time.RFC3339)
	previous := []ProviderQuotaWindow{{Name: "5h", RemainingPercent: &lowRemaining, ResetAt: &reset}}
	fresh := []ProviderQuotaWindow{{Name: "5h", RemainingPercent: &remaining}}
	if providerRecoveryVerified(previous, fresh, cfg, time.Now()) {
		t.Fatal("recovered before reset")
	}
	if providerRecoveryVerified(previous, []ProviderQuotaWindow{{Name: "7d", RemainingPercent: &remaining}}, cfg, time.Now().Add(2*time.Hour)) {
		t.Fatal("recovered with missing window")
	}
	if !providerRecoveryVerified(previous, fresh, cfg, time.Now().Add(2*time.Hour)) {
		t.Fatal("fresh healthy window did not recover after reset")
	}
	if got := sanitizeNetworkError(errors.New("Authorization: Bearer test-secret")); got != "upstream request failed" {
		t.Fatalf("unsafe error: %s", got)
	}
}

func TestPickProviderAccount_Rotation(t *testing.T) {
	now := time.Now()
	// 准备注册表：两个健康的 Claude 账号
	rem80 := 80.0
	globalMultiProviderRegistry.UpdateAccount(ProviderAccount{
		AuthID:      "claude-rot-1",
		Provider:    "claude",
		Priority:    1,
		QuotaStatus: "metered",
		Windows: []ProviderQuotaWindow{
			{Name: "5小时窗口 (5h)", RemainingPercent: &rem80},
		},
	})
	globalMultiProviderRegistry.UpdateAccount(ProviderAccount{
		AuthID:      "claude-rot-2",
		Provider:    "claude",
		Priority:    1,
		QuotaStatus: "metered",
		Windows: []ProviderQuotaWindow{
			{Name: "5小时窗口 (5h)", RemainingPercent: &rem80},
		},
	})

	req := pluginapi.SchedulerPickRequest{
		Provider: "claude",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "claude-rot-1", Provider: "claude", Priority: 1},
			{ID: "claude-rot-2", Provider: "claude", Priority: 1},
		},
	}

	picks := make(map[string]int)
	for i := 0; i < 10; i++ {
		decision := pickProviderAccount(req, now)
		if !decision.Handled || decision.Reject || decision.AuthID == "" {
			t.Fatalf("unexpected pick decision: %+v", decision)
		}
		picks[decision.AuthID]++
	}

	// 验证两个账号均被轮换选中（10 次调度各 5 次）
	if picks["claude-rot-1"] == 0 || picks["claude-rot-2"] == 0 {
		t.Fatalf("both accounts should be rotated, got picks = %+v", picks)
	}
}

func TestPickProviderAccount_LowQuotaProtection(t *testing.T) {
	now := time.Now()
	// 启用额度保护
	origCfg := globalState.Config()
	cfg := origCfg
	cfg.QuotaProtectionEnabled = true
	cfg.FiveHourRemainingThreshold = 20.0
	cfg.LongWindowRemainingThreshold = 20.0
	cfg.QuotaRecoveryThreshold = 30.0
	globalState.ReplaceConfig(cfg)
	defer globalState.ReplaceConfig(origCfg)

	remLow := 15.0  // 低于 20%
	remHigh := 70.0 // 高于 20%

	globalMultiProviderRegistry.UpdateAccount(ProviderAccount{
		AuthID:      "claude-low",
		Provider:    "claude",
		Priority:    1,
		QuotaStatus: "metered",
		Windows: []ProviderQuotaWindow{
			{Name: "5小时窗口 (5h)", RemainingPercent: &remLow},
		},
	})
	globalMultiProviderRegistry.UpdateAccount(ProviderAccount{
		AuthID:      "claude-healthy",
		Provider:    "claude",
		Priority:    1,
		QuotaStatus: "metered",
		Windows: []ProviderQuotaWindow{
			{Name: "5小时窗口 (5h)", RemainingPercent: &remHigh},
		},
	})

	req := pluginapi.SchedulerPickRequest{
		Provider: "claude",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "claude-low", Provider: "claude", Priority: 1},
			{ID: "claude-healthy", Provider: "claude", Priority: 1},
		},
	}

	// 多次调度，低额度账号必须被排除，健康账号必须被选中
	for i := 0; i < 5; i++ {
		decision := pickProviderAccount(req, now)
		if decision.AuthID != "claude-healthy" {
			t.Fatalf("expected claude-healthy, got %q (decision: %+v)", decision.AuthID, decision)
		}
	}
}

func TestPickProviderAccount_AllLowQuotaRejected(t *testing.T) {
	now := time.Now()
	origCfg := globalState.Config()
	cfg := origCfg
	cfg.QuotaProtectionEnabled = true
	cfg.FiveHourRemainingThreshold = 20.0
	globalState.ReplaceConfig(cfg)
	defer globalState.ReplaceConfig(origCfg)

	remLow1 := 10.0
	remLow2 := 5.0

	globalMultiProviderRegistry.UpdateAccount(ProviderAccount{
		AuthID:      "ag-low-1",
		Provider:    "antigravity",
		Priority:    1,
		QuotaStatus: "metered",
		Windows: []ProviderQuotaWindow{
			{Name: "Sonnet 5h", RemainingPercent: &remLow1},
		},
	})
	globalMultiProviderRegistry.UpdateAccount(ProviderAccount{
		AuthID:      "ag-low-2",
		Provider:    "antigravity",
		Priority:    1,
		QuotaStatus: "metered",
		Windows: []ProviderQuotaWindow{
			{Name: "Sonnet 5h", RemainingPercent: &remLow2},
		},
	})

	req := pluginapi.SchedulerPickRequest{
		Provider: "antigravity",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "ag-low-1", Provider: "antigravity"},
			{ID: "ag-low-2", Provider: "antigravity"},
		},
	}

	decision := pickProviderAccount(req, now)
	if !decision.Handled || !decision.Reject || decision.Reason != "quota_protection" {
		t.Fatalf("expected rejection by quota_protection, got: %+v", decision)
	}
}

func TestPickProviderAccount_UnknownQuotaEligible(t *testing.T) {
	now := time.Now()
	origCfg := globalState.Config()
	cfg := origCfg
	cfg.QuotaProtectionEnabled = true
	globalState.ReplaceConfig(cfg)
	defer globalState.ReplaceConfig(origCfg)

	// 未知额度账号（未探测或无额度接口，如 Gemini/OpenAI）
	globalMultiProviderRegistry.UpdateAccount(ProviderAccount{
		AuthID:      "gemini-std",
		Provider:    "gemini",
		QuotaStatus: "unknown",
		Windows:     nil,
	})

	req := pluginapi.SchedulerPickRequest{
		Provider: "gemini",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "gemini-std", Provider: "gemini"},
		},
	}

	decision := pickProviderAccount(req, now)
	if !decision.Handled || decision.Reject || decision.AuthID != "gemini-std" {
		t.Fatalf("unknown quota accounts must remain eligible, got: %+v", decision)
	}
}

func TestPickProviderAccount_MissingFiveHourWindow(t *testing.T) {
	now := time.Now()
	origCfg := globalState.Config()
	cfg := origCfg
	cfg.QuotaProtectionEnabled = true
	cfg.FiveHourRemainingThreshold = 20.0
	cfg.LongWindowRemainingThreshold = 20.0
	globalState.ReplaceConfig(cfg)
	defer globalState.ReplaceConfig(origCfg)

	// 仅有 7 天窗口（无 5 小时限额）
	rem7d := 60.0
	globalMultiProviderRegistry.UpdateAccount(ProviderAccount{
		AuthID:      "claude-no-5h",
		Provider:    "claude",
		QuotaStatus: "metered",
		Windows: []ProviderQuotaWindow{
			{Name: "7天窗口 (7d)", RemainingPercent: &rem7d},
		},
	})

	req := pluginapi.SchedulerPickRequest{
		Provider: "claude",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "claude-no-5h", Provider: "claude"},
		},
	}

	decision := pickProviderAccount(req, now)
	if !decision.Handled || decision.Reject || decision.AuthID != "claude-no-5h" {
		t.Fatalf("account lacking 5h window must be checked only on available windows, got: %+v", decision)
	}
}

func TestProviderRecoveryValidation(t *testing.T) {
	cfg := Config{
		FiveHourRemainingThreshold:   20.0,
		LongWindowRemainingThreshold: 20.0,
		QuotaRecoveryThreshold:       30.0,
	}

	rem25 := 25.0
	rem35 := 35.0

	// 1. 25% < 30% 恢复阈值 -> 不满足
	wLow := []ProviderQuotaWindow{{Name: "5小时窗口 (5h)", RemainingPercent: &rem25}}
	if providerMeetsRecoveryThreshold(wLow, cfg) {
		t.Fatal("25% should not meet 30% recovery threshold")
	}

	// 2. 35% >= 30% 恢复阈值 -> 满足
	wHigh := []ProviderQuotaWindow{{Name: "5小时窗口 (5h)", RemainingPercent: &rem35}}
	if !providerMeetsRecoveryThreshold(wHigh, cfg) {
		t.Fatal("35% should meet 30% recovery threshold")
	}

	// 3. 多窗口时任一窗口低于恢复阈值 -> 不满足
	wMixed := []ProviderQuotaWindow{
		{Name: "5小时窗口 (5h)", RemainingPercent: &rem35},
		{Name: "7天窗口 (7d)", RemainingPercent: &rem25},
	}
	if providerMeetsRecoveryThreshold(wMixed, cfg) {
		t.Fatal("mixed windows with one below recovery threshold should not recover")
	}
}
