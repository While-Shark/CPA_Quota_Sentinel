package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// recoverProtectionAccount 只有持久化 token、auth marker 和凭据指纹同时匹配才有恢复权限。
func (r *QuotaRefresher) recoverProtectionAccount(ctx context.Context, index string, record QuotaProtectionRecord) error {
	response, err := r.host.GetAuth(index)
	if err != nil {
		return err
	}
	raw, disabled, err := protectionAuthObject(response.JSON)
	if err != nil {
		return err
	}
	if !disabled {
		return r.dropProtectionRecord(index)
	}
	credentials, err := ExtractCodexCredentials(response.JSON)
	var marker string
	_ = json.Unmarshal(raw[quotaProtectionOwnerKey], &marker)
	if err != nil || record.OwnerToken == "" || marker != record.OwnerToken || !record.Fingerprint.ValidHashes() || record.Fingerprint != managedAuthFingerprint(index, credentials) {
		return r.loseProtectionOwnership(index)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 崩溃后的 pending 只能由同次写入的随机 marker 确认，绝不凭 disabled=true 接管。
	if !record.DisableApplied {
		if _, err := r.runtimeStore.Update(func(state *PersistentState) error {
			item := state.QuotaProtection[index]
			item.DisableApplied = true
			state.QuotaProtection[index] = item
			return nil
		}); err != nil {
			return err
		}
	}
	if !r.state.Config().QuotaAutoRecover || !r.state.Config().QuotaProtectionEnabled {
		return nil
	}
	if record.ResetUncertain || record.ResetAt.IsZero() || record.ResetAt.After(r.now()) {
		return nil
	}
	quota, err := r.fetchProtectionQuota(credentials)
	if err != nil {
		return err
	}
	if !protectionWindowsRecovered(quota, r.state.Config(), record.FiveHourRequired, record.LongWindowRequired) {
		r.protectionLog("info", "protection.waiting", "额度复核尚未满足全部受保护窗口的恢复条件", index)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.state.Config().QuotaAutoRecover || !r.state.Config().QuotaProtectionEnabled {
		return nil
	}
	raw["disabled"] = json.RawMessage("false")
	delete(raw, quotaProtectionOwnerKey)
	if err := r.saveProtectionAuth(index, response.Name, response.JSON, raw); err != nil {
		return err
	}
	if err := r.dropProtectionRecord(index); err != nil {
		return err
	}
	r.protectionLog("info", "protection.reenabled", "所有受保护窗口已通过新鲜额度复核，账号已自动恢复", index)
	return nil
}

// fetchProtectionQuota 每次恢复都主动查询上游，不使用缓存或单纯重置时间。
func (r *QuotaRefresher) fetchProtectionQuota(credentials CodexCredentials) (ParsedQuota, error) {
	response, err := r.doBackgroundHTTPRequest(pluginapi.HTTPRequest{Method: http.MethodGet, URL: r.state.Config().QuotaEndpoint, Headers: http.Header{"Authorization": []string{"Bearer " + credentials.AccessToken}, "Chatgpt-Account-Id": []string{credentials.ChatGPTAccountID}, "Content-Type": []string{"application/json"}, "User-Agent": []string{quotaUserAgent}}}, false)
	if err != nil {
		return ParsedQuota{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ParsedQuota{}, errors.New("上游额度查询未成功")
	}
	return ParseCodexUsagePayload(response.Body, r.now())
}

// saveProtectionAuth 写入前重读完整 auth，操作员或凭据变化时放弃本轮写入。
func (r *QuotaRefresher) saveProtectionAuth(index, name string, expected []byte, raw map[string]json.RawMessage) error {
	current, err := r.host.GetAuth(index)
	if err != nil {
		return err
	}
	if !bytes.Equal(current.JSON, expected) {
		return errors.New("auth 已变化，取消额度保护写入")
	}
	next, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if name == "" {
		name = index
	}
	return r.host.SaveAuth(name, next)
}

// deferProtectionCheck 在外部调用之前推进期限，让读取、解析和保存错误都按间隔重试。
func (r *QuotaRefresher) deferProtectionCheck(index string) error {
	now := r.now()
	interval := r.state.Config().QuotaProtectionInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	_, err := r.runtimeStore.Update(func(state *PersistentState) error {
		item, ok := state.QuotaProtection[index]
		if !ok {
			return nil
		}
		item.LastQuotaCheckAt = now
		item.NextQuotaCheckAt = now.Add(interval)
		state.QuotaProtection[index] = item
		return nil
	})
	return err
}

// dropProtectionRecord 完成恢复或观察到人工启用后释放插件所有权。
func (r *QuotaRefresher) dropProtectionRecord(index string) error {
	_, err := r.runtimeStore.Update(func(state *PersistentState) error { delete(state.QuotaProtection, index); return nil })
	return err
}

// loseProtectionOwnership 保留暂停证据但永久停止自动写入，交给操作员明确处理。
func (r *QuotaRefresher) loseProtectionOwnership(index string) error {
	_, err := r.runtimeStore.Update(func(state *PersistentState) error {
		item := state.QuotaProtection[index]
		item.OwnershipLost = true
		item.DisableApplied = false
		state.QuotaProtection[index] = item
		return nil
	})
	if err == nil {
		r.protectionLog("warn", "protection.ownership_lost", "auth 所有权证据不匹配，已放弃自动恢复权限", index)
	}
	return err
}

// protectionLog 仅记录账号索引，不将访问令牌、marker 或凭据指纹写入日志。
func (r *QuotaRefresher) protectionLog(level, event, message, index string) {
	r.state.RecordLog(level, event, message, map[string]any{"auth_index": index}, r.now())
}
