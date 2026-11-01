package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// ExtractAuthToken 从 auth 凭据 JSON 中提取访问令牌与项目标识。
func ExtractAuthToken(authJSON []byte) (token string, projectID string) {
	if len(authJSON) == 0 {
		return "", ""
	}
	var raw map[string]any
	if err := json.Unmarshal(authJSON, &raw); err != nil {
		return "", ""
	}

	if t, ok := raw["access_token"].(string); ok && strings.TrimSpace(t) != "" {
		token = strings.TrimSpace(t)
	} else if t, ok := raw["token"].(string); ok && strings.TrimSpace(t) != "" {
		token = strings.TrimSpace(t)
	} else if t, ok := raw["sessionKey"].(string); ok && strings.TrimSpace(t) != "" {
		token = strings.TrimSpace(t)
	} else if meta, ok := raw["metadata"].(map[string]any); ok {
		if t, okMeta := meta["access_token"].(string); okMeta {
			token = strings.TrimSpace(t)
		}
	}

	if pid, ok := raw["project_id"].(string); ok && strings.TrimSpace(pid) != "" {
		projectID = strings.TrimSpace(pid)
	} else if inst, ok := raw["installed"].(map[string]any); ok {
		if pid, okInst := inst["project_id"].(string); okInst {
			projectID = strings.TrimSpace(pid)
		}
	} else if web, ok := raw["web"].(map[string]any); ok {
		if pid, okWeb := web["project_id"].(string); okWeb {
			projectID = strings.TrimSpace(pid)
		}
	} else if attr, ok := raw["attributes"].(map[string]any); ok {
		if pid, okAttr := attr["project_id"].(string); okAttr {
			projectID = strings.TrimSpace(pid)
		} else if pid, okGvp := attr["gemini_virtual_project"].(string); okGvp {
			projectID = strings.TrimSpace(pid)
		}
	}

	return token, projectID
}

type claudeWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

// ParseClaudeUsage 解析 Claude 官方 usage API 返回的真实配额窗口。
// API: GET https://api.anthropic.com/api/oauth/usage
func ParseClaudeUsage(body []byte) ([]ProviderQuotaWindow, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty claude usage response")
	}
	var payload struct {
		FiveHour          *claudeWindow `json:"five_hour"`
		SevenDay          *claudeWindow `json:"seven_day"`
		SevenDayOpus      *claudeWindow `json:"seven_day_opus"`
		SevenDaySonnet    *claudeWindow `json:"seven_day_sonnet"`
		SevenDayOAuthApps *claudeWindow `json:"seven_day_oauth_apps"`
		SevenDayCowork    *claudeWindow `json:"seven_day_cowork"`
		IguanaNecktie     *claudeWindow `json:"iguana_necktie"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode claude usage: %w", err)
	}

	var windows []ProviderQuotaWindow
	addWindow := func(name string, w *claudeWindow) {
		if w == nil || w.Utilization == nil {
			return
		}
		rem := math.Max(0.0, math.Min(100.0, 100.0-*w.Utilization))
		windows = append(windows, ProviderQuotaWindow{
			Name:             name,
			RemainingPercent: &rem,
			ResetAt:          w.ResetsAt,
		})
	}

	addWindow("5小时窗口 (5h)", payload.FiveHour)
	addWindow("7天窗口 (7d)", payload.SevenDay)
	addWindow("7天 Opus", payload.SevenDayOpus)
	addWindow("7天 Sonnet", payload.SevenDaySonnet)
	addWindow("7天 OAuth 应用", payload.SevenDayOAuthApps)
	addWindow("7天 Cowork", payload.SevenDayCowork)
	addWindow("7天 Fable", payload.IguanaNecktie)

	if len(windows) == 0 {
		return nil, fmt.Errorf("no valid quota windows in claude usage")
	}
	return windows, nil
}

// ProbeClaudeAccount 真实探测 Claude 账号配额。
func ProbeClaudeAccount(ctx context.Context, token string, do func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)) ([]ProviderQuotaWindow, error) {
	if token == "" {
		return nil, fmt.Errorf("missing claude access token")
	}
	if do == nil {
		return nil, fmt.Errorf("http client unavailable")
	}

	req := pluginapi.HTTPRequest{
		Method: http.MethodGet,
		URL:    "https://api.anthropic.com/api/oauth/usage",
		Headers: map[string][]string{
			"User-Agent":     {"claude-cli/2.1.280 (external, cli)"},
			"Authorization":  {"Bearer " + token},
			"anthropic-beta": {"oauth-2025-04-20"},
			"Accept":         {"application/json"},
		},
	}
	resp, err := do(req)
	if err != nil {
		return nil, fmt.Errorf("claude usage request failed: %s", sanitizeNetworkError(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("claude usage api returned HTTP %d", resp.StatusCode)
	}
	return ParseClaudeUsage(resp.Body)
}

var antigravityProbeBaseURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com",
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

// ParseAntigravityQuota 解析 Antigravity 官方配额汇总返回的数据。
// API: POST https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary
func ParseAntigravityQuota(body []byte) ([]ProviderQuotaWindow, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty antigravity quota response")
	}
	var payload struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Buckets     []struct {
				DisplayName       string   `json:"displayName"`
				BucketID          string   `json:"bucketId"`
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         *string  `json:"resetTime"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode antigravity quota: %w", err)
	}

	var windows []ProviderQuotaWindow
	for _, group := range payload.Groups {
		for _, bucket := range group.Buckets {
			if bucket.RemainingFraction == nil {
				continue
			}
			name := bucket.DisplayName
			if name == "" {
				name = bucket.BucketID
			}
			if name == "" {
				name = group.DisplayName
			}
			rem := math.Max(0.0, math.Min(100.0, *bucket.RemainingFraction*100.0))
			windows = append(windows, ProviderQuotaWindow{
				Name:             name,
				RemainingPercent: &rem,
				ResetAt:          bucket.ResetTime,
			})
		}
	}

	if len(windows) == 0 {
		return nil, fmt.Errorf("no buckets in antigravity quota response")
	}
	return windows, nil
}

// ProbeAntigravityAccount 真实探测 Antigravity 账号配额。
func ProbeAntigravityAccount(ctx context.Context, token string, projectID string, do func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)) ([]ProviderQuotaWindow, error) {
	if token == "" {
		return nil, fmt.Errorf("missing antigravity access token")
	}
	if do == nil {
		return nil, fmt.Errorf("http client unavailable")
	}

	bodyJSON := []byte(`{}`)
	if projectID != "" {
		bodyJSON, _ = json.Marshal(map[string]string{"project": projectID})
	}

	var lastErr error
	for _, baseURL := range antigravityProbeBaseURLs {
		req := pluginapi.HTTPRequest{
			Method: http.MethodPost,
			URL:    baseURL + "/v1internal:retrieveUserQuotaSummary",
			Headers: map[string][]string{
				"User-Agent":    {"antigravity/hub/2.23.0 darwin/arm64"},
				"Authorization": {"Bearer " + token},
				"Content-Type":  {"application/json"},
			},
			Body: bodyJSON,
		}
		resp, err := do(req)
		if err != nil {
			lastErr = fmt.Errorf("network error: %s", sanitizeNetworkError(err))
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return ParseAntigravityQuota(resp.Body)
		}
		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil, fmt.Errorf("antigravity quota probe failed: %w", lastErr)
}

// sanitizeNetworkError 过滤错误信息中的潜在敏感信息或冗长响应正文。
func sanitizeNetworkError(err error) string {
	if err == nil {
		return ""
	}
	// 宿主错误可能包含完整请求和访问令牌，管理接口只暴露固定分类。
	return "upstream request failed"
}
