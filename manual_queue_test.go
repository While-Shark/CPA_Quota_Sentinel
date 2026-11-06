package main

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func testManualQueueSnapshot() SchedulerSnapshot {
	return SchedulerSnapshot{
		HandleEnabled: true, Queue: ManualQueueConfig{
			Mode:    QueueModeManual,
			Entries: []ManualQueueEntry{{AuthID: "a", Weight: 2}, {AuthID: "b", Weight: 1}, {AuthID: "c", Weight: 3}},
		},
		Rotation:          &ManualQueueRotation{},
		ActiveHighestTier: map[string]struct{}{"a": {}, "b": {}, "c": {}},
		Accounts: []AccountView{
			{ID: "a", Instance: 1, Cache: CacheFresh, CPAPriority: 0},
			{ID: "b", Instance: 2, Cache: CacheFresh, CPAPriority: 0},
			{ID: "c", Instance: 3, Cache: CacheFresh, CPAPriority: 0},
		},
	}
}

func queueTestCandidates() []Candidate {
	return []Candidate{
		{ID: "a", Provider: "codex", Attributes: map[string]string{"weight": "2"}},
		{ID: "b", Provider: "codex", Attributes: map[string]string{"weight": "1"}},
		{ID: "c", Provider: "codex", Attributes: map[string]string{"weight": "3"}},
	}
}

func TestManualQueueWeightedRoundRobin(t *testing.T) {
	snapshot := testManualQueueSnapshot()
	now := time.Now()
	want := []string{"a", "a", "b", "c", "c", "c", "a", "a", "b", "c", "c", "c"}
	for i, id := range want {
		got := SelectAccount(snapshot, queueTestCandidates(), now)
		if got.AuthID != id || got.Class != Preferred {
			t.Fatalf("pick %d got %#v, want %q", i, got, id)
		}
	}
}

func TestManualQueuePreviewDoesNotSpendSlots(t *testing.T) {
	snapshot := testManualQueueSnapshot()
	now := time.Now()
	for i := 0; i < 4; i++ {
		if result := selectManualAccount(snapshot, queueTestCandidates(), now, nil, nil, false); result.AuthID != "a" {
			t.Fatalf("preview spent a rotation slot: %#v", result)
		}
	}
	_ = SelectAccount(snapshot, queueTestCandidates(), now)
	if result := selectManualAccount(snapshot, queueTestCandidates(), now, nil, nil, false); result.AuthID != "a" {
		t.Fatalf("peek after first weighted slot = %#v", result)
	}
	_ = SelectAccount(snapshot, queueTestCandidates(), now)
	if result := selectManualAccount(snapshot, queueTestCandidates(), now, nil, nil, false); result.AuthID != "b" {
		t.Fatalf("peek should advance after weight 2, got %#v", result)
	}
}

func TestManualQueueSkipsUnavailableAndUnadmittedAccounts(t *testing.T) {
	snapshot := testManualQueueSnapshot()
	now := time.Now()
	snapshot.Accounts[0].QuotaProtectionPaused = true
	snapshot.Accounts[1].AuthBlocked = true
	// Unavailable a and b cannot consume requests or bypass guardrails.
	for i := 0; i < 10; i++ {
		if got := SelectAccount(snapshot, queueTestCandidates(), now); got.AuthID != "c" {
			t.Fatalf("unavailable account routed: %#v", got)
		}
	}
	// Even if a priority or weight would favor c, only live CPA candidates
	// inside the admitted tier may be selected.
	snapshot.Accounts[0].QuotaProtectionPaused = false
	snapshot.ActiveHighestTier = map[string]struct{}{"a": {}}
	for i := 0; i < 5; i++ {
		if got := SelectAccount(snapshot, queueTestCandidates(), now); got.AuthID != "a" {
			t.Fatalf("unadmitted auth selected: %#v", got)
		}
	}
	snapshot.ActiveHighestTier = map[string]struct{}{"c": {}}
	if got := SelectAccount(snapshot, []Candidate{{ID: "a", Provider: "codex"}}, now); got.AuthID != "" {
		t.Fatalf("request missing admitted candidate selected auth: %#v", got)
	}
}

func TestManualQueueSkipsDisabledHostCandidates(t *testing.T) {
	snapshot := testManualQueueSnapshot()
	candidates := []Candidate{
		{ID: "a", Provider: "codex", Status: "disabled"},
		{ID: "b", Provider: "codex", Status: "active"},
		{ID: "c", Provider: "codex", Status: "blocked"},
	}
	for i := 0; i < 9; i++ {
		if got := SelectAccount(snapshot, candidates, time.Now()); got.AuthID != "b" {
			t.Fatalf("disabled host candidate selected: %#v", got)
		}
	}
}

func TestManualQueueRespectsCPAPriorityAndAvailabilityClass(t *testing.T) {
	snapshot := testManualQueueSnapshot()
	now := time.Now()
	snapshot.Accounts[2].CPAPriority = 10
	for i := 0; i < 9; i++ {
		if got := SelectAccount(snapshot, queueTestCandidates(), now); got.AuthID != "c" {
			t.Fatalf("lower CPA tier bypassed higher tier: %#v", got)
		}
	}
	snapshot.Accounts[2].AuthBlocked = true
	if got := SelectAccount(snapshot, queueTestCandidates(), now); got.AuthID != "a" {
		t.Fatalf("did not fall through to next CPA tier: %#v", got)
	}
	snapshot.Accounts[0].Cache = CacheUnknown
	if got := SelectAccount(snapshot, queueTestCandidates(), now); got.AuthID != "b" {
		t.Fatalf("preferred healthy account was skipped for opportunistic account: %#v", got)
	}
}

func TestManualQueueConcurrentPicksStayWeighted(t *testing.T) {
	snapshot := testManualQueueSnapshot()
	now := time.Now()
	var wg sync.WaitGroup
	results := make(chan string, 600)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- SelectAccount(snapshot, queueTestCandidates(), now).AuthID
		}()
	}
	wg.Wait()
	close(results)
	counts := make(map[string]int)
	for id := range results {
		counts[id]++
	}
	if counts["a"] != 200 || counts["b"] != 100 || counts["c"] != 300 {
		t.Fatalf("concurrent weighted picks = %#v, want 200/100/300", counts)
	}
}

func TestManualQueueAutoModeIsUnchanged(t *testing.T) {
	snapshot := testManualQueueSnapshot()
	snapshot.Queue.Mode = QueueModeAuto
	snapshot.Accounts[1].PluginPriority = 20
	for i := 0; i < 10; i++ {
		if got := SelectAccount(snapshot, queueTestCandidates(), time.Now()); got.AuthID != "b" {
			t.Fatalf("automatic sorting was changed: %#v", got)
		}
	}
	store := NewPluginState(DefaultConfig())
	if got := store.Annotations().Queue.Mode; got != QueueModeAuto {
		t.Fatalf("existing installations should default to auto, got %q", got)
	}
}

func TestManualQueueNewAccountAndModeReset(t *testing.T) {
	q := ManualQueueConfig{Mode: QueueModeManual, Entries: []ManualQueueEntry{{AuthID: "b", Weight: 3}}}
	entries := effectiveManualQueue(q, []string{"new", "a", "b"})
	if len(entries) != 3 || entries[0].AuthID != "b" || entries[1].AuthID != "a" ||
		entries[2].AuthID != "new" || entries[2].Weight != 1 {
		t.Fatalf("new account was not appended safely: %#v", entries)
	}
	store := NewPluginState(DefaultConfig())
	store.SetAnnotations(AnnotationState{Queue: q})
	_, ok := store.queueRotation.Choose(entries, map[string]AccountView{"b": {ID: "b"}}, true)
	if !ok || store.queueRotation.remaining != 2 {
		t.Fatal("expected first of three weighted slots")
	}
	store.SetAnnotations(AnnotationState{Queue: ManualQueueConfig{Mode: QueueModeAuto, Entries: q.Entries}})
	if store.queueRotation.remaining != 0 {
		t.Fatal("mode switch must reset cursor")
	}
}

func TestManualQueueInvalidConfigRejected(t *testing.T) {
	bad := []ManualQueueConfig{
		{Mode: "unknown"},
		{Mode: QueueModeManual, Entries: []ManualQueueEntry{{AuthID: "a", Weight: 0}}},
		{Mode: QueueModeManual, Entries: []ManualQueueEntry{{AuthID: "a", Weight: 11}}},
		{Mode: QueueModeManual, Entries: []ManualQueueEntry{{AuthID: "a", Weight: 1}, {AuthID: "a", Weight: 2}}},
	}
	for _, q := range bad {
		if err := validateManualQueue(q); err == nil {
			t.Fatalf("invalid queue accepted: %#v", q)
		}
	}
}

func TestManualQueueRejectsInsteadOfDelegatingUnsafeBuiltin(t *testing.T) {
	s := testManualQueueSnapshot()
	s.Fallback = FallbackFillFirst
	for i := range s.Accounts {
		s.Accounts[i].AuthBlocked = true
	}
	previous := publishedSchedulerSnapshot.Load()
	t.Cleanup(func() { publishedSchedulerSnapshot.Store(previous) })
	PublishSchedulerSnapshot(&s)
	req := pluginapi.SchedulerPickRequest{Provider: "codex", Candidates: []pluginapi.SchedulerAuthCandidate{
		{ID: "a", Provider: "codex"}, {ID: "b", Provider: "codex"},
	}}
	decision := schedulerPickPublished(req, time.Now())
	if !decision.Handled || !decision.Reject || decision.DelegateBuiltin != "" || decision.AuthID != "" {
		t.Fatalf("manual mode routed an excluded account via builtin fallback: %#v", decision)
	}
}

func TestManualQueueStatusPreviewTracksRotation(t *testing.T) {
	state := StateSnapshot{Annotations: AnnotationState{Queue: ManualQueueConfig{
		Mode: QueueModeManual, Entries: []ManualQueueEntry{{AuthID: "a", Weight: 2}, {AuthID: "b", Weight: 1}},
	}}, QueueRotation: &ManualQueueRotation{}}
	observedUpstreamWeights.ObserveCandidate(pluginapi.SchedulerAuthCandidate{ID: "a", Attributes: map[string]string{"weight": "2"}})
	observedUpstreamWeights.ObserveCandidate(pluginapi.SchedulerAuthCandidate{ID: "b", Attributes: map[string]string{"weight": "1"}})
	ordered := []ScheduledAccount{
		{AuthID: "a", Available: true, selectionClass: Preferred, selectionView: AccountView{ID: "a"}},
		{AuthID: "b", Available: true, selectionClass: Preferred, selectionView: AccountView{ID: "b"}},
	}
	if next := nextManualStatusAuthID(state, ordered); next != "a" {
		t.Fatalf("first preview = %q", next)
	}
	entry := state.Annotations.Queue.Entries
	allowed := map[string]AccountView{"a": {ID: "a"}, "b": {ID: "b"}}
	state.QueueRotation.Choose(entry, allowed, true)
	if next := nextManualStatusAuthID(state, ordered); next != "a" {
		t.Fatalf("second preview = %q", next)
	}
	state.QueueRotation.Choose(entry, allowed, true)
	if next := nextManualStatusAuthID(state, ordered); next != "b" {
		t.Fatalf("third preview = %q", next)
	}
}

func TestManualQueueManagementPersistsAndExports(t *testing.T) {
	old := defaultStatePath
	dir := t.TempDir()
	defaultStatePath = func() string { return filepath.Join(dir, "state.json") }
	t.Cleanup(func() { defaultStatePath = old })
	store := NewPluginState(DefaultConfig())
	now := time.Now()
	path := "/plugins/cpa-quota-sentinel/queue"
	bad := HandleManagementRequest(store, pluginapi.ManagementRequest{
		Method: "PUT", Path: path, Body: []byte("{\"mode\":\"manual\",\"entries\":[{\"auth_id\":\"a\",\"weight\":0}]}"),
	}, now)
	if bad.StatusCode != 400 {
		t.Fatalf("invalid weight was not rejected: %d", bad.StatusCode)
	}
	if store.Annotations().Queue.Mode != QueueModeAuto {
		t.Fatal("invalid mutation changed scheduler mode")
	}
	desired := ManualQueueConfig{Mode: QueueModeManual,
		Entries: []ManualQueueEntry{{AuthID: "c", Weight: 3}, {AuthID: "a", Weight: 1}},
	}
	body, _ := json.Marshal(desired)
	resp := HandleManagementRequest(store, pluginapi.ManagementRequest{Method: "PUT", Path: path, Body: body}, now)
	if resp.StatusCode != 200 {
		t.Fatalf("queue save failed: %d %s", resp.StatusCode, resp.Body)
	}
	get := HandleManagementRequest(store, pluginapi.ManagementRequest{Method: "GET", Path: path}, now)
	var stored ManualQueueConfig
	if err := json.Unmarshal(get.Body, &stored); err != nil || stored.Mode != QueueModeManual ||
		len(stored.Entries) != 2 || stored.Entries[0].AuthID != "c" {
		t.Fatalf("queue GET mismatch: %#v, %v", stored, err)
	}
	reloaded, loaded, err := loadUserData(semanticStatePaths(defaultStatePath()).UserData)
	if err != nil || !loaded || reloaded.Queue.Mode != QueueModeManual ||
		len(reloaded.Queue.Entries) != 2 || reloaded.Queue.Entries[0].Weight != 3 {
		t.Fatalf("user data lost queue: %#v loaded=%v err=%v", reloaded.Queue, loaded, err)
	}
	exported := HandleManagementRequest(store, pluginapi.ManagementRequest{Method: "GET",
		Path: "/plugins/cpa-quota-sentinel/export"}, now)
	var exportedData PluginDiskState
	if err := json.Unmarshal(exported.Body, &exportedData); err != nil ||
		exportedData.Queue.Mode != QueueModeManual || len(exportedData.Queue.Entries) != 2 {
		t.Fatalf("export lost manual queue: %#v %v", exportedData.Queue, err)
	}
}
