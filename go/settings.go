// Agent settings loader.
//
// Reads ~/.omnicode/agent/settings.json — the real source of truth for
// providers, endpoints, and API keys. The file is gitignored and must never
// be copied into the repo or into the SQLite DB; we read it live at runtime
// and hand keys to the child process via the environment only.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// AgentProvider is one endpoint + credential pair from settings.json.
type AgentProvider struct {
	BaseURL        string `json:"baseUrl"`
	APIKey         string `json:"apiKey"`
	Label          string `json:"label"`
	ReasoningParam string `json:"reasoningParam"`
}

// AgentModel is one entry of the settings.json `models` alias map. The map key
// is a friendly, provider-prefixed name ("xkiro/mistral-medium-3.5") while `id`
// is the real upstream model id the endpoint actually serves
// ("mistralai/mistral-medium-3.5").
type AgentModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Free     bool   `json:"free"`
}

// AgentSettings is the subset of settings.json the app needs.
type AgentSettings struct {
	DefaultProvider string                   `json:"defaultProvider"`
	DefaultModel    string                   `json:"defaultModel"`
	Providers       map[string]AgentProvider `json:"providers"`
	Models          map[string]AgentModel    `json:"models"`
}

// ProviderView is what the API exposes: never includes the key. The field
// set matches providerDTO so every consumer (the header dropdown, the
// Providers card and the Endpoints card) renders from one shape.
type ProviderView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	BaseURL      string `json:"base_url"`
	HasKey       bool   `json:"has_api_key"`
	IsLocal      bool   `json:"is_local"`
	DefaultModel string `json:"default_model"`
	Enabled      bool   `json:"enabled"`
	Label        string `json:"label,omitempty"`
}

// agentSettings caches the parsed file; settings rarely change mid-run but
// the API re-reads on demand so edits apply without a restart.
var (
	agentMu      sync.RWMutex
	agentCache   *AgentSettings
	agentCachedA time.Time
)

func agentSettingsPath() string {
	return filepath.Join(config.InstallPath, "agent", "settings.json")
}

// loadAgentSettings parses agent/settings.json. A missing file is not an
// error: the app falls back to its own database providers.
func loadAgentSettings() (*AgentSettings, error) {
	path := agentSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s AgentSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Providers == nil {
		s.Providers = map[string]AgentProvider{}
	}
	return &s, nil
}

// getAgentSettings returns the cached settings, re-reading at most once a
// second so a burst of requests does not hammer the disk.
func getAgentSettings() *AgentSettings {
	agentMu.RLock()
	if agentCache != nil && timeSince(agentCachedA) < time.Second {
		s := agentCache
		agentMu.RUnlock()
		return s
	}
	agentMu.RUnlock()

	s, err := loadAgentSettings()
	if err != nil || s == nil {
		agentMu.RLock()
		defer agentMu.RUnlock()
		return agentCache
	}
	agentMu.Lock()
	agentCache = s
	agentCachedA = nowTime()
	agentMu.Unlock()
	return s
}

// invalidateAgentCache forces the next read to hit disk.
func invalidateAgentCache() {
	agentMu.Lock()
	agentCache = nil
	agentMu.Unlock()
}

// agentProviderViews lists every configured provider, key presence only.
// Endpoints come from agent/settings.json; key presence reflects whichever
// source actually holds the credential (SQLite after `omnicode sync-keys`,
// otherwise the file).
func agentProviderViews() []ProviderView {
	s := getAgentSettings()
	if s == nil {
		return []ProviderView{}
	}
	out := make([]ProviderView, 0, len(s.Providers))
	for id, p := range s.Providers {
		// Only the selected default carries a model id; the rest are filled
		// in live from the endpoint's /v1/models list.
		defModel := ""
		if id == s.DefaultProvider {
			// The dropdown compares this to the live model list, which uses
			// upstream ids, not the friendly alias stored in settings.
			defModel = resolveModelID(s.DefaultModel)
		}
		if defModel == "" {
			if declared := modelsForProvider(id); len(declared) > 0 {
				defModel = declared[0]
			}
		}
		out = append(out, ProviderView{
			ID:           id,
			Name:         firstNonEmpty(p.Label, id),
			Kind:         "openai",
			BaseURL:      p.BaseURL,
			HasKey:       agentProviderKey(id) != "",
			IsLocal:      isLocalURL(p.BaseURL),
			DefaultModel: defModel,
			Enabled:      true,
			Label:        p.Label,
		})
	}
	sortProviderViews(out)
	return out
}

// agentProviderKey returns the API key for a provider id, reading SQLite
// first (the synced source) and falling back to agent/settings.json.
func agentProviderKey(id string) string {
	_, key := providerCreds(id)
	return key
}

// resolveModelID translates a model reference into the id the endpoint expects.
//
// settings.json stores friendly aliases like "xkiro/mistral-medium-3.5" in
// `defaultModel`, but the API only accepts the upstream id
// ("mistralai/mistral-medium-3.5"). Values that are already real ids, or that
// we cannot resolve, are passed through unchanged.
func resolveModelID(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	s := getAgentSettings()
	if s == nil || len(s.Models) == 0 {
		return ref
	}
	// 1. Exact key match in the alias map.
	if m, ok := s.Models[ref]; ok && m.ID != "" {
		return m.ID
	}
	// 2. Any alias whose `id` equals ref (ref is already a real id).
	for _, m := range s.Models {
		if m.ID == ref {
			return ref
		}
	}
	return resolveAgainst(s.Models, ref)
}

// resolveAgainst applies the alias map. A suffix match is used only when
// every matching alias points at the same upstream id. Two providers often
// share a slug ("llama-3.3-70b") with different ids; picking one at random
// sends the wrong id and the endpoint 404s.
func resolveAgainst(models map[string]AgentModel, ref string) string {
	slug := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		slug = ref[i+1:]
	}
	matched := map[string]bool{}
	only := ""
	for key, m := range models {
		if m.ID == "" {
			continue
		}
		if strings.HasSuffix(key, "/"+slug) || strings.EqualFold(key, slug) {
			matched[m.ID] = true
			only = m.ID
		}
	}
	if len(matched) == 1 {
		return only
	}
	return ref
}

// modelsForProvider lists the model ids a provider declares in settings.json.
func modelsForProvider(providerID string) []string {
	s := getAgentSettings()
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for key, m := range s.Models {
		if m.ID == "" {
			continue
		}
		if m.Provider != providerID &&
			!strings.HasPrefix(key, providerID+"/") {
			continue
		}
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out
}

func isLocalURL(u string) bool {
	l := strings.ToLower(u)
	return strings.Contains(l, "localhost") || strings.Contains(l, "127.0.0.1")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func timeSince(t time.Time) time.Duration { return time.Since(t) }
func nowTime() time.Time                  { return time.Now() }

// sortProviderViews orders providers by id so the UI list is stable.
func sortProviderViews(v []ProviderView) {
	sort.Slice(v, func(i, j int) bool { return v[i].ID < v[j].ID })
}
