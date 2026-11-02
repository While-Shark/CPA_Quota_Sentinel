package main

import (
	"strings"
	"time"
)

// ProviderQuotaWindow 描述单个账号的配额窗口（如 5 小时、7 天或特定模型配额）。
type ProviderQuotaWindow struct {
	Name             string   `json:"name"`
	RemainingPercent *float64 `json:"remaining_percent"`  // 0.0 - 100.0，未知时为 nil
	ResetAt          *string  `json:"reset_at,omitempty"` // ISO8601 时间戳字符串
}

// ProviderAccount 统一描述来自任一 Provider 的 Auth 账号元数据与真实配额状态。
type ProviderAccount struct {
	AuthID       string                `json:"auth_id"`
	AuthIndex    string                `json:"auth_index"`
	Provider     string                `json:"provider"`
	Type         string                `json:"type,omitempty"`
	Priority     int                   `json:"priority"`
	Disabled     bool                  `json:"disabled"`
	Paused       bool                  `json:"paused"`                  // 额度保护或托管停用时为 true
	PauseWindows []ProviderQuotaWindow `json:"pause_windows,omitempty"` // 首次触发保护时的窗口基线，不能被轮询覆盖
	QuotaStatus  string                `json:"quota_status"`            // "metered", "unknown", "error", "disabled"
	Windows      []ProviderQuotaWindow `json:"windows"`
	Error        string                `json:"error,omitempty"`
	LastProbed   time.Time             `json:"last_probed,omitempty"`
}

// SupportedProvider 描述插件集成的上游认证提供商。
type SupportedProvider struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	QuotaMode   string `json:"quota_mode"` // "windowed", "summary", "unknown"
}

var supportedProvidersList = []SupportedProvider{
	{ID: "codex", DisplayName: "ChatGPT / Codex", QuotaMode: "windowed"},
	{ID: "claude", DisplayName: "Anthropic Claude", QuotaMode: "windowed"},
	{ID: "antigravity", DisplayName: "Google Antigravity", QuotaMode: "summary"},
	{ID: "gemini", DisplayName: "Google Gemini", QuotaMode: "unknown"},
	{ID: "openai", DisplayName: "OpenAI Platform", QuotaMode: "unknown"},
}

// ListSupportedProviders 返回插件支持的 Provider 列表。
func ListSupportedProviders() []SupportedProvider {
	out := make([]SupportedProvider, len(supportedProvidersList))
	copy(out, supportedProvidersList)
	return out
}

// NormalizeProviderName 统一 Provider 命名，忽略大小写与前后空格。
func NormalizeProviderName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "anthropic":
		return "claude"
	case "google", "gemini-cli":
		return "gemini"
	default:
		return lower
	}
}

// IsCodexProvider 判断是否为 Codex / ChatGPT 协议账号。
func IsCodexProvider(provider string) bool {
	return NormalizeProviderName(provider) == "codex"
}
