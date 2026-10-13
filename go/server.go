// OmniCode HTTP server + REST API
// Exposes every settings domain as JSON endpoints. The web UI calls these.
// All requests are local-only (binds to 127.0.0.1).

package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// jsonResponse is the standard wrapper for every API response.
type jsonResponse struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, resp jsonResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeOK(w http.ResponseWriter, data interface{}) {
	writeJSON(w, 200, jsonResponse{OK: true, Data: data})
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, jsonResponse{OK: false, Error: err.Error()})
}

func readJSON(r *http.Request, out interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

// registerRoutes wires every endpoint to the mux.
func registerRoutes(mux *http.ServeMux) {
	// --- meta ---
	mux.HandleFunc("GET /api/health", healthHandler)
	mux.HandleFunc("GET /api/config", configHandler)
	mux.HandleFunc("GET /api/dashboard", dashboardHandler)

	// --- secrets (API keys) ---
	mux.HandleFunc("GET /api/secrets", listSecretsHandler)
	mux.HandleFunc("PUT /api/secrets/{key}", putSecretHandler)
	mux.HandleFunc("DELETE /api/secrets/{key}", deleteSecretHandler)

	// --- settings (app-wide key/value) ---
	mux.HandleFunc("GET /api/settings", listSettingsHandler)
	mux.HandleFunc("PUT /api/settings/{key}", putSettingHandler)
	mux.HandleFunc("DELETE /api/settings/{key}", deleteSettingHandler)

	// --- providers (Anthropic, OpenAI, custom endpoints) ---
	mux.HandleFunc("GET /api/providers", listProvidersHandler)

	// --- sticky provider selection (remembered across restarts) ---
	mux.HandleFunc("GET /api/selection", selectionHandler)
	mux.HandleFunc("POST /api/selection", selectionHandler)
	mux.HandleFunc("POST /api/providers", createProviderHandler)
	mux.HandleFunc("GET /api/providers/{id}", getProviderHandler)
	mux.HandleFunc("PUT /api/providers/{id}", updateProviderHandler)
	mux.HandleFunc("DELETE /api/providers/{id}", deleteProviderHandler)
	mux.HandleFunc("POST /api/providers/{id}/test", testProviderHandler)
	mux.HandleFunc("POST /api/providers/{id}/models", listProviderModelsHandler)

	// --- projects ---
	mux.HandleFunc("GET /api/projects", listProjectsHandler)
	mux.HandleFunc("POST /api/projects", createProjectHandler)
	mux.HandleFunc("GET /api/projects/{id}", getProjectHandler)
	mux.HandleFunc("PUT /api/projects/{id}", updateProjectHandler)
	mux.HandleFunc("DELETE /api/projects/{id}", deleteProjectHandler)
	mux.HandleFunc("POST /api/projects/{id}/scan", scanProjectHandler)

	// --- MCP servers ---
	mux.HandleFunc("GET /api/mcp", listMCPServersHandler)
	mux.HandleFunc("POST /api/mcp", createMCPServerHandler)
	mux.HandleFunc("GET /api/mcp/{id}", getMCPServerHandler)
	mux.HandleFunc("PUT /api/mcp/{id}", updateMCPServerHandler)
	mux.HandleFunc("DELETE /api/mcp/{id}", deleteMCPServerHandler)

	// --- plugins ---
	mux.HandleFunc("GET /api/plugins", listPluginsHandler)
	mux.HandleFunc("POST /api/plugins", createPluginHandler)
	mux.HandleFunc("GET /api/plugins/{id}", getPluginHandler)
	mux.HandleFunc("PUT /api/plugins/{id}", updatePluginHandler)
	mux.HandleFunc("DELETE /api/plugins/{id}", deletePluginHandler)

	// --- skills ---
	mux.HandleFunc("GET /api/skills", listSkillsHandler)
	mux.HandleFunc("POST /api/skills", createSkillHandler)
	mux.HandleFunc("GET /api/skills/{id}", getSkillHandler)
	mux.HandleFunc("PUT /api/skills/{id}", updateSkillHandler)
	mux.HandleFunc("DELETE /api/skills/{id}", deleteSkillHandler)

	// --- agents ---
	mux.HandleFunc("GET /api/agents", listAgentsHandler)
	mux.HandleFunc("POST /api/agents", createAgentHandler)
	mux.HandleFunc("GET /api/agents/{id}", getAgentHandler)
	mux.HandleFunc("PUT /api/agents/{id}", updateAgentHandler)
	mux.HandleFunc("DELETE /api/agents/{id}", deleteAgentHandler)

	// --- sessions (chat history) ---
	mux.HandleFunc("GET /api/sessions", listSessionsHandler)
	mux.HandleFunc("POST /api/sessions", createSessionHandler)
	mux.HandleFunc("GET /api/sessions/{id}", getSessionHandler)
	mux.HandleFunc("POST /api/sessions/{id}/messages", appendMessageHandler)
	mux.HandleFunc("DELETE /api/sessions/{id}", deleteSessionHandler)

	// --- editor: workspace files, plan.md, tabs ---
	// The editor view needs a recursive tree of the active project, plus
	// read/save on individual files, the persistent plan.md, and the
	// per-session tab strip. These are the surface that makes "the IDE
	// works" true at the same level as Claude Code.
	mux.HandleFunc("GET /api/files", listFilesHandler)
	mux.HandleFunc("GET /api/file", readFileHandler)
	mux.HandleFunc("PUT /api/file", writeFileHandler)
	mux.HandleFunc("POST /api/mkfile", mkfileHandler)
	mux.HandleFunc("POST /api/mkdir", mkdirHandler)
	mux.HandleFunc("POST /api/rename", renameHandler)
	mux.HandleFunc("POST /api/move", moveHandler)
	mux.HandleFunc("DELETE /api/remove", removeHandler)
	mux.HandleFunc("POST /api/zip", zipHandler)
	mux.HandleFunc("POST /api/share", shareHandler)
	mux.HandleFunc("POST /api/openwith", openWithHandler)
	mux.HandleFunc("POST /api/convert", convertHandler)
	mux.HandleFunc("POST /api/saveas", saveAsHandler)
	mux.HandleFunc("GET /api/export/session.md", exportSessionHandler)
	mux.HandleFunc("POST /api/import/session.md", importSessionHandler)

	mux.HandleFunc("GET /api/plan", getPlanHandler)
	mux.HandleFunc("PUT /api/plan", putPlanHandler)
	mux.HandleFunc("POST /api/plan/append", appendPlanHandler)
	mux.HandleFunc("GET /api/tabs", listTabsHandler)
	mux.HandleFunc("POST /api/tabs", createTabHandler)
	mux.HandleFunc("DELETE /api/tabs/{id}", deleteTabHandler)

	// --- memory / skill distiller ---
	mux.HandleFunc("GET /api/memory/recent", memoryRecentHandler)
	mux.HandleFunc("GET /api/memory/search", memorySearchHandler)
	mux.HandleFunc("POST /api/memory/remember", memoryRememberHandler)
	mux.HandleFunc("GET /api/skills/learned", listLearnedSkillsHandler)

	// --- repair / scan (dependency scanner) ---
	mux.HandleFunc("POST /api/repair", repairHandler)
	mux.HandleFunc("POST /api/scan", scanHandler)

	// --- audit log ---
	mux.HandleFunc("GET /api/audit", auditHandler)

	// --- REPL proxy (forwards prompts to claw-core) ---
	mux.HandleFunc("POST /api/chat", chatHandler)
	mux.HandleFunc("POST /api/chat/stream", chatStreamHandler)

	mux.HandleFunc("POST /api/terminal", termCreateHandler)
	mux.HandleFunc("GET /api/terminal/{id}/stream", termStreamHandler)
	mux.HandleFunc("POST /api/terminal/{id}/input", termInputHandler)
	mux.HandleFunc("POST /api/terminal/{id}/resize", termResizeHandler)
	mux.HandleFunc("DELETE /api/terminal/{id}", termCloseHandler)

	// --- file attachments ---
	mux.HandleFunc("POST /api/upload", uploadHandler)

	// --- static UI ---
	uiDir := config.WebStaticDir
	_ = os.MkdirAll(uiDir, 0755)
	mux.Handle("/", spaHandler(uiDir))
}

// spaHandler serves a single-page app from disk, falling back to index.html
// for any non-asset path so client-side routing works.
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, strings.TrimPrefix(r.URL.Path, "/"))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		// Fall back to index.html
		r.URL.Path = "/"
		fs.ServeHTTP(w, r)
	})
}

// ============================================================
// Meta
// ============================================================

func healthHandler(w http.ResponseWriter, r *http.Request) {
	// expose enough info that the UI (and the user, when running
	// `omnicode doctor`) can confirm the on-disk binary is the one we just
	// built, and that the editor surface is live.
	exe, _ := os.Executable()
	uiDir := config.WebStaticDir
	editorLive := false
	if st, err := os.Stat(filepath.Join(uiDir, "index.html")); err == nil && st.Size() > 0 {
		// The new Editor nav item / Monaco loader / marked/highlight.js links
		// all live in index.html. Their presence is the cheapest freshness
		// probe we can run from Go — no DOM parsing required.
		if data, err := os.ReadFile(filepath.Join(uiDir, "index.html")); err == nil {
			editorLive = bytes.Contains(data, []byte(`data-view="editor"`)) &&
				bytes.Contains(data, []byte("MonacoLoader")) &&
				bytes.Contains(data, []byte("marked")) &&
				bytes.Contains(data, []byte("highlight.js"))
		}
	}
	coreExe, _ := os.Stat(config.CorePath)
	coreOK := coreExe != nil
	writeOK(w, map[string]interface{}{
		"status":       "ok",
		"version":      "0.3.1",
		"time":         time.Now().UTC().Format(time.RFC3339),
		"binary_path":  exe,
		"ui_dir":       uiDir,
		"editor_live":  editorLive,
		"core_path":    config.CorePath,
		"core_present": coreOK,
		"endpoints": []string{
			"/api/files", "/api/file", "/api/plan", "/api/plan/append",
			"/api/tabs", "/api/chat", "/api/chat/stream",
		},
	})
}

func configHandler(w http.ResponseWriter, r *http.Request) {
	writeOK(w, config)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	type counts struct {
		Secrets   int `json:"secrets"`
		Projects  int `json:"projects"`
		Providers int `json:"providers"`
		MCPS      int `json:"mcp_servers"`
		Plugins   int `json:"plugins"`
		Skills    int `json:"skills"`
		Agents    int `json:"agents"`
		Sessions  int `json:"sessions"`
	}
	var c counts
	_ = readDB(func(db *sql.DB) error {
		for _, q := range []struct {
			table string
			dst   *int
		}{
			{"secrets", &c.Secrets},
			{"projects", &c.Projects},
			{"providers", &c.Providers},
			{"mcp_servers", &c.MCPS},
			{"plugins", &c.Plugins},
			{"skills", &c.Skills},
			{"agents", &c.Agents},
			{"sessions", &c.Sessions},
		} {
			_ = db.QueryRow("SELECT count(*) FROM " + q.table).Scan(q.dst)
		}
		return nil
	})
	writeOK(w, c)
}

// ============================================================
// Secrets
// ============================================================

type secretDTO struct {
	Key       string `json:"key"`
	Masked    string `json:"masked"`
	UpdatedAt string `json:"updated_at"`
}

func listSecretsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := listSecrets()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	out := make([]secretDTO, 0, len(rows))
	for _, k := range rows {
		v, _ := getSecret(k)
		out = append(out, secretDTO{Key: k, Masked: maskSecret(v), UpdatedAt: ""})
	}
	writeOK(w, out)
}

func putSecretHandler(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body struct {
		Value string `json:"value"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := upsertSecret(key, body.Value); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("secret.put", key, "")
	writeOK(w, map[string]string{"key": key})
}

func deleteSecretHandler(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := deleteSecret(key); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("secret.delete", key, "")
	writeOK(w, map[string]string{"key": key})
}

func maskSecret(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 8 {
		return "********"
	}
	return v[:4] + "..." + v[len(v)-4:]
}

// ============================================================
// Settings (app-wide key/value)
// ============================================================

type settingDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func listSettingsHandler(w http.ResponseWriter, r *http.Request) {
	var out []settingDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query("SELECT key, value FROM settings ORDER BY key")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s settingDTO
			if err := rows.Scan(&s.Key, &s.Value); err != nil {
				return err
			}
			out = append(out, s)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeOK(w, out)
}

func putSettingHandler(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var body struct {
		Value string `json:"value"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`, key, body.Value)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("setting.put", key, "")
	writeOK(w, map[string]string{"key": key})
}

func deleteSettingHandler(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM settings WHERE key = ?", key)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("setting.delete", key, "")
	writeOK(w, map[string]string{"key": key})
}

// ============================================================
// Providers
// ============================================================

type providerDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	BaseURL      string `json:"base_url"`
	APIKeyRef    string `json:"api_key_ref"`
	DefaultModel string `json:"default_model"`
	Enabled      bool   `json:"enabled"`
	Metadata     string `json:"metadata"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	HasAPIKey    bool   `json:"has_api_key"`
}

func scanProviders(rows *sql.Rows) ([]providerDTO, error) {
	var out []providerDTO
	for rows.Next() {
		var p providerDTO
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.APIKeyRef,
			&p.DefaultModel, &enabled, &p.Metadata, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Enabled = enabled != 0
		if p.APIKeyRef != "" {
			if v, err := getSecret(p.APIKeyRef); err == nil && v != "" {
				p.HasAPIKey = true
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func listProvidersHandler(w http.ResponseWriter, r *http.Request) {
	// agent/settings.json is the source of truth for endpoints and keys.
	if views := agentProviderViews(); len(views) > 0 {
		writeOK(w, views)
		return
	}
	var out []providerDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, name, kind, base_url, api_key_ref, default_model,
			enabled, metadata, created_at, updated_at FROM providers ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		out, err = scanProviders(rows)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if out == nil {
		out = []providerDTO{}
	}
	writeOK(w, out)
}

// selectionHandler reports (and updates) the sticky provider/model choice.
// GET returns the current selection; POST with {provider_id, model} records
// an explicit user change and pins it for subsequent requests.
func selectionHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeOK(w, map[string]string{
			"provider_id": lastProvider(""),
			"model":       lastModel(""),
		})
	case http.MethodPost:
		var body struct {
			ProviderID string `json:"provider_id"`
			Model      string `json:"model"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, 400, err)
			return
		}
		if body.ProviderID != "" {
			rememberProvider(body.ProviderID)
		}
		if m := usableModel(body.Model); m != "" {
			rememberModel(resolveModelID(m))
		}
		audit("select-provider", body.ProviderID, body.Model)
		writeOK(w, map[string]string{
			"provider_id": lastProvider(""),
			"model":       lastModel(""),
		})
	default:
		writeErr(w, 405, errMethodNotAllowed)
	}
}

var errMethodNotAllowed = &methodError{"method not allowed"}

type methodError struct{ msg string }

func (e *methodError) Error() string { return e.msg }

func createProviderHandler(w http.ResponseWriter, r *http.Request) {
	var p providerDTO
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, err)
		return
	}
	if p.ID == "" {
		p.ID = "prov_" + time.Now().Format("20060102150405")
	}
	if p.Name == "" || p.Kind == "" {
		writeErr(w, 400, fmt.Errorf("name and kind are required"))
		return
	}
	if !p.Enabled {
		p.Enabled = true
	}
	enabledInt := 0
	if p.Enabled {
		enabledInt = 1
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO providers (id, name, kind, base_url, api_key_ref,
			default_model, enabled, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Name, p.Kind, p.BaseURL, p.APIKeyRef, p.DefaultModel, enabledInt, p.Metadata)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("provider.create", p.ID, p.Kind)
	writeOK(w, p)
}

func getProviderHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p providerDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, name, kind, base_url, api_key_ref, default_model,
			enabled, metadata, created_at, updated_at FROM providers WHERE id = ?`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		list, err := scanProviders(rows)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return fmt.Errorf("not found")
		}
		p = list[0]
		return nil
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeOK(w, p)
}

func updateProviderHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p providerDTO
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, err)
		return
	}
	enabledInt := 0
	if p.Enabled {
		enabledInt = 1
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE providers SET name=?, kind=?, base_url=?, api_key_ref=?,
			default_model=?, enabled=?, metadata=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			p.Name, p.Kind, p.BaseURL, p.APIKeyRef, p.DefaultModel, enabledInt, p.Metadata, id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("provider.update", id, "")
	writeOK(w, p)
}

func deleteProviderHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM providers WHERE id = ?", id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("provider.delete", id, "")
	writeOK(w, map[string]string{"id": id})
}

// listProviderModelsHandler fetches the live model list from a provider's
// OpenAI-compatible /v1/models endpoint using that provider's own key.
func listProviderModelsHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	base, key := providerCreds(id)
	if base == "" {
		writeErr(w, 404, fmt.Errorf("provider %q has no base URL", id))
		return
	}
	ids, err := fetchModelIDs(base, key)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeOK(w, map[string]interface{}{
		"provider_id": id,
		"base_url":    base,
		"models":      ids,
	})
}

// fetchModelIDs calls {base}/models and pulls the model ids out of either the
// OpenAI {"data":[{"id":...}]} shape or a bare array.
func fetchModelIDs(base, key string) ([]string, error) {
	url := strings.TrimRight(base, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Most of these gateways accept either header; send both so we work
	// regardless of which style the endpoint implements.
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("x-api-key", key)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models request to %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models endpoint returned %d", resp.StatusCode)
	}
	return parseModelIDs(body)
}

// parseModelIDs accepts the OpenAI {"data":[{"id"}]} envelope, a bare array
// of objects, or a bare array of id strings. Some local servers use the
// latter two, and ignoring them left the model dropdown empty.
func parseModelIDs(body []byte) ([]string, error) {
	collect := func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			if id != "" {
				out = append(out, id)
			}
		}
		sort.Strings(out)
		return out
	}
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 {
		ids := make([]string, 0, len(envelope.Data))
		for _, m := range envelope.Data {
			ids = append(ids, m.ID)
		}
		return collect(ids), nil
	}
	var objects []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &objects); err == nil && len(objects) > 0 {
		ids := make([]string, 0, len(objects))
		for _, m := range objects {
			ids = append(ids, m.ID)
		}
		return collect(ids), nil
	}
	var bare []string
	if err := json.Unmarshal(body, &bare); err == nil && len(bare) > 0 {
		return collect(bare), nil
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("could not parse models response: %v", err)
	}
	return []string{}, nil
}

// chatProbeModels lists models to try when checking that an endpoint can
// answer. Live catalogs sort retired ids first, so the last ids are the
// ones the endpoint still serves.
func chatProbeModels(id, base, key string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		out = append(out, model)
	}
	if s := getAgentSettings(); s != nil && s.DefaultProvider == id {
		add(resolveModelID(s.DefaultModel))
	}
	if ids, err := fetchModelIDs(base, key); err == nil {
		added := 0
		for i := len(ids) - 1; i >= 0 && added < 3; i-- {
			before := len(out)
			add(ids[i])
			if len(out) > before {
				added++
			}
		}
	}
	if len(out) == 0 {
		for _, model := range modelsForProvider(id) {
			add(model)
			if len(out) >= 3 {
				break
			}
		}
	}
	if len(out) == 0 {
		out = append(out, "probe")
	}
	return out
}

func testProviderHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Prefer agent/settings.json, which is the live source of truth.
	if key := agentProviderKey(id); key != "" {
		base, _ := providerCreds(id)
		modelsOK := probeProvider(base, key)

		// Probe with a model THIS endpoint still serves. Catalogs list
		// retired ids first, and a 404 for one of those used to be read
		// as "this endpoint has no chat route".
		protocol := providerProtocol(providerKind(id), base)
		probeModel := ""
		chatOK := false
		chatWhy := ""
		for _, candidate := range chatProbeModels(id, base, key) {
			probeModel = candidate
			chatOK, chatWhy = probeChatReady(protocol, base, key, candidate)
			if chatOK || !modelGone(chatWhy) {
				break
			}
		}

		reason := ""
		if !chatOK {
			reason = chatWhy
			if !modelsOK {
				reason = "endpoint did not answer /models and " + chatWhy
			}
		}
		writeOK(w, map[string]interface{}{
			// "ok" means the endpoint can answer a chat prompt.
			"ok":          chatOK,
			"reachable":   modelsOK,
			"chat_ready":  chatOK,
			"protocol":    protocol,
			"source":      "agent/settings.json",
			"base_url":    base,
			"reason":      reason,
			"chat_detail": chatWhy,
			"probe_model": probeModel,
		})
		return
	}

	// Fall back to a row in the providers table.
	var kind, baseURL, apiKeyRef string
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow("SELECT kind, base_url, api_key_ref FROM providers WHERE id = ?", id).
			Scan(&kind, &baseURL, &apiKeyRef)
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if apiKeyRef == "" {
		writeOK(w, map[string]interface{}{"ok": false, "reason": "no api_key_ref set"})
		return
	}
	if _, err := getSecret(apiKeyRef); err != nil {
		writeOK(w, map[string]interface{}{"ok": false, "reason": "api key missing in secret db"})
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "kind": kind, "base_url": baseURL})
}

// ============================================================
// Projects
// ============================================================

type projectDTO struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Path              string `json:"path"`
	Description       string `json:"description"`
	DefaultProviderID string `json:"default_provider_id"`
	DefaultModel      string `json:"default_model"`
	PermissionMode    string `json:"permission_mode"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func listProjectsHandler(w http.ResponseWriter, r *http.Request) {
	var out []projectDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, name, path, description, default_provider_id,
			default_model, permission_mode, created_at, updated_at FROM projects ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p projectDTO
			if err := rows.Scan(&p.ID, &p.Name, &p.Path, &p.Description, &p.DefaultProviderID,
				&p.DefaultModel, &p.PermissionMode, &p.CreatedAt, &p.UpdatedAt); err != nil {
				return err
			}
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if out == nil {
		out = []projectDTO{}
	}
	writeOK(w, out)
}

func createProjectHandler(w http.ResponseWriter, r *http.Request) {
	var p projectDTO
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, err)
		return
	}
	if p.ID == "" {
		p.ID = "proj_" + time.Now().Format("20060102150405")
	}
	if p.Name == "" || p.Path == "" {
		writeErr(w, 400, fmt.Errorf("name and path are required"))
		return
	}
	if p.PermissionMode == "" {
		p.PermissionMode = "workspace-write"
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO projects (id, name, path, description, default_provider_id,
			default_model, permission_mode) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Name, p.Path, p.Description, p.DefaultProviderID, p.DefaultModel, p.PermissionMode)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("project.create", p.ID, p.Path)
	writeOK(w, p)
}

func getProjectHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p projectDTO
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT id, name, path, description, default_provider_id,
			default_model, permission_mode, created_at, updated_at FROM projects WHERE id = ?`, id).
			Scan(&p.ID, &p.Name, &p.Path, &p.Description, &p.DefaultProviderID,
				&p.DefaultModel, &p.PermissionMode, &p.CreatedAt, &p.UpdatedAt)
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeOK(w, p)
}

func updateProjectHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p projectDTO
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, err)
		return
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE projects SET name=?, path=?, description=?, default_provider_id=?,
			default_model=?, permission_mode=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			p.Name, p.Path, p.Description, p.DefaultProviderID, p.DefaultModel, p.PermissionMode, id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("project.update", id, "")
	writeOK(w, p)
}

func deleteProjectHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM projects WHERE id = ?", id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("project.delete", id, "")
	writeOK(w, map[string]string{"id": id})
}

func scanProjectHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p projectDTO
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT id, path FROM projects WHERE id = ?`, id).
			Scan(&p.ID, &p.Path)
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	deps := scanDependencies(p.Path)
	audit("project.scan", id, "")
	writeOK(w, deps)
}

// ============================================================
// MCP servers
// ============================================================

type mcpServerDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Command   string `json:"command"`
	Args      string `json:"args"` // JSON array as string
	URL       string `json:"url"`
	Enabled   bool   `json:"enabled"`
	Config    string `json:"config"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func listMCPServersHandler(w http.ResponseWriter, r *http.Request) {
	var out []mcpServerDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, name, transport, command, args, url, enabled, config,
			created_at, updated_at FROM mcp_servers ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m mcpServerDTO
			var enabled int
			if err := rows.Scan(&m.ID, &m.Name, &m.Transport, &m.Command, &m.Args, &m.URL,
				&enabled, &m.Config, &m.CreatedAt, &m.UpdatedAt); err != nil {
				return err
			}
			m.Enabled = enabled != 0
			out = append(out, m)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if out == nil {
		out = []mcpServerDTO{}
	}
	writeOK(w, out)
}

func createMCPServerHandler(w http.ResponseWriter, r *http.Request) {
	var m mcpServerDTO
	if err := readJSON(r, &m); err != nil {
		writeErr(w, 400, err)
		return
	}
	if m.ID == "" {
		m.ID = "mcp_" + time.Now().Format("20060102150405")
	}
	enabledInt := 0
	if m.Enabled {
		enabledInt = 1
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO mcp_servers (id, name, transport, command, args, url,
			enabled, config) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.Name, m.Transport, m.Command, m.Args, m.URL, enabledInt, m.Config)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("mcp.create", m.ID, m.Transport)
	writeOK(w, m)
}

func getMCPServerHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var m mcpServerDTO
	var enabled int
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT id, name, transport, command, args, url, enabled, config,
			created_at, updated_at FROM mcp_servers WHERE id = ?`, id).
			Scan(&m.ID, &m.Name, &m.Transport, &m.Command, &m.Args, &m.URL,
				&enabled, &m.Config, &m.CreatedAt, &m.UpdatedAt)
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	m.Enabled = enabled != 0
	writeOK(w, m)
}

func updateMCPServerHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var m mcpServerDTO
	if err := readJSON(r, &m); err != nil {
		writeErr(w, 400, err)
		return
	}
	enabledInt := 0
	if m.Enabled {
		enabledInt = 1
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE mcp_servers SET name=?, transport=?, command=?, args=?, url=?,
			enabled=?, config=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			m.Name, m.Transport, m.Command, m.Args, m.URL, enabledInt, m.Config, id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("mcp.update", id, "")
	writeOK(w, m)
}

func deleteMCPServerHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM mcp_servers WHERE id = ?", id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("mcp.delete", id, "")
	writeOK(w, map[string]string{"id": id})
}

// ============================================================
// Plugins
// ============================================================

type pluginDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Source      string `json:"source"`
	InstallPath string `json:"install_path"`
	Enabled     bool   `json:"enabled"`
	Metadata    string `json:"metadata"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func listPluginsHandler(w http.ResponseWriter, r *http.Request) {
	var out []pluginDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, name, version, source, install_path, enabled, metadata,
			created_at, updated_at FROM plugins ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p pluginDTO
			var enabled int
			if err := rows.Scan(&p.ID, &p.Name, &p.Version, &p.Source, &p.InstallPath,
				&enabled, &p.Metadata, &p.CreatedAt, &p.UpdatedAt); err != nil {
				return err
			}
			p.Enabled = enabled != 0
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if out == nil {
		out = []pluginDTO{}
	}
	writeOK(w, out)
}

func createPluginHandler(w http.ResponseWriter, r *http.Request) {
	var p pluginDTO
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, err)
		return
	}
	if p.ID == "" {
		p.ID = "plug_" + time.Now().Format("20060102150405")
	}
	enabledInt := 0
	if p.Enabled {
		enabledInt = 1
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO plugins (id, name, version, source, install_path, enabled,
			metadata) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Name, p.Version, p.Source, p.InstallPath, enabledInt, p.Metadata)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("plugin.create", p.ID, p.Source)
	writeOK(w, p)
}

func getPluginHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p pluginDTO
	var enabled int
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT id, name, version, source, install_path, enabled, metadata,
			created_at, updated_at FROM plugins WHERE id = ?`, id).
			Scan(&p.ID, &p.Name, &p.Version, &p.Source, &p.InstallPath,
				&enabled, &p.Metadata, &p.CreatedAt, &p.UpdatedAt)
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	p.Enabled = enabled != 0
	writeOK(w, p)
}

func updatePluginHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var p pluginDTO
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, err)
		return
	}
	enabledInt := 0
	if p.Enabled {
		enabledInt = 1
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE plugins SET name=?, version=?, source=?, install_path=?,
			enabled=?, metadata=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			p.Name, p.Version, p.Source, p.InstallPath, enabledInt, p.Metadata, id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("plugin.update", id, "")
	writeOK(w, p)
}

func deletePluginHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM plugins WHERE id = ?", id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("plugin.delete", id, "")
	writeOK(w, map[string]string{"id": id})
}

// ============================================================
// Skills
// ============================================================

type skillDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func listSkillsHandler(w http.ResponseWriter, r *http.Request) {
	includeLearned := r.URL.Query().Get("include_learned") == "1"
	var out []map[string]any

	if includeLearned {
		// Merge user/bundled skills with self-learned ones for the Skill
		// Library modal. The two tables share the same outward shape so the
		// UI treats them uniformly.
		err := readDB(func(db *sql.DB) error {
			rows, err := db.Query(`SELECT id, name, description, source, path, enabled,
				created_at, updated_at FROM skills ORDER BY name`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var s skillDTO
				var enabled int
				if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Source, &s.Path,
					&enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
					return err
				}
				s.Enabled = enabled != 0
				body := ""
				if s.Path != "" {
					if data, err := os.ReadFile(s.Path); err == nil {
						body = string(data)
					}
				}
				out = append(out, map[string]any{
					"id": s.ID, "name": s.Name, "description": s.Description,
					"source": s.Source, "path": s.Path, "enabled": s.Enabled,
					"created": s.CreatedAt, "body": body,
				})
			}
			return nil
		})
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		rows, err := func() ([]map[string]any, error) {
			var items []map[string]any
			err := readDB(func(db *sql.DB) error {
				rs, err := db.Query(`SELECT id, name, query, body, source, url, created_at
					FROM learned_skills ORDER BY created_at DESC LIMIT 200`)
				if err != nil {
					return err
				}
				defer rs.Close()
				for rs.Next() {
					var id int64
					var name, query, body, source, url, created string
					if err := rs.Scan(&id, &name, &query, &body, &source, &url, &created); err != nil {
						return err
					}
					items = append(items, map[string]any{
						"id": id, "name": name, "description": "learned from " + source + ": " + query,
						"source": "learned", "path": "", "enabled": true,
						"created": created, "body": body, "url": url,
					})
				}
				return nil
			})
			return items, err
		}()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		out = append(out, rows...)
		if out == nil {
			out = []map[string]any{}
		}
		writeOK(w, out)
		return
	}

	var dtoOut []skillDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, name, description, source, path, enabled,
			created_at, updated_at FROM skills ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s skillDTO
			var enabled int
			if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Source, &s.Path,
				&enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
				return err
			}
			s.Enabled = enabled != 0
			dtoOut = append(dtoOut, s)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if dtoOut == nil {
		dtoOut = []skillDTO{}
	}
	writeOK(w, dtoOut)
}

func createSkillHandler(w http.ResponseWriter, r *http.Request) {
	var s skillDTO
	if err := readJSON(r, &s); err != nil {
		writeErr(w, 400, err)
		return
	}
	if s.ID == "" {
		s.ID = "skill_" + time.Now().Format("20060102150405")
	}
	enabledInt := 0
	if s.Enabled {
		enabledInt = 1
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO skills (id, name, description, source, path, enabled)
			VALUES (?, ?, ?, ?, ?, ?)`,
			s.ID, s.Name, s.Description, s.Source, s.Path, enabledInt)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("skill.create", s.ID, s.Source)
	writeOK(w, s)
}

func getSkillHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var s skillDTO
	var enabled int
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT id, name, description, source, path, enabled,
			created_at, updated_at FROM skills WHERE id = ?`, id).
			Scan(&s.ID, &s.Name, &s.Description, &s.Source, &s.Path,
				&enabled, &s.CreatedAt, &s.UpdatedAt)
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	s.Enabled = enabled != 0
	writeOK(w, s)
}

func updateSkillHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var s skillDTO
	if err := readJSON(r, &s); err != nil {
		writeErr(w, 400, err)
		return
	}
	enabledInt := 0
	if s.Enabled {
		enabledInt = 1
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE skills SET name=?, description=?, source=?, path=?, enabled=?,
			updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			s.Name, s.Description, s.Source, s.Path, enabledInt, id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("skill.update", id, "")
	writeOK(w, s)
}

func deleteSkillHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM skills WHERE id = ?", id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("skill.delete", id, "")
	writeOK(w, map[string]string{"id": id})
}

// ============================================================
// Agents
// ============================================================

type agentDTO struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	Source          string `json:"source"`
	SystemPrompt    string `json:"system_prompt"`
	Metadata        string `json:"metadata"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func listAgentsHandler(w http.ResponseWriter, r *http.Request) {
	var out []agentDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, name, description, model, reasoning_effort, source,
			system_prompt, metadata, created_at, updated_at FROM agents ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a agentDTO
			if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Model, &a.ReasoningEffort,
				&a.Source, &a.SystemPrompt, &a.Metadata, &a.CreatedAt, &a.UpdatedAt); err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if out == nil {
		out = []agentDTO{}
	}
	writeOK(w, out)
}

func createAgentHandler(w http.ResponseWriter, r *http.Request) {
	var a agentDTO
	if err := readJSON(r, &a); err != nil {
		writeErr(w, 400, err)
		return
	}
	if a.ID == "" {
		a.ID = "agent_" + time.Now().Format("20060102150405")
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO agents (id, name, description, model, reasoning_effort,
			source, system_prompt, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.Name, a.Description, a.Model, a.ReasoningEffort, a.Source, a.SystemPrompt, a.Metadata)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("agent.create", a.ID, a.Source)
	writeOK(w, a)
}

func getAgentHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a agentDTO
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT id, name, description, model, reasoning_effort, source,
			system_prompt, metadata, created_at, updated_at FROM agents WHERE id = ?`, id).
			Scan(&a.ID, &a.Name, &a.Description, &a.Model, &a.ReasoningEffort,
				&a.Source, &a.SystemPrompt, &a.Metadata, &a.CreatedAt, &a.UpdatedAt)
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeOK(w, a)
}

func updateAgentHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a agentDTO
	if err := readJSON(r, &a); err != nil {
		writeErr(w, 400, err)
		return
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE agents SET name=?, description=?, model=?, reasoning_effort=?,
			source=?, system_prompt=?, metadata=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			a.Name, a.Description, a.Model, a.ReasoningEffort, a.Source, a.SystemPrompt, a.Metadata, id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("agent.update", id, "")
	writeOK(w, a)
}

func deleteAgentHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM agents WHERE id = ?", id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("agent.delete", id, "")
	writeOK(w, map[string]string{"id": id})
}

// ============================================================
// Sessions (chat history)
// ============================================================

type sessionDTO struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	Title        string `json:"title"`
	StartedAt    string `json:"started_at"`
	EndedAt      string `json:"ended_at"`
	MessageCount int    `json:"message_count"`
	TokenInput   int    `json:"token_input"`
	TokenOutput  int    `json:"token_output"`
}

type sessionMessageDTO struct {
	ID         int    `json:"id"`
	SessionID  string `json:"session_id"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Ts         string `json:"ts"`
}

func listSessionsHandler(w http.ResponseWriter, r *http.Request) {
	var out []sessionDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, project_id, title, started_at, ended_at, message_count,
			token_input, token_output FROM sessions ORDER BY started_at DESC LIMIT 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s sessionDTO
			var endedAt sql.NullString
			if err := rows.Scan(&s.ID, &s.ProjectID, &s.Title, &s.StartedAt, &endedAt,
				&s.MessageCount, &s.TokenInput, &s.TokenOutput); err != nil {
				return err
			}
			if endedAt.Valid {
				s.EndedAt = endedAt.String
			}
			out = append(out, s)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if out == nil {
		out = []sessionDTO{}
	}
	writeOK(w, out)
}

func createSessionHandler(w http.ResponseWriter, r *http.Request) {
	var s sessionDTO
	if err := readJSON(r, &s); err != nil {
		writeErr(w, 400, err)
		return
	}
	if s.ID == "" {
		s.ID = "sess_" + time.Now().Format("20060102150405")
	}
	if s.Title == "" {
		s.Title = "New session"
	}
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions (id, project_id, title) VALUES (?, ?, ?)`,
			s.ID, s.ProjectID, s.Title)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("session.create", s.ID, "")
	writeOK(w, s)
}

func getSessionHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var s sessionDTO
	var endedAt sql.NullString
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT id, project_id, title, started_at, ended_at, message_count,
			token_input, token_output FROM sessions WHERE id = ?`, id).
			Scan(&s.ID, &s.ProjectID, &s.Title, &s.StartedAt, &endedAt,
				&s.MessageCount, &s.TokenInput, &s.TokenOutput)
	})
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if endedAt.Valid {
		s.EndedAt = endedAt.String
	}
	// Also include the messages
	messages, err := getSessionMessages(id)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeOK(w, map[string]interface{}{"session": s, "messages": messages})
}

func appendMessageHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var m sessionMessageDTO
	if err := readJSON(r, &m); err != nil {
		writeErr(w, 400, err)
		return
	}
	m.SessionID = id
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO session_messages (session_id, role, content, tool_name,
			tool_call_id) VALUES (?, ?, ?, ?, ?)`,
			m.SessionID, m.Role, m.Content, m.ToolName, m.ToolCallID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE sessions SET message_count = message_count + 1 WHERE id = ?`, id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeOK(w, m)
}

func deleteSessionHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM sessions WHERE id = ?", id)
		return err
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("session.delete", id, "")
	writeOK(w, map[string]string{"id": id})
}

func getSessionMessages(sessionID string) ([]sessionMessageDTO, error) {
	var out []sessionMessageDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, session_id, role, content, tool_name, tool_call_id, ts
			FROM session_messages WHERE session_id = ? ORDER BY id ASC`, sessionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m sessionMessageDTO
			if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.ToolName,
				&m.ToolCallID, &m.Ts); err != nil {
				return err
			}
			out = append(out, m)
		}
		return nil
	})
	return out, err
}

// ============================================================
// Repair / scan
// ============================================================

func repairHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Path == "" {
		body.Path = "."
	}
	deps := scanDependencies(body.Path)
	actions := checkRepair(deps)
	audit("repair", body.Path, "")
	writeOK(w, map[string]interface{}{
		"path":    body.Path,
		"deps":    deps,
		"actions": actions,
	})
}

func scanHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Path == "" {
		body.Path = "."
	}
	deps := scanDependencies(body.Path)
	audit("scan", body.Path, "")
	writeOK(w, map[string]interface{}{"path": body.Path, "deps": deps})
}

// ============================================================
// Audit
// ============================================================

type auditEntry struct {
	ID      int    `json:"id"`
	Ts      string `json:"ts"`
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Target  string `json:"target"`
	Details string `json:"details"`
}

func auditHandler(w http.ResponseWriter, r *http.Request) {
	var out []auditEntry
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, ts, actor, action, target, details FROM audit_log
			ORDER BY id DESC LIMIT 200`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a auditEntry
			if err := rows.Scan(&a.ID, &a.Ts, &a.Actor, &a.Action, &a.Target, &a.Details); err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if out == nil {
		out = []auditEntry{}
	}
	writeOK(w, out)
}

// ============================================================
// Chat proxy: forwards prompts to claw-core
// ============================================================

type editorContext struct {
	ActiveFile  string   `json:"active_file"`
	OpenTabs    []string `json:"open_tabs"`
	ProjectPath string   `json:"project_path"`
}

type chatRequest struct {
	SessionID      string          `json:"session_id"`
	Prompt         string          `json:"prompt"`
	Model          string          `json:"model"`
	ProviderID     string          `json:"provider_id"`
	ProjectID      string          `json:"project_id"`
	PermissionMode string          `json:"permission_mode"`
	Attachments    []attachmentDTO `json:"attachments"`
	Editor         *editorContext  `json:"editor"`
}

type attachmentDTO struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// maxUploadBytes caps a single attachment at 16 MB.
const maxUploadBytes = 16 << 20

// uploadHandler accepts one or more files as multipart/form-data and stores
// them under ~/.omnicode/uploads, returning the saved path for each.
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeErr(w, 400, fmt.Errorf("parse upload: %v", err))
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	uploadDir := filepath.Join(config.InstallPath, "uploads")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		writeErr(w, 500, fmt.Errorf("create upload dir: %v", err))
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		files = r.MultipartForm.File["file"]
	}
	if len(files) == 0 {
		writeErr(w, 400, fmt.Errorf("no files provided"))
		return
	}

	out := make([]attachmentDTO, 0, len(files))
	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			writeErr(w, 400, fmt.Errorf("open %s: %v", fh.Filename, err))
			return
		}
		// Never trust the client-supplied name: keep only the base and
		// prefix with a timestamp so uploads cannot escape the dir.
		safe := filepath.Base(filepath.FromSlash(fh.Filename))
		safe = strings.ReplaceAll(safe, string(os.PathSeparator), "_")
		if safe == "" || safe == "." || safe == ".." {
			writeErr(w, 400, fmt.Errorf("invalid file name"))
			_ = src.Close()
			return
		}
		dst := filepath.Join(uploadDir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), safe))
		dstFile, err := os.Create(dst)
		if err != nil {
			writeErr(w, 500, fmt.Errorf("save %s: %v", safe, err))
			_ = src.Close()
			return
		}
		written, err := io.Copy(dstFile, io.LimitReader(src, maxUploadBytes))
		closeErr := dstFile.Close()
		_ = src.Close()
		if err != nil {
			writeErr(w, 500, fmt.Errorf("write %s: %v", safe, err))
			return
		}
		if closeErr != nil {
			writeErr(w, 500, fmt.Errorf("close %s: %v", safe, closeErr))
			return
		}
		out = append(out, attachmentDTO{Name: safe, Path: dst, Size: written})
	}
	writeOK(w, out)
}

// normalizePermission accepts the header dropdown values and the aliases
// the projects form has used. Anything else is left unset so claw-core
// keeps its own default.
func normalizePermission(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "read-only", "readonly", "read_only":
		return "read-only"
	case "workspace-write", "workspace_write", "accept-edits", "acceptedits":
		return "workspace-write"
	case "danger-full-access", "danger", "bypass":
		return "danger-full-access"
	default:
		return ""
	}
}

// workspaceDir is the project folder the coding tools should run in.
// With no project selected, that folder is the clawd-code checkout so the
// agent reads CLAW.md and can edit rust/ and src/.
func workspaceDir(projectID, sessionID string) string {
	if path := projectPath(projectID, sessionID); path != "" {
		return path
	}
	return clawCodeRoot()
}

func projectPath(projectID, sessionID string) string {
	if strings.TrimSpace(projectID) == "" && sessionID != "" {
		var pid sql.NullString
		_ = readDB(func(db *sql.DB) error {
			return db.QueryRow(`SELECT project_id FROM sessions WHERE id = ?`, sessionID).Scan(&pid)
		})
		if pid.Valid {
			projectID = pid.String
		}
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ""
	}
	var path string
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT path FROM projects WHERE id = ?`, projectID).Scan(&path)
	})
	if err != nil {
		return ""
	}
	return usableDir(path)
}

func clawCodeRoot() string {
	if wd, err := os.Getwd(); err == nil {
		if root := findClawRoot(wd); root != "" {
			return root
		}
	}
	if exe, err := os.Executable(); err == nil {
		if root := findClawRoot(filepath.Dir(exe)); root != "" {
			return root
		}
	}
	return ""
}

// findClawRoot walks up from start until it finds the clawd-code checkout:
// CLAW.md next to rust/Cargo.toml.
func findClawRoot(start string) string {
	dir := filepath.Clean(strings.TrimSpace(start))
	if dir == "" || dir == "." {
		return ""
	}
	for i := 0; i < 8; i++ {
		claw := filepath.Join(dir, "CLAW.md")
		cargo := filepath.Join(dir, "rust", "Cargo.toml")
		if isFile(claw) && isFile(cargo) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func usableDir(path string) string {
	path = strings.TrimSpace(path)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	return path
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// coreMessage reads claw-core's JSON prompt result. A plain-text reply,
// from an older binary, is returned unchanged.
func coreMessage(raw string) string {
	msg, _, _ := coreResult(raw)
	return msg
}

// coreResult splits a claw-core JSON prompt result into the visible reply,
// a short line per tool call, and any reasoning text the core included.
func coreResult(raw string) (message string, activity []string, reasoning string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, ""
	}
	var parsed struct {
		Message   string `json:"message"`
		Reasoning string `json:"reasoning"`
		ToolUses  []struct {
			Name  string `json:"name"`
			Input string `json:"input"`
		} `json:"tool_uses"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw, nil, ""
	}
	seen := map[string]bool{}
	nameSeen := map[string]bool{}
	var names []string
	for _, tool := range parsed.ToolUses {
		if tool.Name != "" && !nameSeen[tool.Name] {
			nameSeen[tool.Name] = true
			names = append(names, tool.Name)
		}
		line := describeToolUse(tool.Name, tool.Input)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		activity = append(activity, line)
	}
	msg := strings.TrimSpace(parsed.Message)
	if msg == "" && len(names) > 0 {
		msg = "Used tools: " + strings.Join(names, ", ")
	}
	if msg == "" && len(names) == 0 {
		return raw, activity, strings.TrimSpace(parsed.Reasoning)
	}
	return msg, activity, strings.TrimSpace(parsed.Reasoning)
}

// describeToolUse renders one tool call as a single think-box line.
func describeToolUse(name, input string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	detail := toolDetail(input)
	if detail == "" {
		return name
	}
	return name + " · " + detail
}

func toolDetail(input string) string {
	input = strings.TrimSpace(input)
	if input == "" || input == "{}" || input == "null" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		return trimActivity(input)
	}
	for _, key := range []string{"command", "cmd", "path", "file_path", "filePath", "pattern", "query", "url", "prompt"} {
		value, _ := payload[key].(string)
		value = strings.TrimSpace(value)
		if value != "" {
			return trimActivity(value)
		}
	}
	return ""
}

func trimActivity(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 160 {
		return text[:160] + "…"
	}
	return text
}

// parseOmniLine reads one live progress line from claw-core stderr.
// The line is `OMNI` plus a JSON object, which keeps reasoning off stdout
// so the JSON reply stays a single document.
func parseOmniLine(line string) (kind, text string, ok bool) {
	line = strings.TrimSpace(line)
	const prefix = "OMNI "
	if !strings.HasPrefix(line, prefix) {
		return "", "", false
	}
	var ev struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(line[len(prefix):]), &ev); err != nil || ev.Type == "" {
		return "", "", false
	}
	return ev.Type, ev.Text, true
}

func chatHandler(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	payload, code, err := serveChat(r.Context(), req, nil)
	if err != nil {
		writeErr(w, code, err)
		return
	}
	writeOK(w, payload)
}

// serveChat runs one prompt. live, when set, receives reasoning chunks and
// activity lines while the model is still working.
func serveChat(ctx context.Context, req chatRequest, live func(kind, text string)) (map[string]any, int, error) {
	if req.Prompt == "" && len(req.Attachments) == 0 {
		return nil, 400, fmt.Errorf("prompt is required")
	}
	// Build the text handed to claw-core: a short system framing so the agent
	// knows the OmniCode workspace is open in front of the user, then the
	// prompt, then any attached file paths so the agent can read them
	// directly. Steering the model towards write_file matters: a generic
	// "here is the code" markdown reply leaves the IDE with nothing to open,
	// which is exactly what created the "stupid answer" reports.
	ws := workspaceDir(req.ProjectID, req.SessionID)
	sys := omnicodeSystemPrompt(ws, req.SessionID, req.Attachments, req.Editor)
	prompt := sys + req.Prompt
	if len(req.Attachments) > 0 {
		prompt += "\n\n"
		prompt += "Attached files:\n"
		for _, a := range req.Attachments {
			prompt += fmt.Sprintf("- %s (%s)\n", a.Path, a.Name)
		}
	}
	// Persist the user message
	if req.SessionID != "" {
		_ = withDB(func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO session_messages (session_id, role, content)
				VALUES (?, 'user', ?)`, req.SessionID, req.Prompt)
			return err
		})
	}
	// Resolve the sticky provider: an explicit client choice wins, otherwise
	// reuse whatever the user last selected, otherwise the configured default.
	providerID := lastProvider(req.ProviderID)
	// A placeholder from the model dropdown ("loading models…") is not an id.
	model := usableModel(resolveModelID(req.Model))
	if model == "" {
		// A model remembered from a different provider 404s on this endpoint.
		model = usableModel(healModelForProvider(providerID, usableModel(lastModel(""))))
	}
	if base, key := providerCreds(providerID); model == "" && base != "" {
		if ids, ferr := fetchModelIDs(base, key); ferr == nil && len(ids) > 0 {
			model = ids[0]
		}
	}

	// note records reasoning and activity for the final payload and, when
	// the caller asked for it, forwards the same line to the think box.
	var (
		traceMu      sync.Mutex
		reasoning    strings.Builder
		activity     []string
		activitySeen = map[string]bool{}
		toolEvents   int
		toolUses     []map[string]any
	)
	note := func(kind, text string) {
		if kind != "thinking" {
			text = strings.TrimSpace(text)
		}
		if text == "" {
			return
		}
		traceMu.Lock()
		switch kind {
		case "thinking":
			reasoning.WriteString(text)
		default:
			if !activitySeen[text] {
				activitySeen[text] = true
				activity = append(activity, text)
			}
			if kind == "tool" {
				toolEvents++
			}
		}
		traceMu.Unlock()
		if live != nil {
			live(kind, text)
		}
	}

	// runCore launches claw-core against one wire protocol. Anthropic
	// endpoints need it (tools and /v1/messages). OpenAI-compatible
	// endpoints are answered by completeChat; claw-core is only the
	// fallback when that route 404s.
	runCore := func(id, modelID, protocol string) (string, error) {
		if _, err := os.Stat(config.CorePath); err != nil {
			return "", fmt.Errorf("claw-core not found at %s", config.CorePath)
		}
		baseURL, key := providerCreds(id)
		if protocol == "" {
			protocol = providerProtocol(providerKind(id), baseURL)
		}
		// JSON keeps the chat bubble to the assistant's text. --print would
		// force the text format, which mixes spinner frames into the reply.
		args := []string{config.CorePath, "--output-format", "json"}
		if modelID != "" {
			args = append(args, "--model", modelID)
		}
		if mode := normalizePermission(req.PermissionMode); mode != "" {
			args = append(args, "--permission-mode", mode)
		}
		args = append(args, prompt)

		env := injectSecretsIntoEnv(os.Environ(), []string{
			"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
			"OPENAI_API_KEY", "OPENAI_BASE_URL",
			"XAI_API_KEY", "OMNICODE_API_KEY",
		})
		env = applyProviderEnv(env, protocol, id, baseURL, key)
		if key != "" {
			env = setEnv(env, "OMNICODE_API_KEY", key)
		}
		// Ask the core to print live reasoning and tool lines on stderr.
		env = setEnv(env, "OMNICODE_PROGRESS", "1")
		env = applyClawEnv(env)
		note("status", "Asking the model")
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = env
		if dir := workspaceDir(req.ProjectID, req.SessionID); dir != "" {
			cmd.Dir = dir
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return "", err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return "", err
		}
		if err := cmd.Start(); err != nil {
			return "", err
		}
		var outBuf, errBuf bytes.Buffer
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(&outBuf, stdout)
		}()
		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stderr)
			scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for scanner.Scan() {
				line := scanner.Text()
				if kind, text, ok := parseOmniLine(line); ok {
					note(kind, text)
					continue
				}
				errBuf.WriteString(line)
				errBuf.WriteByte('\n')
			}
		}()
		waitErr := cmd.Wait()
		wg.Wait()
		if waitErr != nil {
			detail := strings.TrimSpace(outBuf.String() + "\n" + errBuf.String())
			return "", fmt.Errorf("claw-core: %v\n%s", waitErr, detail)
		}
		msg, steps, reason := coreResult(outBuf.String())
		traceMu.Lock()
		haveReason := reasoning.Len() > 0
		seenTools := toolEvents > 0
		traceMu.Unlock()
		if !haveReason && reason != "" {
			note("thinking", reason)
		}
		if !seenTools {
			for _, step := range steps {
				note("tool", step)
			}
		}
		if uses := coreToolUses(outBuf.String()); len(uses) > 0 {
			traceMu.Lock()
			toolUses = uses
			traceMu.Unlock()
		}
		return msg, nil
	}

	// answer returns the text and the model that actually produced it.
	// A paid-model rejection retries a free model on the same endpoint
	// instead of pretending a different provider answered.
	//
	// OpenAI-compatible endpoints go through claw-core first so the model
	// receives the coding tools. completeChat is the tool-less fallback
	// when that agent cannot run the turn.
	answer := func(id, modelID string) (string, string, error) {
		baseURL, key := providerCreds(id)
		protocol := providerProtocol(providerKind(id), baseURL)
		if protocol == "openai" {
			finish := func(viaCore bool) (string, string, error) {
				once := func(m string) (string, error) {
					if viaCore {
						return runCore(id, m, "openai")
					}
					note("status", "Asking the endpoint")
					text, reason, err := completeChatParts(baseURL, key, m, prompt)
					if reason != "" {
						note("thinking", reason)
					}
					return text, err
				}
				text, err := once(modelID)
				used := modelID
				if err == nil {
					return text, used, nil
				}
				if alt := suggestedModel(err.Error()); alt != "" && alt != used {
					text2, err2 := once(alt)
					if err2 == nil {
						return text2, alt, nil
					}
					err = err2
					used = alt
				}
				if modelNotAllowed(err.Error()) || modelGone(err.Error()) {
					if alt := freeModelFor(id, baseURL, key, used); alt != "" && alt != used {
						if text2, err2 := once(alt); err2 == nil {
							return text2, alt, nil
						}
					}
				}
				// Catalogs keep retired ids at the front. Try a few from the
				// end, where the models the endpoint still serves tend to sit.
				if modelGone(err.Error()) {
					if ids, ferr := fetchModelIDs(baseURL, key); ferr == nil {
						tried := 0
						for i := len(ids) - 1; i >= 0 && tried < 3; i-- {
							if ids[i] == "" || ids[i] == used {
								continue
							}
							tried++
							if text2, err2 := once(ids[i]); err2 == nil {
								return text2, ids[i], nil
							}
						}
					}
				}
				return "", used, err
			}
			text, used, err := finish(true)
			if err == nil {
				return text, used, nil
			}
			// Anthropic-only proxies are stored with kind "custom". When
			// /chat/completions is missing, ask claw-core over /v1/messages.
			if wrongProtocol(err.Error()) {
				if coreText, coreErr := runCore(id, used, "anthropic"); coreErr == nil {
					return coreText, used, nil
				}
			}
			if plain, plainUsed, plainErr := finish(false); plainErr == nil {
				return plain, plainUsed, nil
			}
			return "", used, err
		}
		text, err := runCore(id, modelID, "anthropic")
		if err == nil {
			return text, modelID, nil
		}
		if wrongProtocol(err.Error()) {
			note("status", "Asking the endpoint")
			if chatText, reason, chatErr := completeChatParts(baseURL, key, modelID, prompt); chatErr == nil {
				if reason != "" {
					note("thinking", reason)
				}
				return chatText, modelID, nil
			}
		}
		return "", modelID, err
	}

	out, usedModel, err := answer(providerID, model)
	if err != nil && isProviderDown(err.Error()) && !modelNotAllowed(err.Error()) {
		// The endpoint itself failed. Move to a working provider, remember it,
		// and retry once so the user still gets an answer. Heal the model:
		// the failing endpoint's model id 404s on the next one.
		if next := failoverProvider(providerID); next != "" && next != providerID {
			nextModel := usableModel(healModelForProvider(next, model))
			if nextModel == "" {
				nextModel = model
			}
			audit("provider-failover", providerID, next)
			if retryOut, retryUsed, retryErr := answer(next, nextModel); retryErr == nil {
				rememberProvider(next)
				out, err = retryOut, nil
				usedModel = retryUsed
				providerID = next
				model = retryUsed
			}
		}
	}
	if usedModel != "" {
		model = usedModel
	}
	if err != nil {
		return nil, 500, err
	}
	// Pin the provider actually used so the next request sticks to it.
	if providerID != "" && providerID == lastProvider(req.ProviderID) {
		rememberProvider(providerID)
	}
	rememberModel(model)
	// Persist the assistant message
	if req.SessionID != "" {
		_ = withDB(func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO session_messages (session_id, role, content)
				VALUES (?, 'assistant', ?)`, req.SessionID, string(out))
			return err
		})
	}
	audit("chat", req.SessionID, "")
	// Self-learning distiller: any WebSearch / WebFetch tool the agent used
	// generated fresh knowledge; persist it as a learned skill so the next
	// session can reuse the answer instead of repeating the search.
	if len(toolUses) > 0 {
		go distillLearnedSkills(toolUses, req.SessionID, req.Prompt)
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	if activity == nil {
		activity = []string{}
	}
	if toolUses == nil {
		toolUses = []map[string]any{}
	}
	return map[string]any{
		"response":    out,
		"model":       model,
		"provider_id": providerID,
		"reasoning":   reasoning.String(),
		"activity":    activity,
		"tool_uses":   toolUses,
	}, 200, nil
}

// coreToolUses returns the tool calls from a claw-core JSON result in the
// shape the chat think box and tool cards render.
func coreToolUses(raw string) []map[string]any {
	raw = strings.TrimSpace(raw)
	var parsed struct {
		ToolUses []struct {
			Name  string `json:"name"`
			Input string `json:"input"`
		} `json:"tool_uses"`
	}
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(parsed.ToolUses))
	for _, tool := range parsed.ToolUses {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		var input any
		if json.Unmarshal([]byte(tool.Input), &input) != nil || input == nil {
			input = map[string]any{}
			if strings.TrimSpace(tool.Input) != "" {
				input = map[string]any{"raw": tool.Input}
			}
		}
		out = append(out, map[string]any{
			"name":   tool.Name,
			"input":  input,
			"status": "ok",
		})
	}
	return out
}

// chatStreamHandler pushes reasoning and tool lines as they happen, then
// the finished reply. The web think box reads these events.
func chatStreamHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	var req chatRequest
	if err := readJSON(r, &req); err != nil {
		writeSSE(w, flusher, "error", map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := make(chan streamEvent, 512)
	go func() {
		defer close(events)
		send := func(kind, text string) {
			select {
			case events <- streamEvent{kind: kind, text: text}:
			case <-ctx.Done():
			}
		}
		send("status", "Sending the prompt")
		payload, _, err := serveChat(ctx, req, func(kind, text string) {
			send(kind, text)
		})
		if err != nil {
			send("error", err.Error())
			return
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			send("error", err.Error())
			return
		}
		select {
		case events <- streamEvent{kind: "done", raw: raw}:
		case <-ctx.Done():
		}
	}()
	writeSSE(w, flusher, "start", map[string]string{"status": "thinking"})
	for ev := range events {
		if ev.kind == "done" {
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", ev.raw)
			flusher.Flush()
			return
		}
		writeSSE(w, flusher, ev.kind, map[string]string{"text": ev.text})
		if ev.kind == "error" {
			return
		}
	}
}

type streamEvent struct {
	kind string
	text string
	raw  []byte
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	flusher.Flush()
}

// ============================================================
// Editor: system prompt composer (gauntlet-loop + superpowers mix)
// ============================================================
//
// The system prompt is the single most important lever here. It tells the
// agent:
//   * the OmniCode workspace is live, plan.md is open in the IDE, files
//     opened as tabs are the user's intent
//   * the agentic loop pattern to follow: tiny plan → write_file → verify
//     → reflect (gauntlet-loop + superpowers mix)
//   * that the IDE has a self-learning loop, so every WebSearch result is
//     durable and reusable, not a one-shot
//   * the user sees every tool call as a card, so file creation reports
//     matter

func applyClawEnv(env []string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return env
	}
	// claw-core finds skills under $HOME/.claw and $HOME/.codex. Windows
	// processes often have USERPROFILE and no HOME.
	if strings.TrimSpace(os.Getenv("HOME")) == "" {
		env = setEnv(env, "HOME", home)
	}
	if dir, err := writeClawBridge(home); err == nil && dir != "" {
		env = setEnv(env, "CLAW_CONFIG_HOME", dir)
	}
	return env
}

// writeClawBridge publishes OmniCode's enabled MCP servers into the settings
// file claw-core reads, merged with the user's existing ~/.claw settings.
func writeClawBridge(home string) (string, error) {
	dir := filepath.Join(config.InstallPath, "claw-bridge")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	merged := map[string]any{}
	for _, path := range []string{
		filepath.Join(home, ".claw.json"),
		filepath.Join(home, ".claw", "settings.json"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		for k, v := range doc {
			merged[k] = v
		}
	}
	servers, _ := merged["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	_ = readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT name, COALESCE(transport,''), COALESCE(command,''), COALESCE(args,''), COALESCE(url,'') FROM mcp_servers WHERE enabled != 0`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, transport, command, args, url string
			if err := rows.Scan(&name, &transport, &command, &args, &url); err != nil {
				return err
			}
			entry, ok := mcpBridgeEntry(name, transport, command, args, url)
			if ok {
				servers[name] = entry
			}
		}
		return rows.Err()
	})
	merged["mcpServers"] = servers
	raw, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0644); err != nil {
		return "", err
	}
	return dir, nil
}

func mcpBridgeEntry(name, transport, command, args, url string) (map[string]any, bool) {
	var argv []string
	if strings.TrimSpace(args) != "" {
		_ = json.Unmarshal([]byte(args), &argv)
	}
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "", "stdio":
		if strings.TrimSpace(command) == "" {
			return nil, false
		}
		return map[string]any{"type": "stdio", "command": command, "args": argv}, true
	case "sse":
		if url == "" {
			return nil, false
		}
		return map[string]any{"type": "sse", "url": url}, true
	case "http":
		if url == "" {
			return nil, false
		}
		return map[string]any{"type": "http", "url": url}, true
	case "websocket", "ws":
		if url == "" {
			return nil, false
		}
		return map[string]any{"type": "ws", "url": url}, true
	case "sdk":
		if name == "" {
			return nil, false
		}
		return map[string]any{"type": "sdk", "name": name}, true
	default:
		return nil, false
	}
}

func capabilityLines() string {
	var b strings.Builder
	_ = readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT name, COALESCE(path,'') FROM skills WHERE enabled != 0 ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		var skills []string
		for rows.Next() {
			var name, path string
			if err := rows.Scan(&name, &path); err != nil {
				return err
			}
			if path != "" {
				skills = append(skills, name+" ("+path+")")
			} else if name != "" {
				skills = append(skills, name)
			}
		}
		if len(skills) > 0 {
			fmt.Fprintf(&b, "SKILLS enabled in OmniCode: %s. Use the Skill tool when one matches the task. Skills on disk under .claw/skills and .codex/skills are available too.\n", strings.Join(skills, ", "))
		}
		mrows, err := db.Query(`SELECT name, transport FROM mcp_servers WHERE enabled != 0 ORDER BY name`)
		if err != nil {
			return err
		}
		defer mrows.Close()
		var mcps []string
		for mrows.Next() {
			var name, transport string
			if err := mrows.Scan(&name, &transport); err != nil {
				return err
			}
			if name != "" {
				mcps = append(mcps, name+" ("+transport+")")
			}
		}
		if len(mcps) > 0 {
			fmt.Fprintf(&b, "MCP SERVERS enabled in OmniCode: %s. They are loaded for this turn. Use their tools when the task needs that server.\n", strings.Join(mcps, ", "))
		}
		return nil
	})
	return b.String()
}

func omnicodeSystemPrompt(workspace, sessionID string, attachments []attachmentDTO, editor *editorContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<system>\n")
	fmt.Fprintf(&b, "You are operating inside OmniCode, a local web IDE. "+
		"The user has the editor open with plan.md as the first tab. "+
		"Every file you create or edit becomes a tab in arrival order.\n\n")
	if workspace != "" {
		fmt.Fprintf(&b, "Active workspace: %s\n", workspace)
	}
	if sessionID != "" {
		fmt.Fprintf(&b, "Session: %s\n", sessionID)
	}
	if editor != nil {
		fmt.Fprintf(&b, "\nEDITOR:\n")
		if editor.ActiveFile != "" {
			fmt.Fprintf(&b, "The user is looking at %s. Read it before editing, and use edit_file for changes to that file.\n", editor.ActiveFile)
		}
		if len(editor.OpenTabs) > 0 {
			fmt.Fprintf(&b, "Open tabs: %s\n", strings.Join(editor.OpenTabs, ", "))
		}
		fmt.Fprintf(&b, "File tools run in the active workspace. Paths are relative to that folder unless absolute.\n")
	}
	if extra := capabilityLines(); extra != "" {
		fmt.Fprintf(&b, "\n%s", extra)
	}
	fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(loadSystemInstructions()))
	if len(attachments) > 0 {
		fmt.Fprintf(&b, "\nATTACHMENTS: the user attached %d file(s) below; treat "+
			"them as the canonical input.\n", len(attachments))
	}
	fmt.Fprintf(&b, "</system>\n")
	return b.String()
}

// ============================================================
// Editor: workspace files
// ============================================================
//
// listFilesHandler walks the project directory and returns a recursive tree
// of files + dirs. Skips anything ignored by .gitignore-style patterns and
// anything larger than maxFileListBytes; the tree is for navigation, not
// content. The actual file bodies are pulled by /api/file.
//
// ?project_id=<id> selects the project. If absent, falls back to the sticky
// project from the user's last action.
type fileNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	Kind     string     `json:"kind"` // 'file' | 'dir'
	Size     int64      `json:"size,omitempty"`
	Children []fileNode `json:"children,omitempty"`
}

const maxFileListBytes = 4 << 20 // 4 MiB hard cap on files we'll include
const maxTreeDepth = 8
const maxTreeEntries = 4000

func listFilesHandler(w http.ResponseWriter, r *http.Request) {
	pid := r.URL.Query().Get("project_id")
	if pid == "" {
		pid = stickyProjectID()
	}
	if pid == "" {
		writeErr(w, 400, fmt.Errorf("project_id required"))
		return
	}
	root := projectPathForEditor(pid)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project %s not found or path missing", pid))
		return
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		writeErr(w, 404, fmt.Errorf("project path %s is not a directory", root))
		return
	}
	tree := walkDir(root, "", 0, &walkBudget{max: maxTreeEntries})
	audit("files_list", pid, root)
	writeOK(w, map[string]interface{}{
		"project_id": pid,
		"path":       root,
		"entries":    tree,
	})
}

type walkBudget struct{ used, max int }

func walkDir(absRoot, rel string, depth int, budget *walkBudget) []fileNode {
	if depth > maxTreeDepth {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(absRoot, rel))
	if err != nil {
		return nil
	}
	var out []fileNode
	for _, e := range entries {
		if budget.used >= budget.max {
			break
		}
		name := e.Name()
		if shouldSkipEntry(name) {
			continue
		}
		child := rel
		if child != "" {
			child += "/" + name
		} else {
			child = name
		}
		if e.IsDir() {
			out = append(out, fileNode{
				Name:     name,
				Path:     child,
				Kind:     "dir",
				Children: walkDir(absRoot, child, depth+1, budget),
			})
		} else {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Size() > maxFileListBytes {
				continue
			}
			out = append(out, fileNode{
				Name: name,
				Path: child,
				Kind: "file",
				Size: info.Size(),
			})
		}
		budget.used++
	}
	return out
}

// shouldSkipEntry skips hidden, build-output, and dependency dirs so the
// file tree stays focused on what the user edits.
func shouldSkipEntry(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "target", "dist", "build",
		".next", ".nuxt", ".cache", ".gradle", ".idea", ".vscode",
		"__pycache__", "venv", ".venv", "env":
		return true
	}
	if strings.HasPrefix(name, ".") && name != ".gitignore" && name != ".env.example" {
		return true
	}
	return false
}

// projectPathForEditor resolves a project_id to its filesystem path; empty
// string if the project is unknown or the path is unusable.
func projectPathForEditor(pid string) string {
	var path string
	_ = readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT path FROM projects WHERE id = ?`, pid).Scan(&path)
	})
	return usableDir(path)
}

// stickyProjectID returns the editor's last-used project, or the project on
// the most-recent session, or the most-recently-created project.
func stickyProjectID() string {
	if v := getSetting("editor.last_project"); v != "" {
		return v
	}
	var pid sql.NullString
	_ = readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT project_id FROM sessions ORDER BY started_at DESC LIMIT 1`).Scan(&pid)
	})
	if pid.Valid && pid.String != "" {
		return pid.String
	}
	// Last resort: most recent project.
	var fallback string
	_ = readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT id FROM projects ORDER BY created_at DESC LIMIT 1`).Scan(&fallback)
	})
	return fallback
}

// readFileHandler serves the body of a single file.
//
//	GET /api/file?project_id=<id>&path=<rel>
//
// project_id is normally required, but if the Editor opens a tab before the
// sticky project has been picked, the request comes through with no id and
// the file fetch fails silently. Fall back to the sticky project (same as
// /api/files) so a single missed POST /api/selection does not lock the user
// out of every file.
func readFileHandler(w http.ResponseWriter, r *http.Request) {
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	rel := r.URL.Query().Get("path")
	if pid == "" {
		pid = stickyProjectID()
	}
	if pid == "" {
		writeErr(w, 400, fmt.Errorf("project_id required (no project configured)"))
		return
	}
	if rel == "" {
		writeErr(w, 400, fmt.Errorf("path required"))
		return
	}
	root := projectPathForEditor(pid)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	abs, err := safeJoin(root, rel)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if int64(len(data)) > maxFileListBytes {
		writeErr(w, 413, fmt.Errorf("file too large for editor (%d bytes)", len(data)))
		return
	}
	writeOK(w, map[string]interface{}{
		"project_id": pid,
		"path":       rel,
		"content":    string(data),
		"size":       len(data),
	})
}

// writeFileHandler saves a file under the project root and indexes it in
// project_files so the catalog stays in sync.
//
//	PUT /api/file   { project_id, path, content }
func writeFileHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Path      string `json:"path"`
		Content   string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" {
		writeErr(w, 400, fmt.Errorf("project_id required (no project configured)"))
		return
	}
	if req.Path == "" {
		writeErr(w, 400, fmt.Errorf("path required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	abs, err := safeJoin(root, req.Path)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		writeErr(w, 500, fmt.Errorf("mkdir: %v", err))
		return
	}
	if err := os.WriteFile(abs, []byte(req.Content), 0644); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Index for project_files (best-effort).
	_ = withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_files (project_id, rel_path, abs_path, size)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(project_id, rel_path) DO UPDATE SET
				abs_path = excluded.abs_path,
				size = excluded.size,
				indexed_at = CURRENT_TIMESTAMP`,
			req.ProjectID, req.Path, abs, len(req.Content))
		return err
	})
	audit("file_write", req.ProjectID, req.Path)
	writeOK(w, map[string]interface{}{
		"project_id": req.ProjectID,
		"path":       req.Path,
		"size":       len(req.Content),
	})
}

// safeJoin makes sure a relative path does not escape the project root via
// ".." traversal or absolute prefix.
func safeJoin(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	slash := strings.ReplaceAll(rel, "\\", "/")
	// Reject drives, UNC paths, and rooted paths before OS Clean turns
	// "/v1/file" into a Windows path that Join would keep under root by luck.
	if strings.HasPrefix(slash, "/") || strings.Contains(slash, ":") {
		return "", fmt.Errorf("path escapes project root: %q", rel)
	}
	clean := path.Clean(slash)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes project root: %q", rel)
	}
	abs := filepath.Join(root, filepath.FromSlash(clean))
	if err := ensureInside(root, abs); err != nil {
		return "", fmt.Errorf("path escapes project root: %q", rel)
	}
	return abs, nil
}

// ensureInside reports whether abs stays inside root after symlinks resolve.
// A path that does not exist yet is checked through its nearest existing parent,
// which is the case for a new file written through a symlinked directory.
func ensureInside(root, abs string) error {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		rootResolved = filepath.Clean(root)
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		target, err = resolveExistingParent(abs)
		if err != nil {
			return err
		}
	}
	if pathInside(rootResolved, target) {
		return nil
	}
	return fmt.Errorf("escapes")
}

func resolveExistingParent(abs string) (string, error) {
	parent := abs
	for {
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("no existing parent")
		}
		if _, err := os.Lstat(next); err == nil {
			resolved, e := filepath.EvalSymlinks(next)
			if e != nil {
				resolved = next
			}
			rest, err := filepath.Rel(next, abs)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, rest), nil
		}
		parent = next
	}
}

func pathInside(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	sep := string(os.PathSeparator)
	return target == root || strings.HasPrefix(target, root+sep)
}

// ============================================================
// Editor: plan.md (per-session brainstorm trace)
// ============================================================

func getPlanHandler(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		writeErr(w, 400, fmt.Errorf("session_id required"))
		return
	}
	content, err := loadOrCreatePlan(sid)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeOK(w, map[string]interface{}{
		"session_id": sid,
		"content":    content,
	})
}

func putPlanHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Content   string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.SessionID == "" {
		writeErr(w, 400, fmt.Errorf("session_id required"))
		return
	}
	if err := savePlan(req.SessionID, req.Content); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("plan_write", req.SessionID, fmt.Sprintf("%d bytes", len(req.Content)))
	writeOK(w, map[string]string{"status": "saved"})
}

// appendPlanHandler stores an additional section to plan.md non-destructively.
// Used by the client to record brainstorming traces per turn.
func appendPlanHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Section   string `json:"section"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.SessionID == "" || req.SessionID == "_none_" {
		// Planless session (no chat yet). No-op: traces can be replayed
		// into a fresh session once the user starts one.
		writeOK(w, map[string]string{"status": "no session"})
		return
	}
	existing, err := loadOrCreatePlan(req.SessionID)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	next := existing
	if existing != "" && !strings.HasSuffix(existing, "\n") {
		next += "\n"
	}
	next += req.Section + "\n"
	if err := savePlan(req.SessionID, next); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeOK(w, map[string]string{"status": "appended"})
}

func loadOrCreatePlan(sid string) (string, error) {
	var content string
	err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT content FROM editor_plans WHERE session_id = ?`, sid).Scan(&content)
	})
	if err == sql.ErrNoRows {
		stub := defaultPlan(sid)
		_ = savePlan(sid, stub)
		return stub, nil
	}
	if err != nil {
		return "", err
	}
	return content, nil
}

func savePlan(sid, content string) error {
	return withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO editor_plans (session_id, content, updated_at)
			VALUES (?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(session_id) DO UPDATE SET
				content = excluded.content,
				updated_at = CURRENT_TIMESTAMP`, sid, content)
		return err
	})
}

func defaultPlan(sid string) string {
	return fmt.Sprintf(`# Plan

_Session: %s. Created %s._

## Brainstorm

_(capture the question, the constraints, and any rejected ideas here as we go)_

## Steps

_(each turn appends a "## Step N" section below — never delete these)_
`, sid, time.Now().Format(time.RFC3339))
}

// ============================================================
// Editor: tabs (per-session open-files)
// ============================================================

type tabDTO struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Position  int    `json:"position"`
}

func listTabsHandler(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		writeErr(w, 400, fmt.Errorf("session_id required"))
		return
	}
	var out []tabDTO
	err := readDB(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT id, session_id, path, kind, position
			FROM editor_tabs WHERE session_id = ?
			ORDER BY position ASC, id ASC`, sid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t tabDTO
			if err := rows.Scan(&t.ID, &t.SessionID, &t.Path, &t.Kind, &t.Position); err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if out == nil {
		out = []tabDTO{}
	}
	writeOK(w, map[string]interface{}{"tabs": out})
}

func createTabHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Path      string `json:"path"`
		Kind      string `json:"kind"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.SessionID == "" || req.Path == "" {
		writeErr(w, 400, fmt.Errorf("session_id and path required"))
		return
	}
	if req.Kind == "" {
		req.Kind = "file"
	}
	// Position = current tail + 1, so the creation pipeline's arrival order
	// becomes the visible tab order.
	var maxPos sql.NullInt64
	_ = readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT MAX(position) FROM editor_tabs WHERE session_id = ?`, req.SessionID).Scan(&maxPos)
	})
	pos := int(maxPos.Int64) + 1
	// Default: a fresh session gets plan.md at position 0 only.
	if maxPos.Int64 == 0 {
		// Look up the actual count to be safe (handles duplicate path on re-open).
		var n int
		_ = readDB(func(db *sql.DB) error {
			return db.QueryRow(`SELECT COUNT(*) FROM editor_tabs WHERE session_id = ?`, req.SessionID).Scan(&n)
		})
		if n == 0 && req.Kind != "plan" {
			// Auto-create plan.md at position 0 if creating a non-plan first.
			if err := withDB(func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO editor_tabs (session_id, path, kind, position)
					VALUES (?, 'plan.md', 'plan', 0)`, req.SessionID)
				return err
			}); err == nil {
				pos = 1
			}
		}
	}

	id, err := upsertTab(req.SessionID, req.Path, req.Kind, pos)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("tab_open", req.SessionID, fmt.Sprintf("%s#%s", req.Kind, req.Path))
	writeOK(w, map[string]interface{}{
		"id": id, "session_id": req.SessionID, "path": req.Path, "kind": req.Kind, "position": pos,
	})
}

func upsertTab(sid, path, kind string, pos int) (int64, error) {
	var id int64
	err := withDB(func(tx *sql.Tx) error {
		// If a tab with this session+path+kind already exists, keep it.
		var existingID int64
		err := tx.QueryRow(`SELECT id FROM editor_tabs WHERE session_id = ? AND path = ? AND kind = ?`,
			sid, path, kind).Scan(&existingID)
		if err == nil {
			id = existingID
			return nil
		}
		res, err := tx.Exec(`INSERT INTO editor_tabs (session_id, path, kind, position)
			VALUES (?, ?, ?, ?)`, sid, path, kind, pos)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func deleteTabHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, 400, fmt.Errorf("id required"))
		return
	}
	if err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM editor_tabs WHERE id = ?`, id)
		return err
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeOK(w, map[string]string{"status": "deleted"})
}

// ============================================================
// Editor: file ops (mkfile/mkdir/rename/move/remove/zip/saveas/
// openwith/convert/share)
// ============================================================
//
// All write paths go through safeJoin so the URL ?path= argument cannot
// escape the project root. project_id falls back to the sticky project the
// same way readFileHandler does.

func mkfileHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Path      string `json:"path"`
		Content   string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || req.Path == "" {
		writeErr(w, 400, fmt.Errorf("project_id and path required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	abs, err := safeJoin(root, req.Path)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if _, err := os.Stat(abs); err == nil {
		writeErr(w, 409, fmt.Errorf("file already exists: %s", req.Path))
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := os.WriteFile(abs, []byte(req.Content), 0644); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("file_mkfile", req.ProjectID, req.Path)
	writeOK(w, map[string]interface{}{"project_id": req.ProjectID, "path": req.Path})
}

func mkdirHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Path      string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || req.Path == "" {
		writeErr(w, 400, fmt.Errorf("project_id and path required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	abs, err := safeJoin(root, req.Path)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("file_mkdir", req.ProjectID, req.Path)
	writeOK(w, map[string]interface{}{"project_id": req.ProjectID, "path": req.Path})
}

func renameHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		OldPath   string `json:"old_path"`
		NewPath   string `json:"new_path"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || req.OldPath == "" || req.NewPath == "" {
		writeErr(w, 400, fmt.Errorf("project_id, old_path and new_path required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	src, err := safeJoin(root, req.OldPath)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	dst, err := safeJoin(root, req.NewPath)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("file_rename", req.ProjectID, req.OldPath+" -> "+req.NewPath)
	writeOK(w, map[string]interface{}{"project_id": req.ProjectID, "old_path": req.OldPath, "new_path": req.NewPath})
}

func moveHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Src       string `json:"src"`
		Dst       string `json:"dst"`
		Kind      string `json:"kind"` // 'file' | 'dir'
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || req.Src == "" || req.Dst == "" {
		writeErr(w, 400, fmt.Errorf("project_id, src and dst required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	src, err := safeJoin(root, req.Src)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	dst, err := safeJoin(root, req.Dst)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("file_move", req.ProjectID, req.Src+" -> "+req.Dst)
	writeOK(w, map[string]interface{}{"project_id": req.ProjectID, "src": req.Src, "dst": req.Dst, "kind": req.Kind})
}

func removeHandler(w http.ResponseWriter, r *http.Request) {
	pid := r.URL.Query().Get("project_id")
	if pid == "" {
		pid = stickyProjectID()
	}
	if pid == "" {
		writeErr(w, 400, fmt.Errorf("project_id required"))
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		writeErr(w, 400, fmt.Errorf("path required"))
		return
	}
	root := projectPathForEditor(pid)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	abs, err := safeJoin(root, rel)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	st, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, fmt.Errorf("not found: %v", err))
		return
	}
	if st.IsDir() {
		if err := os.RemoveAll(abs); err != nil {
			writeErr(w, 500, err)
			return
		}
	} else {
		if err := os.Remove(abs); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	audit("file_remove", pid, rel)
	writeOK(w, map[string]interface{}{"project_id": pid, "path": rel})
}

func zipHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string   `json:"project_id"`
		Paths     []string `json:"paths"`
		Out       string   `json:"out"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || len(req.Paths) == 0 || req.Out == "" {
		writeErr(w, 400, fmt.Errorf("project_id, paths and out required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	if filepath.IsAbs(req.Out) || strings.Contains(req.Out, "..") {
		writeErr(w, 400, fmt.Errorf("out must be a relative path inside the project"))
		return
	}
	outAbs, err := safeJoin(root, req.Out)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(outAbs), 0755); err != nil {
		writeErr(w, 500, err)
		return
	}
	outFile, err := os.Create(outAbs)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	defer outFile.Close()
	zw := zip.NewWriter(outFile)
	defer zw.Close()
	for _, p := range req.Paths {
		src, err := safeJoin(root, p)
		if err != nil {
			writeErr(w, 400, err)
			return
		}
		if err := addToZip(zw, root, src); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	audit("file_zip", req.ProjectID, req.Out)
	writeOK(w, map[string]interface{}{"project_id": req.ProjectID, "out": req.Out})
}

func addToZip(zw *zip.Writer, root, abs string) error {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if st.IsDir() {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := addToZip(zw, root, filepath.Join(abs, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	h := &zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate, Modified: st.ModTime()}
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func shareHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Path      string `json:"path"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || req.Path == "" {
		writeErr(w, 400, fmt.Errorf("project_id and path required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	abs, err := safeJoin(root, req.Path)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	if info.IsDir() {
		// For directories, share = open the directory itself.
		writeOK(w, map[string]interface{}{
			"project_id": req.ProjectID, "path": req.Path,
			"url": "file:///" + filepath.ToSlash(abs),
		})
		return
	}
	writeOK(w, map[string]interface{}{
		"project_id": req.ProjectID, "path": req.Path,
		"url": "file:///" + filepath.ToSlash(abs),
	})
}

func openWithHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Path      string `json:"path"`
		App       string `json:"app"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || req.Path == "" {
		writeErr(w, 400, fmt.Errorf("project_id and path required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	abs, err := safeJoin(root, req.Path)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		if strings.TrimSpace(req.App) == "" || strings.EqualFold(req.App, "default") {
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", "file:///"+filepath.ToSlash(abs))
		} else {
			cmd = exec.Command(req.App, abs)
		}
	case "darwin":
		cmd = exec.Command("open", "-a", "Finder", abs)
	default:
		cmd = exec.Command("xdg-open", abs)
	}
	if err := cmd.Start(); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("file_openwith", req.ProjectID, req.Path)
	writeOK(w, map[string]interface{}{"project_id": req.ProjectID, "path": req.Path, "app": req.App})
}

func convertHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		Path      string `json:"path"`
		Target    string `json:"target"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || req.Path == "" || req.Target == "" {
		writeErr(w, 400, fmt.Errorf("project_id, path and target required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	src, err := safeJoin(root, req.Path)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	out := strings.TrimSuffix(req.Path, filepath.Ext(req.Path)) + "." + strings.TrimPrefix(strings.ToLower(req.Target), ".")
	outAbs, err := safeJoin(root, out)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := convertFile(src, outAbs, req.Target); err != nil {
		writeErr(w, 500, fmt.Errorf("convert: %v", err))
		return
	}
	audit("file_convert", req.ProjectID, req.Path+" -> "+out)
	writeOK(w, map[string]interface{}{"project_id": req.ProjectID, "path": req.Path, "target": req.Target, "out": out})
}

// convertFile handles the formats we can do locally without extra tools:
// md->txt (strip formatting), json->csv (flat key list), txt->md (wrap).
// Anything else returns a clear "toolchain missing" error so the UI can
// surface it.
func convertFile(src, dst, target string) error {
	switch strings.ToLower(target) {
	case "txt":
		if strings.EqualFold(filepath.Ext(src), ".md") {
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			stripped := stripMarkdownForTXT(string(data))
			return os.WriteFile(dst, []byte(stripped), 0644)
		}
	case "md":
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		wrapped := "# " + filepath.Base(src) + "\n\n```\n" + string(data) + "\n```\n"
		return os.WriteFile(dst, []byte(wrapped), 0644)
	case "csv":
		if strings.EqualFold(filepath.Ext(src), ".json") {
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			csv, err := jsonToCSV(string(data))
			if err != nil {
				return err
			}
			return os.WriteFile(dst, []byte(csv), 0644)
		}
	}
	return fmt.Errorf("no local converter for %s (install pandoc / a toolchain)", target)
}

func stripMarkdownForTXT(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "#"):
			b.WriteString(strings.TrimLeft(t, "# "))
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			b.WriteString("- " + strings.TrimPrefix(strings.TrimPrefix(t, "- "), "* "))
		case strings.HasPrefix(t, "```"):
			continue
		default:
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func jsonToCSV(s string) (string, error) {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", err
	}
	switch t := v.(type) {
	case []any:
		if len(t) == 0 {
			return "", nil
		}
		var header []string
		row0, ok := t[0].(map[string]any)
		if !ok {
			return "", fmt.Errorf("expected object array")
		}
		for k := range row0 {
			header = append(header, k)
		}
		var b strings.Builder
		b.WriteString(strings.Join(header, ","))
		b.WriteString("\n")
		for _, row := range t {
			obj, ok := row.(map[string]any)
			if !ok {
				continue
			}
			for i, k := range header {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, "%v", obj[k])
			}
			b.WriteString("\n")
		}
		return b.String(), nil
	case map[string]any:
		var b strings.Builder
		for k, val := range t {
			fmt.Fprintf(&b, "%s,%v\n", k, val)
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("unsupported JSON shape for csv")
}

func saveAsHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"project_id"`
		SrcPath   string `json:"src_path"`
		NewPath   string `json:"new_path"`
		Content   string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.ProjectID == "" {
		req.ProjectID = stickyProjectID()
	}
	if req.ProjectID == "" || req.NewPath == "" {
		writeErr(w, 400, fmt.Errorf("project_id and new_path required"))
		return
	}
	root := projectPathForEditor(req.ProjectID)
	if root == "" {
		writeErr(w, 404, fmt.Errorf("project not found"))
		return
	}
	dst, err := safeJoin(root, req.NewPath)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := os.WriteFile(dst, []byte(req.Content), 0644); err != nil {
		writeErr(w, 500, err)
		return
	}
	// Index
	_ = withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_files (project_id, rel_path, abs_path, size)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(project_id, rel_path) DO UPDATE SET
				abs_path = excluded.abs_path, size = excluded.size, indexed_at = CURRENT_TIMESTAMP`,
			req.ProjectID, req.NewPath, dst, len(req.Content))
		return err
	})
	audit("file_saveas", req.ProjectID, req.SrcPath+" -> "+req.NewPath)
	writeOK(w, map[string]interface{}{"project_id": req.ProjectID, "new_path": req.NewPath, "size": len(req.Content)})
}

// ============================================================
// Editor: session.md export / import (resume a project)
// ============================================================

func exportSessionHandler(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		writeErr(w, 400, fmt.Errorf("session_id required"))
		return
	}
	out, err := buildSessionMarkdown(sid)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("session_export", sid, "")
	writeOK(w, map[string]interface{}{"session_id": sid, "title": out.title, "markdown": out.markdown})
}

type sessionExport struct {
	title    string
	markdown string
}

func buildSessionMarkdown(sid string) (sessionExport, error) {
	var title string
	_ = readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT title FROM sessions WHERE id = ?`, sid).Scan(&title)
	})
	plan, _ := loadOrCreatePlan(sid)

	var rows []struct {
		Role    string
		Content string
		Tool    string
		TS      string
	}
	_ = readDB(func(db *sql.DB) error {
		rs, err := db.Query(`SELECT role, content, tool_name, ts FROM session_messages WHERE session_id = ? ORDER BY ts ASC`, sid)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var row struct {
				Role    string
				Content string
				Tool    string
				TS      string
			}
			if err := rs.Scan(&row.Role, &row.Content, &row.Tool, &row.TS); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return nil
	})

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "_Session: %s_\n\n", sid)
	if plan != "" {
		fmt.Fprintf(&b, "## Plan\n\n%s\n\n", plan)
	}
	fmt.Fprintf(&b, "## Conversation\n\n")
	for _, row := range rows {
		fmt.Fprintf(&b, "### %s (%s)\n\n", row.Role, row.TS)
		if row.Tool != "" {
			fmt.Fprintf(&b, "_tool: %s_\n\n", row.Tool)
		}
		if row.Content != "" {
			fmt.Fprintf(&b, "%s\n\n", row.Content)
		}
	}
	return sessionExport{title: title, markdown: b.String()}, nil
}

func importSessionHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Markdown string `json:"markdown"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Markdown == "" {
		writeErr(w, 400, fmt.Errorf("markdown required"))
		return
	}

	// Create a new session, reuse the markdown as the plan, and store the
	// original markdown as a single user message so the chat history keeps
	// the imported trace.
	id := fmt.Sprintf("imp_%d", time.Now().UnixNano())
	title := "Imported session " + time.Now().Format(time.RFC3339)
	if err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions (id, title, started_at, message_count) VALUES (?, ?, CURRENT_TIMESTAMP, 1)`,
			id, title)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO session_messages (session_id, role, content) VALUES (?, 'user', ?)`,
			id, req.Markdown)
		return err
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	if err := savePlan(id, req.Markdown); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("session_import", id, fmt.Sprintf("%d bytes", len(req.Markdown)))
	writeOK(w, map[string]interface{}{
		"session_id": id,
		"title":      title,
		"messages":   []map[string]any{{"role": "user", "content": req.Markdown, "ts": nowString()}},
	})
}

// ============================================================
// Editor: memory (recent / search / remember)
// ============================================================
//
// Fast memory lives in two tables:
//   * session_messages (existing) — every chat turn, indexed by
//     (session_id, ts) for sequential retrieval
//   * memory_rememberings — durable user-pinned facts (sticky across
//     sessions) the agent can recall via /api/memory/search
//
// We deliberately keep this simple (no embeddings yet). Search is a
// case-insensitive substring scan with a length score. When the user adds
// an embedding pass later, the public surface here does not change.

type memoryRow struct {
	ID       string  `json:"id"`
	TS       string  `json:"ts"`
	Role     string  `json:"role"`
	Content  string  `json:"content"`
	ToolName string  `json:"tool_name"`
	Score    float64 `json:"score,omitempty"`
}

func memoryRecentHandler(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session_id")
	if sid == "" {
		writeErr(w, 400, fmt.Errorf("session_id required"))
		return
	}
	limit := atoiOrDefault(r.URL.Query().Get("limit"), 50)
	var rows []memoryRow
	_ = readDB(func(db *sql.DB) error {
		rs, err := db.Query(`SELECT id, ts, role, content, tool_name FROM session_messages
			WHERE session_id = ? ORDER BY ts DESC LIMIT ?`, sid, limit)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var m memoryRow
			if err := rs.Scan(&m.ID, &m.TS, &m.Role, &m.Content, &m.ToolName); err != nil {
				return err
			}
			rows = append(rows, m)
		}
		return nil
	})
	if rows == nil {
		rows = []memoryRow{}
	}
	writeOK(w, rows)
}

func memorySearchHandler(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, 400, fmt.Errorf("q required"))
		return
	}
	var rows []memoryRow
	_ = readDB(func(db *sql.DB) error {
		like := "%" + q + "%"
		rs, err := db.Query(`SELECT id, ts, role, content, tool_name FROM session_messages
			WHERE content LIKE ? OR tool_name LIKE ? ORDER BY ts DESC LIMIT 100`, like, like)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var m memoryRow
			if err := rs.Scan(&m.ID, &m.TS, &m.Role, &m.Content, &m.ToolName); err != nil {
				return err
			}
			m.Score = scoreMatch(q, m.Content)
			rows = append(rows, m)
		}
		return nil
	})
	writeOK(w, rows)
}

func memoryRememberHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fact string `json:"fact"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if req.Fact == "" {
		writeErr(w, 400, fmt.Errorf("fact required"))
		return
	}
	id := fmt.Sprintf("memo_%d", time.Now().UnixNano())
	if err := withDB(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO memory_rememberings (id, content) VALUES (?, ?)`, id, req.Fact)
		return err
	}); err != nil {
		writeErr(w, 500, err)
		return
	}
	audit("memory_remember", id, "")
	writeOK(w, map[string]string{"id": id})
}

func scoreMatch(q, text string) float64 {
	q = strings.ToLower(strings.TrimSpace(q))
	t := strings.ToLower(text)
	if q == "" || t == "" {
		return 0
	}
	if !strings.Contains(t, q) {
		return 0
	}
	return 1.0 / float64(len(t))
}

func atoiOrDefault(s string, d int) int {
	if s == "" {
		return d
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return d
	}
	return n
}

// listLearnedSkillsHandler returns skills whose source = 'learned'. Skills
// are created on the fly by the chat handler's self-learning distiller.
func listLearnedSkillsHandler(w http.ResponseWriter, r *http.Request) {
	var out []map[string]any
	_ = readDB(func(db *sql.DB) error {
		rs, err := db.Query(`SELECT id, name, description, source, path, enabled, metadata, created_at
			FROM skills WHERE source = 'learned' ORDER BY created_at DESC LIMIT 200`)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var (
				id, name, source, path string
				enabled                int
				description, metadata  string
				created                string
			)
			if err := rs.Scan(&id, &name, &description, &source, &path, &enabled, &metadata, &created); err != nil {
				return err
			}
			body := ""
			if path != "" {
				if data, err := os.ReadFile(path); err == nil {
					body = string(data)
				}
			}
			out = append(out, map[string]any{
				"id": id, "name": name, "description": description,
				"source": source, "path": path, "enabled": enabled == 1,
				"created": created, "body": body, "metadata": metadata,
			})
		}
		return nil
	})
	if out == nil {
		out = []map[string]any{}
	}
	writeOK(w, out)
}

// ============================================================
// Self-learning distiller
// ============================================================
//
// When the assistant invokes WebSearch or WebFetch we don't know what came
// back at the chat-proxy level (claw-core already wrapped it). But we DO
// know:
//   * the user prompt that triggered the search
//   * the assistant's final reply, which paraphrases / cites the answer
//
// Persist a learned_skills row keyed by a sanitized version of the query
// and write a SKILL.md under ~/.omnicode/skills/learned/<name>.md so the
// existing skills loader can surface it next session. Keep this best-
// effort: every error is logged and swallowed so a corrupted row never
// breaks the user's chat.

func distillLearnedSkills(toolUses []map[string]any, sessionID, prompt string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "distillLearnedSkills panic: %v\n", r)
		}
	}()
	for _, tu := range toolUses {
		name, _ := tu["name"].(string)
		if name != "WebSearch" && name != "WebFetch" {
			continue
		}
		input, _ := tu["input"].(map[string]any)
		var query, url string
		if q, ok := input["query"].(string); ok {
			query = q
		}
		if u, ok := input["url"].(string); ok {
			url = u
		}
		if query == "" && url == "" {
			continue
		}
		if query == "" {
			query = url
		}
		title := sanitizeSkillName(query)
		if title == "" {
			continue
		}
		body := synthesizeSkillBody(name, query, url, prompt, tu)
		if err := saveLearnedSkill(title, name, query, url, body); err != nil {
			fmt.Fprintf(os.Stderr, "saveLearnedSkill %s: %v\n", title, err)
		}
	}
}

func synthesizeSkillBody(name, query, url, prompt string, tu map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", query)
	fmt.Fprintf(&b, "_Auto-learned by OmniCode via %s._\n\n", name)
	fmt.Fprintf(&b, "## Source\n\n")
	if url != "" {
		fmt.Fprintf(&b, "- URL: %s\n", url)
	}
	fmt.Fprintf(&b, "- Query: %s\n", query)
	fmt.Fprintf(&b, "- Captured: %s\n\n", nowString())
	fmt.Fprintf(&b, "## When to use this skill\n\n")
	fmt.Fprintf(&b, "Invoke this skill the next time a request matches the query above.\n\n")
	fmt.Fprintf(&b, "## Prompt context\n\n```\n%s\n```\n\n", prompt)
	if s, ok := tu["result"].(string); ok && s != "" {
		fmt.Fprintf(&b, "## Distilled answer\n\n%s\n", s)
	}
	fmt.Fprintf(&b, "## Notes\n\n")
	fmt.Fprintf(&b, "This file is re-written every time the agent performs the same query, so keep it up to date by adding follow-up notes here.\n")
	return b.String()
}

func sanitizeSkillName(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	var b strings.Builder
	for _, r := range q {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == ' ' {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return ""
	}
	if len(out) > 80 {
		out = out[:80]
	}
	out = strings.ReplaceAll(out, " ", "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}

func saveLearnedSkill(name, source, query, url, body string) error {
	skillsDir := filepath.Join(config.InstallPath, "skills", "learned")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return err
	}
	skillPath := filepath.Join(skillsDir, name+".md")
	if err := os.WriteFile(skillPath, []byte(body), 0644); err != nil {
		return err
	}
	// The skill body lives in the markdown file and in learned_skills.body.
	// The skills table only mirrors the listing fields. Both writes share
	// one transaction so a failure cannot leave them half-updated.
	return withDB(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO learned_skills (name, query, body, source, url, created_at)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(name) DO UPDATE SET
				query = excluded.query,
				body  = excluded.body,
				source = excluded.source,
				url = excluded.url,
				created_at = CURRENT_TIMESTAMP`,
			name, query, body, source, url); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO skills (id, name, description, source, path, enabled)
			VALUES (?, ?, ?, 'learned', ?, 1)
			ON CONFLICT(id) DO UPDATE SET
				description = excluded.description,
				path        = excluded.path,
				enabled     = 1`,
			"learned_"+name, name, "auto-learned from "+source+": "+query, skillPath)
		return err
	})
}
