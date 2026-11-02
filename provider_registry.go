package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// MultiProviderRegistry 管理所有上游 Provider 的账号扫描、真实配额适配与状态呈现。
type MultiProviderRegistry struct {
	mu       sync.RWMutex
	accounts map[string]ProviderAccount
}

var globalMultiProviderRegistry = &MultiProviderRegistry{
	accounts: make(map[string]ProviderAccount),
}

// GetAll 返回当前所有已发现账号的快照列表。
func (r *MultiProviderRegistry) GetAll() []ProviderAccount {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.getAllLocked()
}

// Get 返回单个指定 ID 的账号快照。
func (r *MultiProviderRegistry) Get(authID string) (ProviderAccount, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	acc, ok := r.accounts[authID]
	return acc, ok
}

// UpdateAccount 更新单个账号状态并持久化。
func (r *MultiProviderRegistry) UpdateAccount(acc ProviderAccount) {
	r.mu.Lock()
	r.accounts[acc.AuthID] = acc
	r.mu.Unlock()
	_ = r.saveToDisk()
}

// SyncFromHostAuths 从宿主拉取所有 Auth 文件并结合当前 Codex 调度快照同步多 Provider 状态。
// 注意：宿主 IPC/IO 必须在互斥锁外部执行，避免锁内阻塞并发读取。
func (r *MultiProviderRegistry) SyncFromHostAuths(ctx context.Context, host HostClient, snapshot StateSnapshot, now time.Time) ([]ProviderAccount, error) {
	var hostFiles []pluginapi.HostAuthFileEntry
	if host != nil {
		files, err := host.ListAuths()
		if err != nil {
			return r.GetAll(), fmt.Errorf("list host auths: %s", sanitizeNetworkError(err))
		}
		hostFiles = files
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	codexMap := make(map[string]AccountState, len(snapshot.Accounts))
	for _, acc := range snapshot.Accounts {
		codexMap[acc.AuthID] = acc
	}

	// 如果 host 回调不可用，至少保留当前 Codex 快照中的账号
	if len(hostFiles) == 0 {
		for _, codexAcc := range snapshot.Accounts {
			r.accounts[codexAcc.AuthID] = buildCodexProviderAccount(codexAcc, now)
		}
		return r.getAllLocked(), nil
	}

	seen := make(map[string]bool, len(hostFiles))
	for _, file := range hostFiles {
		prov := NormalizeProviderName(file.Provider)
		if prov == "" {
			prov = "unknown"
		}
		authID := file.ID
		if authID == "" {
			authID = file.Name
		}
		seen[authID] = true

		if prov == "codex" {
			if codexAcc, ok := codexMap[authID]; ok {
				acc := buildCodexProviderAccount(codexAcc, now)
				acc.AuthIndex = file.AuthIndex
				acc.Priority = file.Priority
				r.accounts[authID] = acc
			} else {
				r.accounts[authID] = ProviderAccount{
					AuthID:      authID,
					AuthIndex:   file.AuthIndex,
					Provider:    "codex",
					Priority:    file.Priority,
					Disabled:    file.Disabled,
					QuotaStatus: "unknown",
				}
			}
			continue
		}

		// 非 Codex 账号：保留已有探测结果与暂停状态，绝不捏造虚拟配额
		existing, exists := r.accounts[authID]
		quotaStatus := "unknown"
		var windows []ProviderQuotaWindow
		errMsg := ""
		paused := false
		var pauseWindows []ProviderQuotaWindow
		lastProbed := time.Time{}

		if exists {
			if len(existing.Windows) > 0 {
				quotaStatus = existing.QuotaStatus
				windows = existing.Windows
			}
			errMsg = existing.Error
			paused = existing.Paused
			pauseWindows = existing.PauseWindows
			lastProbed = existing.LastProbed
		}
		if file.Disabled {
			quotaStatus = "disabled"
		}

		r.accounts[authID] = ProviderAccount{
			AuthID:       authID,
			AuthIndex:    file.AuthIndex,
			Provider:     prov,
			Type:         file.Type,
			Priority:     file.Priority,
			Disabled:     file.Disabled,
			Paused:       paused,
			PauseWindows: pauseWindows,
			QuotaStatus:  quotaStatus,
			Windows:      windows,
			Error:        errMsg,
			LastProbed:   lastProbed,
		}
	}

	// 清理已从宿主删除的账号
	for id := range r.accounts {
		if !seen[id] {
			delete(r.accounts, id)
		}
	}

	return r.getAllLocked(), nil
}

// RefreshProviderAccounts 同步宿主 Auth 清单，必要时刷新真实额度，并返回管理 UI 契约。
func RefreshProviderAccounts(ctx context.Context, probe bool) ([]ProviderAccount, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	accounts, err := globalMultiProviderRegistry.SyncFromHostAuths(ctx, ABIHostClient{}, globalState.Snapshot(time.Now()), time.Now())
	if err != nil {
		return accounts, err
	}
	if probe {
		globalMultiProviderRegistry.ProbeAll(ctx, ABIHostClient{})
		accounts = globalMultiProviderRegistry.GetAll()
	}
	return accounts, nil
}

func (r *MultiProviderRegistry) getAllLocked() []ProviderAccount {
	out := make([]ProviderAccount, 0, len(r.accounts))
	for _, acc := range r.accounts {
		out = append(out, acc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].AuthID < out[j].AuthID
	})
	return out
}

func buildCodexProviderAccount(acc AccountState, now time.Time) ProviderAccount {
	var windows []ProviderQuotaWindow
	if acc.Quota.FiveHour != nil && acc.Quota.FiveHour.UsedPercent != nil {
		rem := math.Max(0.0, math.Min(100.0, 100.0-*acc.Quota.FiveHour.UsedPercent))
		var resetStr *string
		if !acc.Quota.FiveHour.ResetAt.IsZero() {
			s := acc.Quota.FiveHour.ResetAt.Format(time.RFC3339)
			resetStr = &s
		}
		windows = append(windows, ProviderQuotaWindow{
			Name:             "5小时窗口 (5h)",
			RemainingPercent: &rem,
			ResetAt:          resetStr,
		})
	}
	if acc.Quota.LongWindow != nil && acc.Quota.LongWindow.UsedPercent != nil {
		rem := math.Max(0.0, math.Min(100.0, 100.0-*acc.Quota.LongWindow.UsedPercent))
		var resetStr *string
		if !acc.Quota.LongWindow.ResetAt.IsZero() {
			s := acc.Quota.LongWindow.ResetAt.Format(time.RFC3339)
			resetStr = &s
		}
		name := "周窗口 (7d)"
		if acc.Family == AccountFamilyMonthly {
			name = "月窗口 (30d)"
		}
		windows = append(windows, ProviderQuotaWindow{
			Name:             name,
			RemainingPercent: &rem,
			ResetAt:          resetStr,
		})
	}

	quotaStatus := "unknown"
	if len(windows) > 0 {
		quotaStatus = "metered"
	}
	paused := acc.QuotaProtectionPaused

	return ProviderAccount{
		AuthID:      acc.AuthID,
		AuthIndex:   acc.AuthIndex,
		Provider:    "codex",
		Priority:    acc.Priority,
		Paused:      paused,
		QuotaStatus: quotaStatus,
		Windows:     windows,
	}
}

// ProbeAccount 对指定的非 Codex 账号执行真实在线额度探测。
func (r *MultiProviderRegistry) ProbeAccount(ctx context.Context, host HostClient, authID string) error {
	if host == nil {
		return fmt.Errorf("host client is nil")
	}
	r.mu.RLock()
	account, ok := r.accounts[authID]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("account %s not found in registry", authID)
	}

	if account.Provider == "codex" {
		return nil // Codex 由已有刷新器调度管辖
	}

	authResp, err := host.GetAuth(account.AuthIndex)
	if err != nil {
		account.QuotaStatus = "error"
		account.Error = sanitizeNetworkError(err)
		r.UpdateAccount(account)
		return err
	}

	token, projectID := ExtractAuthToken(authResp.JSON)
	if token == "" {
		errNoToken := fmt.Errorf("no access token found in auth credentials")
		account.QuotaStatus = "error"
		account.Error = errNoToken.Error()
		r.UpdateAccount(account)
		return errNoToken
	}

	var windows []ProviderQuotaWindow
	switch account.Provider {
	case "claude":
		windows, err = ProbeClaudeAccount(ctx, token, host.Do)
	case "antigravity":
		windows, err = ProbeAntigravityAccount(ctx, token, projectID, host.Do)
	default:
		return fmt.Errorf("provider %s does not support quota probing", account.Provider)
	}

	if err != nil {
		account.QuotaStatus = "error"
		account.Error = sanitizeNetworkError(err)
		r.UpdateAccount(account)
		return err
	}

	previousWindows := account.PauseWindows
	account.QuotaStatus = "metered"
	account.Windows = windows
	account.Error = ""
	account.LastProbed = time.Now()

	cfg := globalState.Config()
	if cfg.QuotaProtectionEnabled {
		if account.Paused {
			// 使用首次触发暂停的窗口快照，后续轮询不能改写恢复证据。
			if cfg.QuotaAutoRecover && providerRecoveryVerified(previousWindows, windows, cfg, time.Now()) {
				account.Paused = false
				account.PauseWindows = nil
			}
		} else if providerNeedsProtectionPause(windows, cfg) {
			account.Paused = true
			account.PauseWindows = append([]ProviderQuotaWindow(nil), windows...)
		}
	}

	r.UpdateAccount(account)
	return nil
}

// ProbeAll 探测所有支持在线额度的非 Codex 账号。
func (r *MultiProviderRegistry) ProbeAll(ctx context.Context, host HostClient) {
	if host == nil {
		return
	}
	accounts := r.GetAll()
	for _, acc := range accounts {
		if acc.Provider == "claude" || acc.Provider == "antigravity" {
			_ = r.ProbeAccount(ctx, host, acc.AuthID)
		}
	}
}

func providerStateDiskPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "CLIProxyAPI", "codex-quota-scheduler", "provider_state.json")
}

func (r *MultiProviderRegistry) saveToDisk() error {
	r.mu.RLock()
	data, err := json.MarshalIndent(r.accounts, "", "  ")
	r.mu.RUnlock()
	if err != nil {
		return err
	}
	p := providerStateDiskPath()
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	return os.WriteFile(p, data, 0644)
}

func (r *MultiProviderRegistry) loadFromDisk() {
	p := providerStateDiskPath()
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var loaded map[string]ProviderAccount
	if err := json.Unmarshal(data, &loaded); err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range loaded {
		if _, exists := r.accounts[k]; !exists {
			r.accounts[k] = v
		}
	}
}
