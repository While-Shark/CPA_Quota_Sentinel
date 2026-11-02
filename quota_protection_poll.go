package main

import "time"

// protectionPollDeadline 为启用额度保护的 OAuth 账号提供独立于请求活跃窗口的定期复核。
// 正常调度仍遵守已确认的 CPA 账号清单与刷新退避；这里只安排唤醒，不直接请求上游。
func (r *QuotaRefresher) protectionPollDeadline(now time.Time) time.Time {
	if r == nil || r.state == nil {
		return time.Time{}
	}
	cfg := r.state.Config()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !cfg.QuotaProtectionEnabled {
		r.nextProtectionPollAt = time.Time{}
		r.protectionPollInterval = 0
		return time.Time{}
	}
	interval := cfg.QuotaProtectionInterval
	if interval <= 0 {
		interval = DefaultConfig().QuotaProtectionInterval
	}
	if r.nextProtectionPollAt.IsZero() || r.protectionPollInterval != interval {
		r.nextProtectionPollAt = now.Add(interval)
		r.protectionPollInterval = interval
	}
	return r.nextProtectionPollAt
}

// advanceProtectionPollDeadline 在执行回调前推进唤醒时间，避免故障时产生零延迟忙循环。
func (r *QuotaRefresher) advanceProtectionPollDeadline(now time.Time) {
	if r == nil || r.state == nil {
		return
	}
	cfg := r.state.Config()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !cfg.QuotaProtectionEnabled {
		r.nextProtectionPollAt = time.Time{}
		r.protectionPollInterval = 0
		return
	}
	interval := cfg.QuotaProtectionInterval
	if interval <= 0 {
		interval = DefaultConfig().QuotaProtectionInterval
	}
	if r.nextProtectionPollAt.IsZero() || r.protectionPollInterval != interval || !r.nextProtectionPollAt.After(now) {
		r.nextProtectionPollAt = now.Add(interval)
		r.protectionPollInterval = interval
	}
}
