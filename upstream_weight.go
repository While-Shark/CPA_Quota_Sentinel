package main

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// CPA's runtime auth.weight is not CPA priority. The host accepts 0 (disabled
// for weighted routing) through 1,000,000; absent weight means 1.
const maxUpstreamWeight int64 = 1_000_000

type UpstreamWeight struct {
	Weight int64  `json:"weight"`
	Source string `json:"source"`
}

type UpstreamWeightCache struct {
	mu     sync.RWMutex
	byAuth map[string]UpstreamWeight
}

var observedUpstreamWeights = &UpstreamWeightCache{byAuth: make(map[string]UpstreamWeight)}

func readUpstreamWeight(raw any) (int64, error) {
	var s string
	switch v := raw.(type) {
	case string:
		s = strings.TrimSpace(v)
	case json.Number:
		s = v.String()
	case int:
		s = strconv.FormatInt(int64(v), 10)
	case int64:
		s = strconv.FormatInt(v, 10)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v {
			return 0, errors.New("invalid weight")
		}
		s = strconv.FormatFloat(v, 'f', 0, 64)
	default:
		return 0, errors.New("invalid weight")
	}
	parsed, err := strconv.ParseInt(s, 10, 64)
	if err != nil || parsed > maxUpstreamWeight {
		return 0, errors.New("invalid weight")
	}
	if parsed < 0 {
		return 0, nil
	}
	return parsed, nil
}

// Attributes take precedence over metadata exactly as in CPA's runtime
// weighted selector. An invalid explicit value fails closed at weight zero.
func upstreamWeightFromFields(attrs map[string]string, metadata map[string]any) UpstreamWeight {
	if raw, ok := attrs["weight"]; ok && strings.TrimSpace(raw) != "" {
		weight, err := readUpstreamWeight(raw)
		if err != nil {
			return UpstreamWeight{Weight: 0, Source: "invalid"}
		}
		return UpstreamWeight{Weight: weight, Source: "attributes"}
	}
	if raw, ok := metadata["weight"]; ok {
		weight, err := readUpstreamWeight(raw)
		if err != nil {
			return UpstreamWeight{Weight: 0, Source: "invalid"}
		}
		return UpstreamWeight{Weight: weight, Source: "metadata"}
	}
	return UpstreamWeight{Weight: 1, Source: "default"}
}

func upstreamWeightFromCandidate(candidate pluginapi.SchedulerAuthCandidate) UpstreamWeight {
	return upstreamWeightFromFields(candidate.Attributes, candidate.Metadata)
}

func upstreamWeightFromAuthJSON(raw json.RawMessage) UpstreamWeight {
	var body map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &body) != nil {
		return UpstreamWeight{Weight: 1, Source: "unknown"}
	}
	var attrs map[string]string
	var meta map[string]any
	_ = json.Unmarshal(body["attributes"], &attrs)
	_ = json.Unmarshal(body["metadata"], &meta)
	// Auth file top-level weight is used as routing metadata by CPA.
	if value, ok := body["weight"]; ok && len(value) > 0 && string(value) != "null" {
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(string(value)))
		decoder.UseNumber()
		if decoder.Decode(&decoded) == nil {
			if meta == nil {
				meta = make(map[string]any)
			}
			meta["weight"] = decoded
		}
	}
	result := upstreamWeightFromFields(attrs, meta)
	if result.Source == "attributes" || result.Source == "metadata" {
		result.Source = "auth_file"
	}
	return result
}

func (c *UpstreamWeightCache) ObserveCandidate(candidate pluginapi.SchedulerAuthCandidate) {
	if c == nil || candidate.ID == "" {
		return
	}
	w := upstreamWeightFromCandidate(candidate)
	if w.Source == "attributes" || w.Source == "metadata" {
		w.Source = "candidate"
	}
	c.mu.Lock()
	if c.byAuth == nil {
		c.byAuth = make(map[string]UpstreamWeight)
	}
	// A credential that has no weight field in this candidate may still
	// have an explicit upstream value read from its auth file. Retain it
	// until a runtime candidate explicitly reports a different value.
	if previous, ok := c.byAuth[candidate.ID]; ok &&
		w.Source == "default" && (previous.Source == "auth_file" || previous.Source == "invalid") {
		c.mu.Unlock()
		return
	}
	c.byAuth[candidate.ID] = w
	c.mu.Unlock()
}

func (c *UpstreamWeightCache) ObserveCandidates(candidates []pluginapi.SchedulerAuthCandidate) {
	for _, candidate := range candidates {
		c.ObserveCandidate(candidate)
	}
}

// WeightForCandidate always honors an explicit runtime field. When a host
// candidate omits weight, use the read-only auth-file value if available.
func (c *UpstreamWeightCache) WeightForCandidate(candidate pluginapi.SchedulerAuthCandidate) UpstreamWeight {
	current := upstreamWeightFromCandidate(candidate)
	if current.Source != "default" || c == nil {
		return current
	}
	c.mu.RLock()
	observed, ok := c.byAuth[candidate.ID]
	c.mu.RUnlock()
	if ok && (observed.Source == "auth_file" || observed.Source == "invalid") {
		return observed
	}
	return current
}

func (c *UpstreamWeightCache) MergeHostWeights(weights map[string]UpstreamWeight) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byAuth == nil {
		c.byAuth = make(map[string]UpstreamWeight)
	}
	for id, w := range weights {
		if current, found := c.byAuth[id]; !found || (current.Source != "candidate" && current.Source != "invalid") {
			c.byAuth[id] = w
		}
	}
}

func (c *UpstreamWeightCache) Snapshot() map[string]UpstreamWeight {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]UpstreamWeight, len(c.byAuth))
	for id, weight := range c.byAuth {
		out[id] = weight
	}
	return out
}

// Only an authenticated management endpoint calls this method. Credential
// JSON is inspected in memory; never returned, persisted or written to logs.
func loadUpstreamWeights(host HostClient, observed map[string]UpstreamWeight) (map[string]UpstreamWeight, error) {
	result := make(map[string]UpstreamWeight, len(observed))
	for id, weight := range observed {
		result[id] = weight
	}
	if host == nil {
		return result, errors.New("CPA auth API unavailable")
	}
	files, err := host.ListAuths()
	if err != nil {
		return result, err
	}
	for _, file := range files {
		id := file.ID
		if id == "" {
			id = file.Name
		}
		if id == "" {
			continue
		}
		if file.AuthIndex == "" {
			if _, ok := result[id]; !ok {
				result[id] = UpstreamWeight{Weight: 1, Source: "default"}
			}
			continue
		}
		detail, err := host.GetAuth(file.AuthIndex)
		if err != nil {
			// Existing runtime observations are more useful than pretending
			// that an inaccessible auth file has an explicit weight 1.
			if _, exists := result[id]; !exists {
				result[id] = UpstreamWeight{Weight: 1, Source: "unknown"}
			}
			continue
		}
		weight := upstreamWeightFromAuthJSON(detail.JSON)
		if weight.Source == "unknown" {
			if _, exists := result[id]; !exists {
				result[id] = weight
			}
			continue
		}
		// A live runtime candidate reflects CPA's effective attributes,
		// including overrides that may not exist in the stored auth file.
		if existing, ok := result[id]; !ok || (existing.Source != "candidate" && existing.Source != "invalid") {
			result[id] = weight
		}
	}
	return result, nil
}

func weightedEntriesFromCandidates(entries []ManualQueueEntry, byID map[string]pluginapi.SchedulerAuthCandidate) []ManualQueueEntry {
	for i := range entries {
		if c, ok := byID[entries[i].AuthID]; ok {
			entries[i].Weight = int(observedUpstreamWeights.WeightForCandidate(c).Weight)
		} else {
			entries[i].Weight = 0
		}
	}
	return entries
}

// Each provider/model has its own rotation pointer, so a Gemini request
// cannot spend Claude's next slot (nor vice versa).
type ProviderQueueRotations struct {
	mu        sync.Mutex
	rotations map[string]*ManualQueueRotation
}

func (p *ProviderQueueRotations) For(provider, model string) *ManualQueueRotation {
	if p == nil {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(provider)) + "/" + strings.ToLower(strings.TrimSpace(model))
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rotations == nil {
		p.rotations = make(map[string]*ManualQueueRotation)
	}
	if cursor, ok := p.rotations[key]; ok {
		return cursor
	}
	if len(p.rotations) >= 128 {
		// Snapshot refreshes do not reset the cursor, but unbounded model
		// names from requests must not grow the map indefinitely.
		clear(p.rotations)
	}
	cursor := &ManualQueueRotation{}
	p.rotations[key] = cursor
	return cursor
}

func (p *ProviderQueueRotations) Reset() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rotations = make(map[string]*ManualQueueRotation)
}
