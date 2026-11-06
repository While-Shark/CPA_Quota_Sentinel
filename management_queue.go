package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// handlePutManualQueue saves the queue with the existing atomic user-data
// writer. It never edits CPA auth file state or native priority weights.
func handlePutManualQueue(store *PluginState, req pluginapi.ManagementRequest, now time.Time) pluginapi.ManagementResponse {
	var queue ManualQueueConfig
	if err := json.Unmarshal(req.Body, &queue); err != nil {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	if err := validateManualQueue(queue); err != nil {
		return jsonManagementResponse(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	state := store.Annotations()
	state.Queue = cloneManualQueue(queue)
	if err := persistAnnotationState(store, state); err != nil {
		return jsonManagementResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	store.SetAnnotations(state)
	publishSchedulerState(store, nil, now)
	store.RecordLog("info", "ui.manual_queue_saved", "手动队列模式及账号权重已保存", map[string]any{
		"mode": queue.Mode, "account_count": len(queue.Entries),
	}, now)
	return jsonManagementResponse(http.StatusOK, cloneManualQueue(state.Queue))
}

// Status previews inspect the cursor without spending a slot. The same
// highest selectable class/tier rule used in real picks remains in force.
func handleGetQueueWeights(host HostClient) pluginapi.ManagementResponse {
	weights, err := loadUpstreamWeights(host, observedUpstreamWeights.Snapshot())
	if err == nil {
		observedUpstreamWeights.MergeHostWeights(weights)
	}
	payload := map[string]any{"weights": weights}
	if err != nil {
		// Never include host errors, auth-file JSON or credential paths.
		payload["warning"] = "CPA 上游权重读取暂不可用；仅展示已观测权重"
	}
	return jsonManagementResponse(http.StatusOK, payload)
}

func nextManualStatusAuthID(snapshot StateSnapshot, ordered []ScheduledAccount) string {
	bestClass, bestPriority := Excluded, 0
	allowed := make(map[string]AccountView)
	ids := make([]string, 0, len(ordered))
	for _, account := range ordered {
		ids = append(ids, account.AuthID)
		if !account.Available || account.selectionClass == Excluded {
			continue
		}
		if account.selectionClass < bestClass ||
			(account.selectionClass == bestClass && account.CPAPriority > bestPriority) {
			bestClass, bestPriority = account.selectionClass, account.CPAPriority
			clear(allowed)
		}
		if account.selectionClass == bestClass && account.CPAPriority == bestPriority {
			allowed[account.AuthID] = account.selectionView
		}
	}
	entries := effectiveManualQueue(snapshot.Annotations.Queue, ids)
	weights := observedUpstreamWeights.Snapshot()
	for i := range entries {
		if w, exists := weights[entries[i].AuthID]; exists {
			entries[i].Weight = int(w.Weight)
		} else {
			entries[i].Weight = 1
		}
	}
	if next, ok := snapshot.QueueRotation.Choose(entries, allowed, false); ok {
		return next.ID
	}
	return ""
}
