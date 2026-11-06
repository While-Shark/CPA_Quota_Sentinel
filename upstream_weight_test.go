package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestUpstreamWeightMatchesCPAAttributesAndMetadata(t *testing.T) {
	cases := []struct {
		name     string
		attrs    map[string]string
		metadata map[string]any
		want     int64
		source   string
	}{
		{"default", nil, nil, 1, "default"},
		{"auth attribute", map[string]string{"weight": "50"}, nil, 50, "attributes"},
		{"metadata numeric", nil, map[string]any{"weight": float64(7)}, 7, "metadata"},
		{"attribute wins", map[string]string{"weight": "3"}, map[string]any{"weight": float64(100)}, 3, "attributes"},
		{"explicit zero", map[string]string{"weight": "0"}, nil, 0, "attributes"},
		{"negative disables", nil, map[string]any{"weight": -2}, 0, "metadata"},
		{"large accepted", map[string]string{"weight": "1000000"}, nil, 1000000, "attributes"},
		{"too large excluded", map[string]string{"weight": "1000001"}, nil, 0, "invalid"},
		{"invalid excluded", map[string]string{"weight": "abc"}, nil, 0, "invalid"},
		{"fraction excluded", nil, map[string]any{"weight": 3.25}, 0, "invalid"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := upstreamWeightFromFields(tt.attrs, tt.metadata)
			if got.Weight != tt.want || got.Source != tt.source {
				t.Fatalf("weight=%#v want %d/%s", got, tt.want, tt.source)
			}
		})
	}
}

func TestUpstreamWeightCandidateMissingFieldUsesHostValue(t *testing.T) {
	cache := &UpstreamWeightCache{byAuth: map[string]UpstreamWeight{
		"account-a": {Weight: 7, Source: "auth_file"},
		"account-b": {Weight: 0, Source: "auth_file"},
	}}
	withoutWeight := pluginapi.SchedulerAuthCandidate{ID: "account-a", Provider: "codex"}
	if got := cache.WeightForCandidate(withoutWeight); got.Weight != 7 || got.Source != "auth_file" {
		t.Fatalf("missing candidate field overwrote file weight: %#v", got)
	}
	cache.ObserveCandidate(withoutWeight)
	if got := cache.WeightForCandidate(withoutWeight); got.Weight != 7 {
		t.Fatalf("default candidate overwrote explicit host weight: %#v", got)
	}
	if got := cache.WeightForCandidate(pluginapi.SchedulerAuthCandidate{
		ID: "account-b", Attributes: map[string]string{"weight": "3"},
	}); got.Weight != 3 || got.Source != "attributes" {
		t.Fatalf("explicit runtime attribute must override stale auth-file weight: %#v", got)
	}
	if got := cache.WeightForCandidate(pluginapi.SchedulerAuthCandidate{ID: "account-b"}); got.Weight != 0 {
		t.Fatalf("host weight zero should block selection when candidate omits weight: %#v", got)
	}
}

func TestUpstreamWeightsReadFromHostWithoutLeakingAuth(t *testing.T) {
	host := &mockHostClientForQuota{
		authFiles: []pluginapi.HostAuthFileEntry{
			{ID: "codex-a", AuthIndex: "ca", Provider: "codex"},
			{ID: "claude-b", AuthIndex: "cb", Provider: "claude"},
			{ID: "xai-c", AuthIndex: "xc", Provider: "xai"},
			{ID: "missing", AuthIndex: "no", Provider: "gemini"},
		},
		auths: map[string]string{
			"ca": `{"weight":42,"access_token":"SECRET_DO_NOT_RETURN"}`,
			"cb": `{"metadata":{"weight":2,"access_token":"SECOND_SECRET"}}`,
			"xc": `{"attributes":{"weight":"0"},"refresh_token":"ANOTHER_SECRET"}`,
		},
	}
	weights, err := loadUpstreamWeights(host, nil)
	if err != nil {
		t.Fatal(err)
	}
	if weights["codex-a"].Weight != 42 || weights["codex-a"].Source != "auth_file" {
		t.Fatalf("top-level CPA weight not read: %#v", weights["codex-a"])
	}
	if weights["claude-b"].Weight != 2 || weights["xai-c"].Weight != 0 {
		t.Fatalf("other providers not read correctly: %#v", weights)
	}
	serialized, _ := json.Marshal(weights)
	if strings.Contains(string(serialized), "SECRET") {
		t.Fatalf("credential leaked into weight response: %s", serialized)
	}
	weights["codex-a"] = UpstreamWeight{Weight: 60, Source: "candidate"}
	refreshed, _ := loadUpstreamWeights(host, weights)
	if refreshed["codex-a"].Weight != 60 {
		t.Fatalf("stale auth-file value overwrote runtime CPA attributes: %#v", refreshed)
	}
	if refreshed["missing"].Weight != 1 {
		t.Fatalf("unknown auth should not fabricate explicit weight: %#v", refreshed)
	}
}

func TestProviderManualRotationWeightedAndIsolatedByModel(t *testing.T) {
	previousState, previousRegistry := globalState, globalMultiProviderRegistry
	t.Cleanup(func() { globalState, globalMultiProviderRegistry = previousState, previousRegistry })
	globalState = NewPluginState(DefaultConfig())
	globalState.SetAnnotations(AnnotationState{Queue: ManualQueueConfig{
		Mode: QueueModeManual, Entries: []ManualQueueEntry{
			{AuthID: "claude-a", Weight: 1}, {AuthID: "claude-b", Weight: 10},
			{AuthID: "agy-a", Weight: 1}, {AuthID: "agy-b", Weight: 1},
		},
	}})
	globalMultiProviderRegistry = &MultiProviderRegistry{accounts: map[string]ProviderAccount{}}
	claude := pluginapi.SchedulerPickRequest{Provider: "claude", Model: "claude-sonnet", Candidates: []pluginapi.SchedulerAuthCandidate{
		{ID: "claude-a", Provider: "claude", Priority: 2, Attributes: map[string]string{"weight": "2"}},
		{ID: "claude-b", Provider: "claude", Priority: 2, Attributes: map[string]string{"weight": "1"}},
	}}
	agy := pluginapi.SchedulerPickRequest{Provider: "antigravity", Model: "gemini-pro", Candidates: []pluginapi.SchedulerAuthCandidate{
		{ID: "agy-a", Provider: "antigravity", Priority: 0, Attributes: map[string]string{"weight": "3"}},
		{ID: "agy-b", Provider: "antigravity", Priority: 0, Metadata: map[string]any{"weight": float64(1)}},
	}}
	for i, expected := range []string{"claude-a", "claude-a", "claude-b", "claude-a", "claude-a", "claude-b"} {
		if got := pickProviderAccount(claude, time.Now()); got.AuthID != expected {
			t.Fatalf("Claude pick %d = %#v want %s", i, got, expected)
		}
		// AGY traffic must not spend a Claude rotation slot.
		if i < 4 {
			want := []string{"agy-a", "agy-a", "agy-a", "agy-b"}[i]
			if got := pickProviderAccount(agy, time.Now()); got.AuthID != want {
				t.Fatalf("AGY pick %d = %#v want %s", i, got, want)
			}
		}
	}
}

// CPA may advertise Codex as one possible provider while its actual
// candidate list for this model contains only Claude. Manual mode must route
// using the compatible Claude auth, not the Codex-only picker.
func TestManualDispatchUsesActualNonCodexCandidates(t *testing.T) {
	previousState, previousRegistry := globalState, globalMultiProviderRegistry
	t.Cleanup(func() { globalState, globalMultiProviderRegistry = previousState, previousRegistry })
	globalState = NewPluginState(DefaultConfig())
	globalMultiProviderRegistry = &MultiProviderRegistry{accounts: map[string]ProviderAccount{}}
	globalState.SetAnnotations(AnnotationState{Queue: ManualQueueConfig{
		Mode: QueueModeManual, Entries: []ManualQueueEntry{{AuthID: "claude-only", Weight: 1}},
	}})
	req := pluginapi.SchedulerPickRequest{
		Provider: "claude", Providers: []string{"codex", "claude"}, Model: "claude-sonnet",
		Candidates: []pluginapi.SchedulerAuthCandidate{{
			ID: "claude-only", Provider: "claude", Attributes: map[string]string{"weight": "5"},
		}},
	}
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleSchedulerPick(payload)
	if err != nil {
		t.Fatal(err)
	}
	var result envelope
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	var selected pluginapi.SchedulerPickResponse
	if err := json.Unmarshal(result.Result, &selected); err != nil {
		t.Fatal(err)
	}
	if selected.AuthID != "claude-only" || !selected.Handled || selected.Reject {
		t.Fatalf("manual mixed-provider dispatch selected wrong auth: %#v", selected)
	}
}

func TestProviderManualPreservesPriorityAndSkipsDisabledWeightZero(t *testing.T) {
	previousState, previousRegistry := globalState, globalMultiProviderRegistry
	t.Cleanup(func() { globalState, globalMultiProviderRegistry = previousState, previousRegistry })
	globalState = NewPluginState(DefaultConfig())
	globalState.SetAnnotations(AnnotationState{Queue: ManualQueueConfig{
		Mode: QueueModeManual, Entries: []ManualQueueEntry{
			{AuthID: "xai-low", Weight: 1}, {AuthID: "xai-high", Weight: 1},
		},
	}})
	globalMultiProviderRegistry = &MultiProviderRegistry{accounts: map[string]ProviderAccount{}}
	req := pluginapi.SchedulerPickRequest{Provider: "xai", Model: "grok", Candidates: []pluginapi.SchedulerAuthCandidate{
		{ID: "xai-low", Provider: "xai", Priority: 0, Attributes: map[string]string{"weight": "5"}},
		{ID: "xai-high", Provider: "xai", Priority: 10, Attributes: map[string]string{"weight": "1"}},
	}}
	for i := 0; i < 5; i++ {
		if got := pickProviderAccount(req, time.Now()); got.AuthID != "xai-high" {
			t.Fatalf("CPA priority ignored: %#v", got)
		}
	}
	req.Candidates[1].Attributes["weight"] = "0"
	if got := pickProviderAccount(req, time.Now()); got.AuthID != "xai-low" {
		t.Fatalf("weight 0 routed or lower tier not selected: %#v", got)
	}
	req.Candidates[0].Status = "disabled"
	if got := pickProviderAccount(req, time.Now()); !got.Reject || got.AuthID != "" {
		t.Fatalf("manual queue bypassed zero-weight/disabled candidates: %#v", got)
	}
}
