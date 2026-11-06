package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	maxManualQueueEntries = 500
	maxManualQueueWeight  = 10
)

// normalizeManualQueue makes old user-data files default to the unchanged
// automatic scheduler. Malformed saved entries never create privileged routes.
func normalizeManualQueue(q ManualQueueConfig) ManualQueueConfig {
	out := ManualQueueConfig{Mode: QueueModeAuto}
	if q.Mode == QueueModeManual {
		out.Mode = QueueModeManual
	}
	seen := make(map[string]struct{}, len(q.Entries))
	for _, e := range q.Entries {
		id := strings.TrimSpace(e.AuthID)
		if id == "" || len(id) > 256 || len(out.Entries) >= maxManualQueueEntries {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if e.Weight < 1 || e.Weight > maxManualQueueWeight {
			e.Weight = 1
		}
		out.Entries = append(out.Entries, ManualQueueEntry{AuthID: id, Weight: e.Weight})
	}
	return out
}

// validateManualQueue rejects invalid management writes rather than silently
// changing the user's requested order or frequency.
func validateManualQueue(q ManualQueueConfig) error {
	if q.Mode != QueueModeAuto && q.Mode != QueueModeManual {
		return fmt.Errorf("queue mode must be auto or manual")
	}
	if len(q.Entries) > maxManualQueueEntries {
		return fmt.Errorf("queue supports at most %d accounts", maxManualQueueEntries)
	}
	seen := make(map[string]struct{}, len(q.Entries))
	for _, e := range q.Entries {
		id := strings.TrimSpace(e.AuthID)
		if id == "" || id != e.AuthID || len(id) > 256 {
			return fmt.Errorf("invalid queue auth_id")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate queue auth_id")
		}
		seen[id] = struct{}{}
		if e.Weight < 1 || e.Weight > maxManualQueueWeight {
			return fmt.Errorf("queue weight must be between 1 and %d", maxManualQueueWeight)
		}
	}
	return nil
}

func cloneManualQueue(q ManualQueueConfig) ManualQueueConfig {
	q.Entries = append([]ManualQueueEntry(nil), q.Entries...)
	return q
}

// effectiveManualQueue appends newly discovered accounts at the end with
// weight 1. A removed account cannot be routed because selection still
// requires both a live CPA candidate and a validated, eligible AccountView.
func effectiveManualQueue(q ManualQueueConfig, ids []string) []ManualQueueEntry {
	allowed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			allowed[id] = struct{}{}
		}
	}
	result := make([]ManualQueueEntry, 0, len(allowed))
	for _, e := range q.Entries {
		if _, ok := allowed[e.AuthID]; ok {
			result = append(result, e)
			delete(allowed, e.AuthID)
		}
	}
	missing := make([]string, 0, len(allowed))
	for id := range allowed {
		missing = append(missing, id)
	}
	sort.Strings(missing)
	for _, id := range missing {
		result = append(result, ManualQueueEntry{AuthID: id, Weight: 1})
	}
	return result
}

// ManualQueueRotation is a shared, concurrency-safe cursor. Published
// snapshots keep this pointer across quota refreshes so polling does not
// restart the manual rotation. The cursor itself is ephemeral: configured
// order and weights are persisted, not the count of live requests.
type ManualQueueRotation struct {
	mu        sync.Mutex
	signature string
	index     int
	remaining int
}

func manualQueueSignature(entries []ManualQueueEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(strconv.Itoa(len(e.AuthID)))
		b.WriteByte(':')
		b.WriteString(e.AuthID)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(e.Weight))
		b.WriteByte(';')
	}
	return b.String()
}

func (r *ManualQueueRotation) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signature = ""
	r.index = 0
	r.remaining = 0
}

// Choose consumes exactly one slot when advance=true; preview=false is NOT
// assumed by callers. If a configured account is unavailable, its remaining
// slots are skipped, not used to bypass quota or circuit protection.
func (r *ManualQueueRotation) Choose(entries []ManualQueueEntry, allowed map[string]AccountView, advance bool) (AccountView, bool) {
	if len(entries) == 0 || len(allowed) == 0 {
		return AccountView{}, false
	}
	if r == nil {
		for _, e := range entries {
			if a, ok := allowed[e.AuthID]; ok && e.Weight > 0 {
				return a, true
			}
		}
		return AccountView{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sig := manualQueueSignature(entries)
	index, remaining := r.index, r.remaining
	if r.signature != sig || index >= len(entries) {
		index, remaining = 0, 0
	}
	for tries := 0; tries < len(entries); tries++ {
		e := entries[index]
		if a, ok := allowed[e.AuthID]; ok && e.Weight > 0 {
			if remaining == 0 {
				remaining = e.Weight
			}
			remaining--
			if remaining == 0 {
				index = (index + 1) % len(entries)
			}
			if advance {
				r.signature, r.index, r.remaining = sig, index, remaining
			}
			return a, true
		}
		index = (index + 1) % len(entries)
		remaining = 0
	}
	return AccountView{}, false
}

// selectManualAccount uses the same admission, trial and exclusion checks as
// the automatic picker. Order and weight only operate inside the highest
// currently selectable CPA tier and availability class.
func selectManualAccount(snapshot SchedulerSnapshot, candidates []Candidate, now time.Time, skip map[AuthInstanceID]struct{}, trials *TrialRegistry, advance bool) SelectionResult {
	admitted := make(map[string]struct{}, len(candidates))
	byID := make(map[string]pluginapi.SchedulerAuthCandidate, len(candidates))
	for _, c := range candidates {
		if c.ID == "" || c.Provider != "codex" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(c.Status)) {
		case "disabled", "unavailable", "blocked", "expired":
			continue
		}
		if _, ok := snapshot.ActiveHighestTier[c.ID]; ok {
			weight := observedUpstreamWeights.WeightForCandidate(pluginapi.SchedulerAuthCandidate{ID: c.ID, Provider: c.Provider, Attributes: c.Attributes, Metadata: c.Metadata})
			if weight.Weight <= 0 {
				continue
			}
			admitted[c.ID] = struct{}{}
			byID[c.ID] = pluginapi.SchedulerAuthCandidate{ID: c.ID, Provider: c.Provider, Attributes: c.Attributes, Metadata: c.Metadata}
		}
	}
	bestClass, bestPriority := Excluded, 0
	allowed := make(map[string]AccountView, len(admitted))
	ids := make([]string, 0, len(admitted))
	for _, a := range snapshot.Accounts {
		if _, ok := admitted[a.ID]; !ok {
			continue
		}
		ids = append(ids, a.ID)
		if _, blocked := skip[a.Instance]; blocked {
			continue
		}
		if trials != nil {
			if advance {
				trials.Advance(a.Instance, now)
			}
			a.Trial = trials.State(a.Instance, now)
		}
		class := ClassifyAccount(a, now)
		if class == Excluded {
			continue
		}
		if class < bestClass || (class == bestClass && a.CPAPriority > bestPriority) {
			bestClass, bestPriority = class, a.CPAPriority
			clear(allowed)
		}
		if class == bestClass && a.CPAPriority == bestPriority {
			allowed[a.ID] = a
		}
	}
	if len(allowed) == 0 {
		return SelectionResult{Reason: "no_selectable_account", Fallback: snapshot.Fallback == FallbackFillFirst}
	}
	entries := weightedEntriesFromCandidates(effectiveManualQueue(snapshot.Queue, ids), byID)
	a, ok := snapshot.Rotation.Choose(entries, allowed, advance)
	if !ok {
		return SelectionResult{Reason: "no_selectable_account", Fallback: snapshot.Fallback == FallbackFillFirst}
	}
	result := SelectionResult{AuthID: a.ID, Instance: a.Instance, Class: bestClass, Trial: bestClass == Opportunistic, Reason: "selected"}
	if result.Trial {
		result.EvidenceSource = "trial_evidence"
	}
	return result
}
