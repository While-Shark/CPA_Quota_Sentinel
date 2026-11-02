package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// errXAIMissingQuota 表示上游可访问，但没有返回可用于百分比保护的订阅额度。
var errXAIMissingQuota = errors.New("xai billing quota percentage unavailable")

// ParseXAIBilling 读取 Grok Build 的真实订阅额度。
// 该接口提供周/月账单周期，不承诺存在 Codex 式的五小时窗口。
func ParseXAIBilling(body []byte) ([]ProviderQuotaWindow, error) {
	var payload struct {
		Config *struct {
			CreditUsagePercent *float64 `json:"creditUsagePercent"`
			CurrentPeriod      *struct {
				Type string `json:"type"`
				End  string `json:"end"`
			} `json:"currentPeriod"`
			ProductUsage []struct {
				Product      string   `json:"product"`
				UsagePercent *float64 `json:"usagePercent"`
			} `json:"productUsage"`
			MonthlyLimit *struct {
				Val float64 `json:"val"`
			} `json:"monthlyLimit"`
			Used *struct {
				Val float64 `json:"val"`
			} `json:"used"`
			BillingPeriodEnd string `json:"billingPeriodEnd"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode xai billing: %w", err)
	}
	if payload.Config == nil {
		return nil, errXAIMissingQuota
	}
	config := payload.Config
	used := config.CreditUsagePercent
	// 产品级 Grok Build 周额度更贴合 Grok Build OAuth 的路由请求。
	for _, usage := range config.ProductUsage {
		product := strings.ToLower(usage.Product)
		if (strings.Contains(product, "grok_build") || strings.Contains(product, "grokbuild")) &&
			usage.UsagePercent != nil {
			used = usage.UsagePercent
			break
		}
	}
	if used == nil && config.MonthlyLimit != nil && config.Used != nil &&
		config.MonthlyLimit.Val > 0 {
		value := config.Used.Val / config.MonthlyLimit.Val * 100
		used = &value
	}
	if used == nil {
		return nil, errXAIMissingQuota
	}
	if math.IsNaN(*used) || math.IsInf(*used, 0) || *used < 0 || *used > 100 {
		return nil, fmt.Errorf("invalid xai usage percentage")
	}
	remaining := 100 - *used
	name := "Grok Build · 账单周期额度"
	reset := config.BillingPeriodEnd
	if period := config.CurrentPeriod; period != nil {
		if period.End != "" {
			reset = period.End
		}
		switch {
		case strings.Contains(strings.ToUpper(period.Type), "WEEKLY"):
			name = "Grok Build · 周额度 (7d)"
		case strings.Contains(strings.ToUpper(period.Type), "MONTHLY"):
			name = "Grok Build · 月额度 (30d)"
		}
	}
	var resetPtr *string
	if strings.TrimSpace(reset) != "" {
		resetPtr = &reset
	}
	return []ProviderQuotaWindow{{
		Name:             name,
		BucketID:         "grok-build-included-credits",
		RemainingPercent: &remaining,
		ResetAt:          resetPtr,
	}}, nil
}

// xaiAuthMetadata 只提取可公开使用的身份字段，不返回任何密钥给页面或日志。
func xaiAuthMetadata(authJSON []byte) (userID string, usingAPIKey bool) {
	var raw map[string]any
	if err := json.Unmarshal(authJSON, &raw); err != nil {
		return "", false
	}
	for _, source := range []map[string]any{raw, asJSONObject(raw["metadata"]), asJSONObject(raw["attributes"])} {
		for _, name := range []string{"user_id", "userId", "xai_user_id"} {
			if value, ok := source[name].(string); ok && strings.TrimSpace(value) != "" {
				userID = strings.TrimSpace(value)
				break
			}
		}
		if source["using_api"] == true {
			usingAPIKey = true
		}
		if value, ok := source["using_api"].(string); ok && strings.EqualFold(value, "true") {
			usingAPIKey = true
		}
		if value, ok := source["api_key"].(string); ok && value != "" {
			usingAPIKey = true
		}
		if kind, ok := source["auth_kind"].(string); ok &&
			(kind == "api_key" || kind == "api-key" || kind == "apikey") {
			usingAPIKey = true
		}
	}
	if userID == "" {
		// CPA 的 OAuth auth 文件可将 OIDC subject 保存为 sub。
		if subject, ok := raw["sub"].(string); ok {
			userID = strings.TrimSpace(subject)
		}
	}
	return
}

func asJSONObject(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}

// ProbeXAIAccount 通过 Grok CLI 的只读账单接口获取订阅额度，不发起模型推理。
func ProbeXAIAccount(
	ctx context.Context,
	token string,
	userID string,
	do func(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error),
) ([]ProviderQuotaWindow, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("missing xai access token")
	}
	if do == nil {
		return nil, fmt.Errorf("http client unavailable")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	headers := map[string][]string{
		"Authorization":    {"Bearer " + token},
		"X-XAI-Token-Auth": {"xai-grok-cli"},
		"Accept":           {"application/json"},
	}
	if userID != "" {
		headers["x-userid"] = []string{userID}
	}
	resp, err := do(pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     "https://cli-chat-proxy.grok.com/v1/billing?format=credits",
		Headers: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("xai billing request failed: %s", sanitizeNetworkError(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("xai billing returned HTTP %d", resp.StatusCode)
	}
	return ParseXAIBilling(resp.Body)
}

// safeXAIProbeError 仅允许可验证的 HTTP 状态码进入管理页，不泄露凭据和响应正文。
func safeXAIProbeError(err error) string {
	if err == nil {
		return ""
	}
	const prefix = "xai billing returned HTTP "
	if strings.HasPrefix(err.Error(), prefix) {
		value := strings.TrimPrefix(err.Error(), prefix)
		if status, parseErr := strconv.Atoi(value); parseErr == nil && status >= 100 && status <= 599 {
			return prefix + strconv.Itoa(status)
		}
	}
	return sanitizeNetworkError(err)
}
