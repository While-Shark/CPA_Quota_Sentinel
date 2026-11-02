package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestXAIBillingUsesRealWeeklyQuota(t *testing.T) {
	body := []byte(`{
		"config": {
			"creditUsagePercent": 42.5,
			"currentPeriod": {
				"type": "USAGE_PERIOD_TYPE_WEEKLY",
				"end": "2026-11-09T10:00:00Z"
			},
			"productUsage": [
				{"product": "PRODUCT_GROK_BUILD", "usagePercent": 61.2}
			]
		}
	}`)
	windows, err := ParseXAIBilling(body)
	if err != nil || len(windows) != 1 {
		t.Fatalf("parse weekly Grok Build quota: windows=%+v err=%v", windows, err)
	}
	if got := *windows[0].RemainingPercent; got < 38.79 || got > 38.81 {
		t.Fatalf("remaining = %.2f; want 38.8", got)
	}
	if windows[0].Name != "Grok Build · 周额度 (7d)" ||
		windows[0].ResetAt == nil || *windows[0].ResetAt != "2026-11-09T10:00:00Z" {
		t.Fatalf("unexpected window: %+v", windows[0])
	}
}

func TestXAIBillingOldAndMissingQuota(t *testing.T) {
	windows, err := ParseXAIBilling([]byte(`{
		"config": {
			"monthlyLimit": {"val": 2000}, "used": {"val": 1500},
			"billingPeriodEnd": "2026-12-01T00:00:00Z"
		}
	}`))
	if err != nil || len(windows) != 1 || *windows[0].RemainingPercent != 25 {
		t.Fatalf("legacy billing: %+v %v", windows, err)
	}
	for _, raw := range []string{
		`{}`,
		`{"config": {}}`,
		`{"config": {"creditUsagePercent": null}}`,
	} {
		if _, err := ParseXAIBilling([]byte(raw)); !errors.Is(err, errXAIMissingQuota) {
			t.Fatalf("%s should remain unknown: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"config": {"creditUsagePercent": -10}}`,
		`{"config": {"creditUsagePercent": 120}}`,
	} {
		if _, err := ParseXAIBilling([]byte(raw)); err == nil {
			t.Fatalf("out-of-bounds percentage was accepted: %s", raw)
		}
	}
}

func TestXAIProbeReadOnlyAndAuthentication(t *testing.T) {
	var requests []pluginapi.HTTPRequest
	do := func(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
		requests = append(requests, req)
		return pluginapi.HTTPResponse{
			StatusCode: 200,
			Body: []byte(`{"config":{"creditUsagePercent":70,
				"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-11-09T10:00:00Z"}}}`),
		}, nil
	}
	windows, err := ProbeXAIAccount(context.Background(), "test-oauth", "user-test", do)
	if err != nil || len(windows) != 1 || *windows[0].RemainingPercent != 30 {
		t.Fatalf("probe: windows=%+v err=%v", windows, err)
	}
	if len(requests) != 1 || requests[0].Method != "GET" ||
		requests[0].URL != "https://cli-chat-proxy.grok.com/v1/billing?format=credits" ||
		requests[0].Headers["X-XAI-Token-Auth"][0] != "xai-grok-cli" ||
		requests[0].Headers["x-userid"][0] != "user-test" {
		t.Fatalf("incorrect Grok read-only request")
	}
	metadata := []byte(`{"access_token":"test","metadata":{"user_id":"u2", "auth_kind":"oauth"}}`)
	id, apiKey := xaiAuthMetadata(metadata)
	if id != "u2" || apiKey {
		t.Fatalf("xai OAuth metadata: user=%q apiKey=%t", id, apiKey)
	}
	_, apiKey = xaiAuthMetadata([]byte(`{"auth_kind":"api_key", "token":"not-an-oauth-token"}`))
	if !apiKey {
		t.Fatal("API key must not be sent to CLI OAuth billing API")
	}
}

func TestXAISafeErrorsNeverExposeAuthData(t *testing.T) {
	if got := safeXAIProbeError(fmt.Errorf("xai billing returned HTTP 401")); got != "xai billing returned HTTP 401" {
		t.Fatalf("safe HTTP status not displayed: %q", got)
	}
	unsafe := errors.New("HTTP error Authorization: Bearer confidential-token")
	if got := safeXAIProbeError(unsafe); got != "upstream request failed" {
		t.Fatalf("unsafe network text leaked: %q", got)
	}
}

func TestAntigravitySeparateGroupsAndBucketIdentity(t *testing.T) {
	body := []byte(`{
		"groups": [
			{"displayName":"Gemini Models","buckets":[
				{"bucketId":"gemini-weekly","displayName":"Weekly Limit Remaining","remainingFraction":0.9,"resetTime":"2026-11-09T00:00:00Z"},
				{"bucketId":"gemini-5h","displayName":"Five Hour Limit Remaining","remainingFraction":0.95,"resetTime":"2026-11-02T15:00:00Z"}
			]},
			{"displayName":"Claude and GPT models","buckets":[
				{"bucketId":"3p-weekly","displayName":"Weekly Limit Remaining","remainingFraction":0.05,"resetTime":"2026-11-09T00:00:00Z"}
			]},
			{"displayName":"Gemini 4 Argon","buckets":[
				{"bucketId":"gemini-argon-wtu-weekly","displayName":"Gemini 4 Argon Weekly Limit","remainingFraction":0,"resetTime":"2026-11-09T00:00:00Z"}
			]}
		]
	}`)
	windows, err := ParseAntigravityQuota(body)
	if err != nil || len(windows) != 4 {
		t.Fatalf("AGY quota: %+v err=%v", windows, err)
	}
	if windows[0].Group != "gemini" || windows[1].Group != "gemini" ||
		windows[2].Group != "claude-gpt" || windows[3].Group != "gemini-argon" ||
		providerQuotaIdentity(windows[0]) == providerQuotaIdentity(windows[2]) {
		t.Fatalf("wrong AGY model family grouping: %+v", windows)
	}
	cfg := DefaultConfig()
	cfg.QuotaProtectionEnabled = true
	cfg.FiveHourRemainingThreshold = 20
	cfg.LongWindowRemainingThreshold = 40
	acc := ProviderAccount{AuthID: "agy-group-test", Provider: "antigravity", Windows: windows}
	if antigravityGroupBlocked(acc, "gemini-3.8-flash", cfg) {
		t.Fatal("Gemini with 90% weekly quota must stay usable")
	}
	if !antigravityGroupBlocked(acc, "claude-sonnet-5.5", cfg) {
		t.Fatal("Claude/GPT group with 5% weekly must be protected")
	}
	if !antigravityGroupBlocked(acc, "gemini-4-argon", cfg) {
		t.Fatal("Argon group with 0% weekly must be protected")
	}
	acc.Windows = windows[:2]
	if antigravityGroupBlocked(acc, "gemini-4-argon", cfg) {
		t.Fatal("missing Argon quota does not prove exhaustion")
	}
	if providerNeedsProtectionPause(windows[1:2], cfg) {
		t.Fatal("Five Hour Limit Remaining 95% must use five-hour threshold")
	}
}

func TestAntigravityGroupPauseResetsIndependently(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.QuotaProtectionEnabled = true
	cfg.QuotaAutoRecover = true
	cfg.FiveHourRemainingThreshold = 20
	cfg.LongWindowRemainingThreshold = 20
	cfg.QuotaRecoveryThreshold = 30
	past := now.Add(-time.Minute).Format(time.RFC3339)
	future := now.Add(time.Hour).Format(time.RFC3339)
	rem5, rem95, rem0 := 5.0, 95.0, 0.0
	windows := []ProviderQuotaWindow{
		{Group: "gemini", BucketID: "gemini-weekly", Name: "Weekly Limit Remaining", RemainingPercent: &rem95, ResetAt: &future},
		{Group: "claude-gpt", BucketID: "3p-weekly", Name: "Weekly Limit Remaining", RemainingPercent: &rem5, ResetAt: &past},
		{Group: "gemini-argon", BucketID: "gemini-argon-wtu-weekly", Name: "Argon Weekly", RemainingPercent: &rem0, ResetAt: &future},
	}
	paused := advanceAntigravityGroupPauses(nil, windows, cfg, now)
	if len(paused) != 2 || paused["gemini"].Windows != nil {
		t.Fatalf("expected two independent paused groups: %+v", paused)
	}
	// Claude/GPT 周窗口完成重置，Argon 周窗口尚未重置。
	recovered := append([]ProviderQuotaWindow(nil), windows...)
	recovered[1].RemainingPercent = &rem95
	paused = advanceAntigravityGroupPauses(paused, recovered, cfg, now)
	if len(paused) != 1 || paused["gemini-argon"].Windows == nil {
		t.Fatalf("Claude group should recover independently: %+v", paused)
	}
	// 缺失 Argon 桶不能被当作重置成功。
	paused = advanceAntigravityGroupPauses(paused, recovered[:2], cfg, now.Add(2*time.Hour))
	if len(paused) != 1 {
		t.Fatal("a missing group must preserve its protection state")
	}
	// 重置后返回健康额度，才能真正恢复。
	recovered[2].RemainingPercent = &rem95
	paused = advanceAntigravityGroupPauses(paused, recovered, cfg, now.Add(2*time.Hour))
	if len(paused) != 0 {
		t.Fatalf("Argon should recover after reset: %+v", paused)
	}
}

func TestPickAGYOnlyProtectsRequestedModelGroup(t *testing.T) {
	old := globalState.Config()
	cfg := DefaultConfig()
	cfg.QuotaProtectionEnabled = true
	cfg.FiveHourRemainingThreshold = 20
	cfg.LongWindowRemainingThreshold = 20
	globalState.ReplaceConfig(cfg)
	t.Cleanup(func() { globalState.ReplaceConfig(old) })
	rem0, rem90 := 0.0, 90.0
	acc := ProviderAccount{
		AuthID:      "agy-family-route",
		Provider:    "antigravity",
		QuotaStatus: "metered",
		Windows: []ProviderQuotaWindow{
			{Name: "Weekly Limit Remaining", Group: "gemini", BucketID: "gemini-weekly", RemainingPercent: &rem90},
			{Name: "Gemini 4 Argon Weekly Limit", Group: "gemini-argon", BucketID: "gemini-argon-wtu-weekly", RemainingPercent: &rem0},
		},
	}
	globalMultiProviderRegistry.UpdateAccount(acc)
	req := pluginapi.SchedulerPickRequest{
		Provider:   "antigravity",
		Model:      "gemini-3.8-flash",
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: acc.AuthID, Provider: "antigravity"}},
	}
	if got := pickProviderAccount(req, time.Now()); got.Reject || got.AuthID != acc.AuthID {
		t.Fatalf("healthy Gemini falsely blocked: %+v", got)
	}
	req.Model = "gemini-4-argon"
	if got := pickProviderAccount(req, time.Now()); !got.Reject || got.Reason != "quota_protection" {
		t.Fatalf("exhausted Argon should be rejected: %+v", got)
	}
	// 同一个用户同时配了多个 OAuth 账号：不再让一个 Argon 桶消耗影响全部路由。
}

func TestMigrateLegacyAGYPauseOnlyWhenGroupIsProvable(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QuotaProtectionEnabled = true
	cfg.LongWindowRemainingThreshold = 20
	oldLow, healthy := 0.0, 90.0
	reset := "2026-11-09T00:00:00Z"
	old := ProviderQuotaWindow{Name: "Gemini 4 Argon Weekly Limit", RemainingPercent: &oldLow, ResetAt: &reset}
	current := ProviderQuotaWindow{Name: old.Name, Group: "gemini-argon",
		BucketID: "gemini-argon-wtu-weekly", RemainingPercent: &oldLow, ResetAt: &reset}
	acc := ProviderAccount{AuthID: "agy-legacy", Provider: "antigravity",
		Paused: true, PauseWindows: []ProviderQuotaWindow{old}}
	migrateLegacyAntigravityPause(&acc, []ProviderQuotaWindow{
		{Name: "Weekly Limit Remaining", Group: "gemini", BucketID: "gemini-weekly", RemainingPercent: &healthy},
		current,
	}, cfg)
	if acc.Paused || len(acc.PauseWindows) > 0 || acc.GroupPauses["gemini-argon"].Windows == nil {
		t.Fatalf("legacy Argon pause did not migrate to model-scoped protection: %+v", acc)
	}
	if antigravityGroupBlocked(acc, "gemini-3.8-flash", cfg) == true ||
		!antigravityGroupBlocked(acc, "gemini-4-argon", cfg) {
		t.Fatal("migrated legacy pause blocked the wrong model")
	}
	unknown := ProviderAccount{Paused: true, PauseWindows: []ProviderQuotaWindow{
		{Name: "Weekly Limit Remaining", RemainingPercent: &oldLow, ResetAt: &reset},
	}}
	migrateLegacyAntigravityPause(&unknown, []ProviderQuotaWindow{current}, cfg)
	if !unknown.Paused {
		t.Fatal("unknown legacy quota group must not be auto-restored")
	}
	missing := ProviderAccount{Paused: true, PauseWindows: []ProviderQuotaWindow{old}}
	migrateLegacyAntigravityPause(&missing, nil, cfg)
	if !missing.Paused {
		t.Fatal("missing Argon window must retain global legacy pause")
	}
}

func TestXAIRegistryProbeUnknownAndMetered(t *testing.T) {
	reg := &MultiProviderRegistry{accounts: make(map[string]ProviderAccount)}
	now := time.Now()
	host := &mockHostClientForQuota{
		authFiles: []pluginapi.HostAuthFileEntry{
			{ID: "xai-oauth-1", AuthIndex: "index-a", Provider: "xai"},
		},
		auths: map[string]string{
			"index-a": `{"access_token":"test-token","user_id":"test-user","auth_kind":"oauth"}`,
		},
		httpResp: pluginapi.HTTPResponse{
			StatusCode: 200,
			Body:       []byte(`{"config":{"creditUsagePercent":85.0,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-11-08T00:00:00Z"}}}`),
		},
	}
	if _, err := reg.SyncFromHostAuths(context.Background(), host, StateSnapshot{}, now); err != nil {
		t.Fatal(err)
	}
	if err := reg.ProbeAccount(context.Background(), host, "xai-oauth-1"); err != nil {
		t.Fatal(err)
	}
	account, ok := reg.Get("xai-oauth-1")
	if !ok || account.QuotaStatus != "metered" || len(account.Windows) != 1 || *account.Windows[0].RemainingPercent != 15 {
		t.Fatalf("xAI billing account not metered: %+v", account)
	}
	// 上游未返回百分比时保持 unknown 而不是误暂停到零。
	host.httpResp.Body = []byte(`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY"}}}`)
	if err := reg.ProbeAccount(context.Background(), host, "xai-oauth-1"); err != nil {
		t.Fatal(err)
	}
	account, _ = reg.Get("xai-oauth-1")
	if account.QuotaStatus != "unknown" || len(account.Windows) != 0 {
		t.Fatalf("unknown xAI billing must not be treated as zero: %+v", account)
	}
	// API-key 账号不调用 OAuth 订阅接口。
	host.auths["index-a"] = `{"auth_kind":"api_key","token":"test-api-key"}`
	if err := reg.ProbeAccount(context.Background(), host, "xai-oauth-1"); err != nil {
		t.Fatal(err)
	}
	account, _ = reg.Get("xai-oauth-1")
	if account.QuotaStatus != "unknown" {
		t.Fatalf("API key should not be probed as OAuth: %+v", account)
	}
}

func TestProviderGroupPauseSurvivesRegistryJSONRoundTrip(t *testing.T) {
	low := 12.0
	reset := "2026-11-09T00:00:00Z"
	before := ProviderAccount{
		AuthID:   "agy-persisted",
		Provider: "antigravity",
		GroupPauses: map[string]ProviderGroupPause{
			"gemini-argon": {
				Windows: []ProviderQuotaWindow{{
					Name:             "Gemini 4 Argon Weekly Limit",
					BucketID:         "gemini-argon-wtu-weekly",
					Group:            "gemini-argon",
					RemainingPercent: &low,
					ResetAt:          &reset,
				}},
			},
		},
	}
	payload, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	var after ProviderAccount
	if err := json.Unmarshal(payload, &after); err != nil {
		t.Fatal(err)
	}
	pause, ok := after.GroupPauses["gemini-argon"]
	if !ok || len(pause.Windows) != 1 || pause.Windows[0].BucketID != "gemini-argon-wtu-weekly" {
		t.Fatalf("lost protected quota identity after restart: %+v", after)
	}
}

func TestDisabledXAIAccountIsNotProbed(t *testing.T) {
	reg := &MultiProviderRegistry{accounts: map[string]ProviderAccount{
		"manual-disabled": {AuthID: "manual-disabled", AuthIndex: "idx-disabled", Provider: "xai", Disabled: true, QuotaStatus: "disabled"},
	}}
	host := &mockHostClientForQuota{}
	if err := reg.ProbeAccount(context.Background(), host, "manual-disabled"); err != nil {
		t.Fatal(err)
	}
	acc, _ := reg.Get("manual-disabled")
	if !acc.Disabled || acc.QuotaStatus != "disabled" {
		t.Fatal("manually disabled account was altered")
	}
}

// Go 编译器检查 SDK HostAuthGetResponse 的 JSON 契约。
var _ = json.RawMessage{}
