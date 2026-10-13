// Provider selection memory.
//
// The app remembers which provider the user last chose and keeps using it on
// every request. It only moves off that provider when the user explicitly
// picks another one, or when the provider is demonstrably down — in which
// case it fails over to the configured default and remembers that instead.

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	settingLastProvider = "agent.last_provider"
	settingLastModel    = "agent.last_model"
)

// rememberProvider persists the user's explicit choice so it survives restarts.
func rememberProvider(id string) {
	if id == "" {
		return
	}
	_ = setSetting(settingLastProvider, id)
}

func rememberModel(model string) {
	if model == "" {
		return
	}
	_ = setSetting(settingLastModel, model)
}

// lastProvider resolves the provider to use for the next request.
//
// Order: the value just sent by the client (a deliberate user change) ->
// the remembered provider -> the default from settings.json.
// coreBaseURL adapts a provider's configured base URL to what claw-core
// expects. claw-core always appends "/v1/messages", so a base that already
// ends in "/v1" would produce "/v1/v1/messages" and a 404. Strip it.
func coreBaseURL(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return ""
	}
	for _, suffix := range []string{"/v1", "/openai/v1", "/api/v1"} {
		if strings.HasSuffix(b, suffix) {
			return strings.TrimSuffix(b, suffix)
		}
	}
	return b
}

// setEnv sets or replaces a key in a []string environment slice.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, line := range env {
		if strings.HasPrefix(line, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func lastProvider(requested string) string {
	if s := strings.TrimSpace(requested); s != "" {
		return s
	}
	if v := getSetting(settingLastProvider); v != "" {
		return v
	}
	if s := getAgentSettings(); s != nil && s.DefaultProvider != "" {
		return s.DefaultProvider
	}
	return ""
}

// lastModel is the same idea for the model id.
//
// The value is run through resolveModelID because settings.json stores
// friendly aliases ("xkiro/mistral-medium-3.5") while the endpoint expects
// the real upstream id ("mistralai/mistral-medium-3.5").
func lastModel(requested string) string {
	if m := strings.TrimSpace(requested); m != "" {
		return resolveModelID(m)
	}
	if v := getSetting(settingLastModel); v != "" {
		return resolveModelID(v)
	}
	if s := getAgentSettings(); s != nil {
		return resolveModelID(s.DefaultModel)
	}
	return ""
}

// healModelForProvider drops a remembered model the provider does not serve.
//
// Switching provider in the UI leaves the old provider's model id behind
// (e.g. "MiniMax-M3" while now on xkiro), which the endpoint answers with a
// 404. Prefer the settings.json default for this provider, then anything the
// provider actually declares.
func healModelForProvider(providerID, model string) string {
	if providerID == "" || model == "" {
		return model
	}
	// Already valid for this provider? Keep it.
	for _, declared := range modelsForProvider(providerID) {
		if declared == model {
			return model
		}
	}
	if s := getAgentSettings(); s != nil {
		if s.DefaultProvider == providerID && s.DefaultModel != "" {
			if id := resolveModelID(s.DefaultModel); id != "" {
				return id
			}
		}
	}
	if declared := modelsForProvider(providerID); len(declared) > 0 {
		return declared[0]
	}
	return model
}

// providerFailureWords mark an error as "this endpoint is not working",
// as opposed to a bad prompt or a refusal.
var providerFailureWords = []string{
	// auth / quota
	"401", "403", "unauthorized", "forbidden", "invalid api key",
	"invalid x-api-key", "authentication", "permission denied",
	"429", "rate limit", "quota", "insufficient_quota",
	// server-side
	"500", "502", "503", "504", "internal server error", "bad gateway",
	"service unavailable", "overloaded",
	// transport / DNS / TLS
	"connection refused", "connection reset", "connection aborted",
	"actively refused", "connectex",
	"no such host", "could not resolve", "name or service not known",
	"timeout", "timed out", "eof", "unreachable", "certificate",
	"tls handshake", "dns", "network is unreachable", "host is unreachable",
	"error sending request", "failed to connect", "empty reply",
}

// isProviderDown reports whether an error means the endpoint itself failed.
func isProviderDown(output string) bool {
	l := strings.ToLower(output)
	for _, w := range providerFailureWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// failoverProvider picks the replacement when the remembered provider is down.
// It prefers the settings.json default, then any other configured provider
// that actually has a key. It never returns the failing provider.
func failoverProvider(failing string) string {
	s := getAgentSettings()
	if s == nil {
		return ""
	}
	if def := s.DefaultProvider; def != "" && def != failing && hasKey(def) {
		return def
	}
	names := make([]string, 0, len(s.Providers))
	for id := range s.Providers {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		if id != failing && hasKey(id) {
			return id
		}
	}
	return ""
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func hasKey(id string) bool { return agentProviderKey(id) != "" }

// providerCreds resolves the base URL and key for a provider id.
//
// The SQLite database is the primary source: providers.base_url points at
// providers.api_key_ref, which points at a row in secrets. agent/settings.json
// is the fallback so the app still works before `omnicode sync-keys` has been
// run (or if a provider was only just added to that file).
func providerCreds(id string) (baseURL, apiKey string) {
	if id == "" {
		return "", ""
	}

	// 1. SQLite: base_url + api_key_ref -> secrets.value.
	dbBase, dbRef := "", ""
	if err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT base_url, COALESCE(api_key_ref,'') FROM providers WHERE id = ?`, id).
			Scan(&dbBase, &dbRef)
	}); err == nil && dbBase != "" {
		if dbRef != "" {
			if v, err := getSecret(dbRef); err == nil {
				apiKey = strings.TrimSpace(v)
			}
		}
		if apiKey != "" {
			return dbBase, apiKey
		}
		// Row exists but has no usable secret yet: fall through to the file
		// so a fresh settings.json still works.
		if s := getAgentSettings(); s != nil {
			if p, ok := s.Providers[id]; ok && strings.TrimSpace(p.APIKey) != "" {
				return dbBase, strings.TrimSpace(p.APIKey)
			}
		}
		return dbBase, ""
	}

	// 2. agent/settings.json.
	if s := getAgentSettings(); s != nil {
		if p, ok := s.Providers[id]; ok {
			return p.BaseURL, strings.TrimSpace(p.APIKey)
		}
	}
	return dbBase, apiKey
}

// probeProvider reports whether the provider answers its OpenAI-compatible
// /models endpoint with the given key.
func probeProvider(baseURL, apiKey string) bool {
	if strings.TrimSpace(baseURL) == "" {
		return false
	}
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// probeChatPath reports whether the provider actually serves the Anthropic
// endpoint that claw-core calls ({base}/v1/messages). A provider can be
// perfectly reachable for /v1/models and still 404 here, which is why the
// provider "test" used to report green while every chat request failed.
//
// The probe posts a real model id when one is known: several gateways answer
// 404 for an *unknown model* as well as for an unknown route, so a bogus
// model name would make a working endpoint look dead.
func probeChatPath(baseURL, apiKey, model string) (bool, string) {
	if strings.TrimSpace(baseURL) == "" {
		return false, "no base URL configured"
	}
	url := coreBaseURL(baseURL) + "/v1/messages"
	if strings.TrimSpace(model) == "" {
		model = "probe"
	}
	payload := fmt.Sprintf(
		`{"model":%q,"max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, model)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return false, "could not build probe request"
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return false, "timed out or could not reach the endpoint"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	detail := strings.TrimSpace(string(body))

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, "reachable, but it does not serve the Anthropic /v1/messages path " +
			"that claw-core calls — this endpoint is OpenAI-style only"
	case resp.StatusCode >= 200 && resp.StatusCode < 500:
		return true, "serves /v1/messages"
	default:
		return false, fmt.Sprintf("endpoint returned HTTP %d %s", resp.StatusCode, detail)
	}
}

// chatTimeout is how long one completion may run. A real answer, especially
// from a reasoning model, often takes longer than the 20s liveness probe.
const chatTimeout = 180 * time.Second

// providerKind is the wire-protocol label stored for a provider, if any.
func providerKind(id string) string {
	if id == "" {
		return ""
	}
	var kind string
	if err := readDB(func(db *sql.DB) error {
		return db.QueryRow(`SELECT kind FROM providers WHERE id = ?`, id).Scan(&kind)
	}); err != nil {
		return ""
	}
	return kind
}

// providerProtocol reports which chat API a provider implements.
//
// "anthropic" speaks /v1/messages. Everything else is called as
// OpenAI-compatible /chat/completions, which is the route Groq, DeepSeek,
// Ollama, OpenRouter, xAI, MiniMax, and the other configured gateways share.
// An official Anthropic host stays on /v1/messages even when the stored kind
// was overwritten to "openai".
func providerProtocol(kind, baseURL string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "anthropic", "claude":
		return "anthropic"
	}
	host := strings.ToLower(urlHost(baseURL))
	if host == "api.anthropic.com" || strings.HasSuffix(host, ".anthropic.com") {
		return "anthropic"
	}
	return "openai"
}

func urlHost(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// openaiBaseURL is the root the OpenAI-compatible client appends
// "/chat/completions" to. Unlike coreBaseURL, a trailing /v1 is kept:
// stripping it produces /chat/completions on the bare host, which 404s.
func openaiBaseURL(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(b), "/chat/completions") {
		return b
	}
	lowered := strings.ToLower(b)
	for _, suffix := range []string{"/v1", "/openai/v1", "/api/v1", "/v1beta/openai"} {
		if strings.HasSuffix(lowered, suffix) {
			return b
		}
	}
	// A bare host and a host that ends in /openai both need /v1.
	return b + "/v1"
}

func chatCompletionsURL(base string) string {
	b := openaiBaseURL(base)
	if b == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(b), "/chat/completions") {
		return b
	}
	return b + "/chat/completions"
}

// usableModel drops placeholders the model dropdown shows before a real id
// has loaded ("loading models…", "(endpoint returned no models)").
func usableModel(model string) string {
	m := strings.TrimSpace(model)
	if m == "" || strings.HasPrefix(m, "(") || strings.HasPrefix(strings.ToLower(m), "loading") {
		return ""
	}
	return m
}

// providerEnvVars maps a provider family to the env pair claw-core reads
// for that client. OpenAI-compatible calls always also set OPENAI_*, because
// that is the wire protocol. Only the matching family is filled in besides
// that, so a Groq key is not also published as MiniMax and xAI.
var providerEnvVars = map[string][2]string{
	"xai":     {"XAI_API_KEY", "XAI_BASE_URL"},
	"grok":    {"XAI_API_KEY", "XAI_BASE_URL"},
	"minimax": {"MINIMAX_API_KEY", "MINIMAX_BASE_URL"},
}

// applyProviderEnv publishes the selected endpoint to claw-core.
//
// claw-core picks a protocol from CLAW_PROVIDER, then from the model name,
// then from whichever API key is set. The selected provider's key is written
// to the env vars for its family. Other families are cleared so a leftover
// key in the parent process cannot steal the request.
func applyProviderEnv(env []string, protocol, providerID, baseURL, apiKey string) []string {
	env = clearCompatEnv(env)
	switch protocol {
	case "anthropic":
		if core := coreBaseURL(baseURL); core != "" {
			env = setEnv(env, "ANTHROPIC_BASE_URL", core)
		}
		if apiKey != "" {
			env = setEnv(env, "ANTHROPIC_API_KEY", apiKey)
		}
		env = setEnv(env, "CLAW_PROVIDER", "anthropic")
	default:
		oai := openaiBaseURL(baseURL)
		env = setEnv(env, "CLAW_PROVIDER", "openai")
		env = setEnv(env, "OPENAI_BASE_URL", oai)
		if apiKey != "" {
			env = setEnv(env, "OPENAI_API_KEY", apiKey)
		}
		if keyName, urlName := vendorEnvNames(providerID, baseURL); keyName != "" {
			env = setEnv(env, urlName, oai)
			if apiKey != "" {
				env = setEnv(env, keyName, apiKey)
			}
		}
		// The CLI default is 64k, which some gateways bill up front and
		// then refuse. 8192 is enough for a tool call and a code edit.
		env = setEnv(env, "CLAW_MAX_TOKENS", "8192")
	}
	return env
}

func clearCompatEnv(env []string) []string {
	for _, key := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY", "XAI_API_KEY", "MINIMAX_API_KEY",
		"XAI_BASE_URL", "MINIMAX_BASE_URL",
	} {
		env = setEnv(env, key, "")
	}
	return env
}

// vendorEnvNames reports the extra env pair for this provider, if claw-core
// has a dedicated client for it. The stored kind is often "openai" even for
// xAI and MiniMax, so the provider id and host are checked too.
func vendorEnvNames(providerID, baseURL string) (string, string) {
	kind := strings.ToLower(strings.TrimSpace(providerKind(providerID)))
	if pair, ok := providerEnvVars[kind]; ok {
		return pair[0], pair[1]
	}
	id := strings.ToLower(strings.TrimSpace(providerID))
	host := strings.ToLower(urlHost(baseURL))
	switch {
	case strings.Contains(id, "minimax") || strings.Contains(host, "minimax"):
		pair := providerEnvVars["minimax"]
		return pair[0], pair[1]
	case id == "xai" || id == "grok" || host == "api.x.ai" || strings.HasSuffix(host, ".x.ai"):
		pair := providerEnvVars["xai"]
		return pair[0], pair[1]
	default:
		return "", ""
	}
}

// wrongProtocol reports an error that means this route does not exist on
// the endpoint, as opposed to the endpoint rejecting the prompt or the model.
func wrongProtocol(msg string) bool {
	l := strings.ToLower(msg)
	if strings.Contains(l, "does not serve") {
		return true
	}
	if !strings.Contains(l, "404") && !strings.Contains(l, "not found") {
		return false
	}
	// "model x not found" and NVIDIA's "Function '…': Not found" mean the
	// route exists and this model is missing. Falling over to the other
	// protocol would not make that model answer.
	if strings.Contains(l, "model") || strings.Contains(l, "function") {
		return false
	}
	return true
}

// modelGone reports that this model id is no longer served. The endpoint
// itself is fine; a later id in the catalog usually still answers.
func modelGone(msg string) bool {
	l := strings.ToLower(msg)
	if strings.Contains(l, "end of life") || strings.Contains(l, "retired") || strings.Contains(l, "410") {
		return true
	}
	if strings.Contains(l, "404") && (strings.Contains(l, "model") || strings.Contains(l, "function")) {
		return true
	}
	return false
}

// modelNotAllowed reports that this model is rejected while the endpoint
// itself is up — typically a free-plan key sent a paid model id.
func modelNotAllowed(msg string) bool {
	l := strings.ToLower(msg)
	return strings.Contains(l, "paid model") ||
		strings.Contains(l, "free plan") ||
		strings.Contains(l, "permission_denied") ||
		strings.Contains(l, "permission denied")
}

var (
	useInsteadRe     = regexp.MustCompile("(?i)use\\s+[\"'`]([^\"'`]*)[\"'`]\\s+instead")
	useInsteadBareRe = regexp.MustCompile(`(?i)use\s+([A-Za-z0-9_./:@+-]+)\s+instead`)
)

// gatewayMessage returns the error message from a JSON gateway body.
// Plain text is returned unchanged.
func gatewayMessage(detail string) string {
	s := strings.TrimSpace(detail)
	if !strings.HasPrefix(s, "{") {
		return detail
	}
	var env struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(s), &env) != nil {
		return detail
	}
	if env.Error.Message != "" {
		return env.Error.Message
	}
	if env.Message != "" {
		return env.Message
	}
	return detail
}

// suggestedModel reads a gateway hint such as `Use "other-id" instead`.
func suggestedModel(detail string) string {
	msg := gatewayMessage(detail)
	if m := useInsteadRe.FindStringSubmatch(msg); len(m) >= 2 && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1])
	}
	if m := useInsteadBareRe.FindStringSubmatch(msg); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// freeModelFor picks a model this provider still serves on a free plan.
// A settings alias that the live catalog no longer lists is skipped: those
// ids come back as "retired" and the endpoint never answers them.
func freeModelFor(providerID, base, key, current string) string {
	var aliases []string
	if s := getAgentSettings(); s != nil {
		for alias, m := range s.Models {
			if m.ID == "" || m.ID == current {
				continue
			}
			if m.Provider != providerID && !strings.HasPrefix(alias, providerID+"/") {
				continue
			}
			id := strings.ToLower(m.ID)
			name := strings.ToLower(alias)
			if m.Free || strings.HasSuffix(id, ":free") || strings.Contains(name, "free") {
				aliases = append(aliases, m.ID)
			}
		}
	}
	var live []string
	if ids, err := fetchModelIDs(base, key); err == nil {
		live = ids
	}
	return pickFreeModel(aliases, live, current)
}

func pickFreeModel(aliases, live []string, current string) string {
	sort.Strings(aliases)
	if len(live) > 0 {
		served := map[string]bool{}
		for _, id := range live {
			served[id] = true
		}
		for _, id := range aliases {
			if id != current && served[id] {
				return id
			}
		}
		for _, id := range live {
			if id != current && strings.HasSuffix(strings.ToLower(id), ":free") {
				return id
			}
		}
		return ""
	}
	if len(aliases) > 0 {
		return aliases[0]
	}
	return ""
}

// defaultMaxTokens caps a completion so a gateway does not assume the
// model's whole context window. OpenRouter bills that assumption up front
// and answers 402 when the balance cannot cover it.
const defaultMaxTokens = 4096

var affordRe = regexp.MustCompile(`(?i)afford\s+(\d+)`)

func affordableTokens(detail string) int {
	m := affordRe.FindStringSubmatch(gatewayMessage(detail))
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// completeChat asks an OpenAI-compatible endpoint for one reply.
func completeChat(baseURL, apiKey, model, prompt string) (string, error) {
	text, _, err := completeChatParts(baseURL, apiKey, model, prompt)
	return text, err
}

// completeChatParts returns the reply and any reasoning text beside it.
// Reasoning stays out of the reply when the endpoint also sent an answer.
func completeChatParts(baseURL, apiKey, model, prompt string) (string, string, error) {
	return completeChatTokensParts(baseURL, apiKey, model, prompt, defaultMaxTokens, true)
}

func completeChatTokens(baseURL, apiKey, model, prompt string, maxTokens int, allowBudgetRetry bool) (string, error) {
	text, _, err := completeChatTokensParts(baseURL, apiKey, model, prompt, maxTokens, allowBudgetRetry)
	return text, err
}

func completeChatTokensParts(baseURL, apiKey, model, prompt string, maxTokens int, allowBudgetRetry bool) (string, string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return "", "", fmt.Errorf("no base URL configured")
	}
	model = usableModel(model)
	if model == "" {
		return "", "", fmt.Errorf("no model available for this endpoint")
	}
	endpoint := chatCompletionsURL(baseURL)
	payload, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("content-type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := (&http.Client{Timeout: chatTimeout}).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("chat request to %s failed: %v", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(body))
		// The gateway priced the request at max_tokens. Retry once inside
		// the balance it just told us we have.
		if resp.StatusCode == http.StatusPaymentRequired && allowBudgetRetry {
			n := affordableTokens(detail)
			if n <= 0 || n >= maxTokens {
				n = 1024
			}
			if n > 16 && n < maxTokens {
				return completeChatTokensParts(baseURL, apiKey, model, prompt, n, false)
			}
		}
		return "", "", fmt.Errorf("endpoint returned HTTP %d %s", resp.StatusCode, detail)
	}
	text, reasoning, err := assistantParts(body)
	if err != nil {
		return "", "", err
	}
	return text, reasoning, nil
}

func assistantText(body []byte) (string, error) {
	text, _, err := assistantParts(body)
	return text, err
}

// assistantParts splits an OpenAI chat payload into the visible reply and
// the reasoning trace. When the endpoint sends only reasoning, that trace
// is also the reply so the caller still has an answer.
func assistantParts(body []byte) (string, string, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				Reasoning        string          `json:"reasoning"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("could not parse chat response: %v", err)
	}
	if len(parsed.Choices) == 0 {
		if len(parsed.Error) > 0 && string(parsed.Error) != "null" {
			return "", "", fmt.Errorf("endpoint error: %s", parsed.Error)
		}
		return "", "", fmt.Errorf("endpoint returned no choices")
	}
	choice := parsed.Choices[0]
	reasoning := strings.TrimSpace(choice.Message.ReasoningContent)
	if reasoning == "" {
		reasoning = strings.TrimSpace(choice.Message.Reasoning)
	}
	if text := decodeContent(choice.Message.Content); strings.TrimSpace(text) != "" {
		return text, reasoning, nil
	}
	if reasoning != "" {
		return reasoning, reasoning, nil
	}
	if strings.TrimSpace(choice.Text) != "" {
		return choice.Text, "", nil
	}
	return "", "", fmt.Errorf("endpoint returned an empty reply")
}

func decodeContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return ""
}

// probeOpenAIChat reports whether the endpoint serves /chat/completions.
func probeOpenAIChat(baseURL, apiKey, model string) (bool, string) {
	if strings.TrimSpace(baseURL) == "" {
		return false, "no base URL configured"
	}
	if strings.TrimSpace(model) == "" {
		model = "probe"
	}
	endpoint := chatCompletionsURL(baseURL)
	payload := fmt.Sprintf(
		`{"model":%q,"max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`, model)
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(payload))
	if err != nil {
		return false, "could not build probe request"
	}
	req.Header.Set("content-type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return false, "timed out or could not reach the endpoint"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	detail := strings.TrimSpace(string(body))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// A missing model is not a missing route. NVIDIA answers 404
		// "Function … Not found" and 410 "end of life" for retired ids
		// on a /chat/completions route that otherwise works.
		msg := fmt.Sprintf("endpoint returned HTTP %d %s", resp.StatusCode, detail)
		if modelGone(msg) {
			return false, msg
		}
		if resp.StatusCode == http.StatusNotFound {
			return false, "does not serve /chat/completions"
		}
		return false, msg
	case resp.StatusCode >= 200 && resp.StatusCode < 500:
		return true, "serves /chat/completions"
	default:
		return false, fmt.Sprintf("endpoint returned HTTP %d %s", resp.StatusCode, detail)
	}
}

// probeChatReady checks the protocol this provider will be called with, and
// the other protocol when the first route is missing.
func probeChatReady(protocol, base, key, model string) (bool, string) {
	if protocol == "anthropic" {
		ok, why := probeChatPath(base, key, model)
		if ok || !wrongProtocol(why) {
			return ok, why
		}
		oaiOK, oaiWhy := probeOpenAIChat(base, key, model)
		if oaiOK {
			return true, oaiWhy
		}
		return false, why + "; " + oaiWhy
	}
	ok, why := probeOpenAIChat(base, key, model)
	if ok || !wrongProtocol(why) {
		return ok, why
	}
	msgOK, msgWhy := probeChatPath(base, key, model)
	if msgOK {
		return true, msgWhy
	}
	return false, why + "; " + msgWhy
}
