package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestProviderQuotaNormalization(t *testing.T) {
	if !IsCodexProvider("codex") || !IsCodexProvider("CoDeX ") {
		t.Fatal("IsCodexProvider failed for codex")
	}
	if IsCodexProvider("claude") || IsCodexProvider("gemini") {
		t.Fatal("IsCodexProvider should be false for non-codex")
	}
	if got := NormalizeProviderName("Anthropic"); got != "claude" {
		t.Fatalf("NormalizeProviderName(Anthropic) = %q, want claude", got)
	}
	if got := NormalizeProviderName("gemini-cli"); got != "gemini" {
		t.Fatalf("NormalizeProviderName(gemini-cli) = %q, want gemini", got)
	}
	if got := NormalizeProviderName("google"); got != "gemini" {
		t.Fatalf("NormalizeProviderName(google) = %q, want gemini", got)
	}
}

func TestParseClaudeUsageRealContract(t *testing.T) {
	// 真实 Claude usage API 样例响应（包含 5h 与 7d 窗口，utilization 百分比 0-100）
	rawResp := []byte(`{
		"five_hour": {
			"utilization": 25.0,
			"resets_at": "2026-11-01T20:00:00Z"
		},
		"seven_day": {
			"utilization": 60.5,
			"resets_at": "2026-11-08T00:00:00Z"
		},
		"seven_day_sonnet": {
			"utilization": 10.0,
			"resets_at": "2026-11-08T00:00:00Z"
		}
	}`)

	windows, err := ParseClaudeUsage(rawResp)
	if err != nil {
		t.Fatalf("ParseClaudeUsage failed: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("len(windows) = %d, want 3", len(windows))
	}

	// 5h 窗口: remaining = 100 - 25 = 75%
	if windows[0].Name != "5小时窗口 (5h)" || *windows[0].RemainingPercent != 75.0 {
		t.Fatalf("windows[0] = %+v, want 75%% remaining", windows[0])
	}
	if *windows[0].ResetAt != "2026-11-01T20:00:00Z" {
		t.Fatalf("windows[0].ResetAt = %v", *windows[0].ResetAt)
	}

	// 7d 窗口: remaining = 100 - 60.5 = 39.5%
	if windows[1].Name != "7天窗口 (7d)" || *windows[1].RemainingPercent != 39.5 {
		t.Fatalf("windows[1] = %+v, want 39.5%% remaining", windows[1])
	}

	// 错误格式拒绝
	if _, err := ParseClaudeUsage([]byte(`{}`)); err == nil {
		t.Fatal("empty object should fail")
	}
	if _, err := ParseClaudeUsage(nil); err == nil {
		t.Fatal("nil body should fail")
	}
}

func TestParseAntigravityQuotaRealContract(t *testing.T) {
	// 真实 Antigravity retrieveUserQuotaSummary 样例响应
	rawResp := []byte(`{
		"groups": [
			{
				"displayName": "Claude Models",
				"buckets": [
					{
						"displayName": "Claude 3.5 Sonnet",
						"bucketId": "claude-3-5-sonnet",
						"remainingFraction": 0.85,
						"resetTime": "2026-11-01T21:30:00Z"
					},
					{
						"displayName": "Claude 3.5 Haiku",
						"bucketId": "claude-3-5-haiku",
						"remainingFraction": 1.0,
						"resetTime": "2026-11-01T22:00:00Z"
					}
				]
			}
		]
	}`)

	windows, err := ParseAntigravityQuota(rawResp)
	if err != nil {
		t.Fatalf("ParseAntigravityQuota failed: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("len(windows) = %d, want 2", len(windows))
	}

	// Sonnet: remainingFraction 0.85 -> 85%
	if windows[0].Name != "Claude 3.5 Sonnet" || *windows[0].RemainingPercent != 85.0 {
		t.Fatalf("windows[0] = %+v, want 85%% remaining", windows[0])
	}
	if *windows[0].ResetAt != "2026-11-01T21:30:00Z" {
		t.Fatalf("windows[0].ResetAt = %v", *windows[0].ResetAt)
	}

	// Haiku: 100%
	if windows[1].Name != "Claude 3.5 Haiku" || *windows[1].RemainingPercent != 100.0 {
		t.Fatalf("windows[1] = %+v, want 100%% remaining", windows[1])
	}

	if _, err := ParseAntigravityQuota([]byte(`{"groups":[]}`)); err == nil {
		t.Fatal("empty groups should fail")
	}
}

func TestExtractAuthToken(t *testing.T) {
	// 1. 标准 access_token
	tok, pid := ExtractAuthToken([]byte(`{"access_token":"token-123","project_id":"proj-456"}`))
	if tok != "token-123" || pid != "proj-456" {
		t.Fatalf("ExtractAuthToken = (%q, %q)", tok, pid)
	}

	// 2. sessionKey
	tok, _ = ExtractAuthToken([]byte(`{"sessionKey":"sk-ant-test"}`))
	if tok != "sk-ant-test" {
		t.Fatalf("ExtractAuthToken sessionKey = %q", tok)
	}

	// 3. metadata.access_token 与 attributes.gemini_virtual_project
	tok, pid = ExtractAuthToken([]byte(`{
		"metadata": {"access_token": "meta-tok"},
		"attributes": {"gemini_virtual_project": "gvp-789"}
	}`))
	if tok != "meta-tok" || pid != "gvp-789" {
		t.Fatalf("ExtractAuthToken nested = (%q, %q)", tok, pid)
	}
}

type mockHostClientForQuota struct {
	authFiles []pluginapi.HostAuthFileEntry
	auths     map[string]string // authIndex -> rawJSON
	httpResp  pluginapi.HTTPResponse
	httpErr   error
}

func (m *mockHostClientForQuota) ListAuths() ([]pluginapi.HostAuthFileEntry, error) {
	return m.authFiles, nil
}

func (m *mockHostClientForQuota) GetAuth(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	raw, ok := m.auths[authIndex]
	if !ok {
		return pluginapi.HostAuthGetResponse{}, nil
	}
	return pluginapi.HostAuthGetResponse{
		AuthIndex: authIndex,
		JSON:      json.RawMessage(raw),
	}, nil
}

func (m *mockHostClientForQuota) SaveAuth(string, json.RawMessage) error { return nil }

func (m *mockHostClientForQuota) Do(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if m.httpErr != nil {
		return pluginapi.HTTPResponse{}, m.httpErr
	}
	return m.httpResp, nil
}

func (m *mockHostClientForQuota) Log(string, string, map[string]any) {}

func (m *mockHostClientForQuota) ResetRoutingCooldown(string) (pluginapi.HostRoutingResetCooldownResponse, error) {
	return pluginapi.HostRoutingResetCooldownResponse{}, nil
}

func TestMultiProviderRegistrySyncAndProbe(t *testing.T) {
	now := time.Now()
	reg := &MultiProviderRegistry{accounts: make(map[string]ProviderAccount)}

	// 构造宿主 auth 列表，包含 codex, claude, gemini 三个账号
	mockHost := &mockHostClientForQuota{
		authFiles: []pluginapi.HostAuthFileEntry{
			{ID: "codex-1", AuthIndex: "idx-codex", Provider: "codex", Priority: 1},
			{ID: "claude-1", AuthIndex: "idx-claude", Provider: "anthropic", Priority: 2},
			{ID: "gemini-1", AuthIndex: "idx-gemini", Provider: "gemini-cli", Priority: 0},
		},
		auths: map[string]string{
			"idx-claude": `{"access_token":"claude-secret"}`,
		},
		httpResp: pluginapi.HTTPResponse{
			StatusCode: 200,
			Body: []byte(`{
				"five_hour": {"utilization": 20.0, "resets_at": "2026-11-01T20:00:00Z"},
				"seven_day": {"utilization": 40.0, "resets_at": "2026-11-08T00:00:00Z"}
			}`),
		},
	}

	used5h := 15.0
	snapshot := StateSnapshot{
		Accounts: []AccountState{
			{
				AuthID:    "codex-1",
				AuthIndex: "idx-codex",
				Provider:  "codex",
				Priority:  1,
				Quota: ParsedQuota{
					FiveHour: &QuotaWindow{
						UsedPercent: &used5h,
						ResetAt:     now.Add(3 * time.Hour),
					},
				},
			},
		},
	}

	// 1. 同步宿主账号
	accounts, err := reg.SyncFromHostAuths(context.Background(), mockHost, snapshot, now)
	if err != nil {
		t.Fatalf("SyncFromHostAuths failed: %v", err)
	}
	if len(accounts) != 3 {
		t.Fatalf("len(accounts) = %d, want 3", len(accounts))
	}

	// 2. 验证 Codex 账号已自动集成来自调度的配额
	var codexAcc, claudeAcc, geminiAcc ProviderAccount
	for _, a := range accounts {
		switch a.Provider {
		case "codex":
			codexAcc = a
		case "claude":
			claudeAcc = a
		case "gemini":
			geminiAcc = a
		}
	}

	if codexAcc.QuotaStatus != "metered" || len(codexAcc.Windows) != 1 {
		t.Fatalf("codexAcc = %+v, want metered with 1 window", codexAcc)
	}
	if *codexAcc.Windows[0].RemainingPercent != 85.0 {
		t.Fatalf("codex remaining = %v, want 85", *codexAcc.Windows[0].RemainingPercent)
	}

	// 3. 验证未探测前，Claude 与 Gemini 不得捏造配额，也不得标记为 unmetered
	if claudeAcc.QuotaStatus != "unknown" || len(claudeAcc.Windows) != 0 {
		t.Fatalf("unprobed claude must be unknown with empty windows, got %+v", claudeAcc)
	}
	if geminiAcc.QuotaStatus != "unknown" || len(geminiAcc.Windows) != 0 {
		t.Fatalf("gemini must be unknown, got %+v", geminiAcc)
	}

	// 4. 对 Claude 执行真实在线探测
	errProbe := reg.ProbeAccount(context.Background(), mockHost, "claude-1")
	if errProbe != nil {
		t.Fatalf("ProbeAccount claude failed: %v", errProbe)
	}

	probedClaude := reg.GetAll()[0]
	for _, a := range reg.GetAll() {
		if a.AuthID == "claude-1" {
			probedClaude = a
			break
		}
	}
	if probedClaude.QuotaStatus != "metered" || len(probedClaude.Windows) != 2 {
		t.Fatalf("probedClaude = %+v, want metered with 2 windows", probedClaude)
	}
	if *probedClaude.Windows[0].RemainingPercent != 80.0 {
		t.Fatalf("probedClaude 5h remaining = %v, want 80", *probedClaude.Windows[0].RemainingPercent)
	}
}
