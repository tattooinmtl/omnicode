package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAIBaseURLKeepsVersionPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://api.groq.com/openai/v1", "https://api.groq.com/openai/v1"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1"},
		{"https://api.fireworks.ai/inference/v1", "https://api.fireworks.ai/inference/v1"},
		{"https://generativelanguage.googleapis.com/v1beta/openai", "https://generativelanguage.googleapis.com/v1beta/openai"},
		{"https://api.groq.com/openai", "https://api.groq.com/openai/v1"},
		{"https://api.openai.com", "https://api.openai.com/v1"},
		{"http://localhost:11434/v1", "http://localhost:11434/v1"},
	}
	for _, tc := range cases {
		if got := openaiBaseURL(tc.in); got != tc.want {
			t.Errorf("openaiBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestChatCompletionsURL(t *testing.T) {
	got := chatCompletionsURL("https://api.groq.com/openai/v1")
	want := "https://api.groq.com/openai/v1/chat/completions"
	if got != want {
		t.Fatalf("got %s", got)
	}
	got = chatCompletionsURL("https://generativelanguage.googleapis.com/v1beta/openai/")
	want = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestCoreBaseURLStillStripsV1(t *testing.T) {
	if got := coreBaseURL("https://api.anthropic.com/v1"); got != "https://api.anthropic.com" {
		t.Fatalf("got %s", got)
	}
}

func TestProviderProtocol(t *testing.T) {
	if got := providerProtocol("openai", "https://api.anthropic.com/v1"); got != "anthropic" {
		t.Fatalf("anthropic host: %s", got)
	}
	if got := providerProtocol("anthropic", "https://api.groq.com/openai/v1"); got != "anthropic" {
		t.Fatalf("explicit kind: %s", got)
	}
	if got := providerProtocol("custom", "https://api.groq.com/openai/v1"); got != "openai" {
		t.Fatalf("groq: %s", got)
	}
	if got := providerProtocol("", "http://localhost:11434/v1"); got != "openai" {
		t.Fatalf("ollama: %s", got)
	}
	if got := providerProtocol("openai", "https://api.x.ai/v1"); got != "openai" {
		t.Fatalf("xai: %s", got)
	}
}

func TestApplyProviderEnvDoesNotForceAnthropicOnOpenAI(t *testing.T) {
	env := applyProviderEnv(
		[]string{"ANTHROPIC_API_KEY=stale", "OPENAI_API_KEY=stale", "MINIMAX_API_KEY=stale"},
		"openai",
		"groq",
		"https://api.groq.com/openai/v1",
		"groq-key",
	)
	assertEnv(t, env, "CLAW_PROVIDER", "openai")
	assertEnv(t, env, "OPENAI_BASE_URL", "https://api.groq.com/openai/v1")
	assertEnv(t, env, "OPENAI_API_KEY", "groq-key")
	assertEnv(t, env, "XAI_API_KEY", "")
	assertEnv(t, env, "XAI_BASE_URL", "")
	assertEnv(t, env, "MINIMAX_API_KEY", "")
	assertEnv(t, env, "MINIMAX_BASE_URL", "")
	assertEnv(t, env, "ANTHROPIC_API_KEY", "")
}

func TestApplyProviderEnvPublishesOnlyTheMatchingVendor(t *testing.T) {
	env := applyProviderEnv(nil, "openai", "xai", "https://api.x.ai/v1", "xai-key")
	assertEnv(t, env, "OPENAI_API_KEY", "xai-key")
	assertEnv(t, env, "XAI_API_KEY", "xai-key")
	assertEnv(t, env, "XAI_BASE_URL", "https://api.x.ai/v1")
	assertEnv(t, env, "MINIMAX_API_KEY", "")
	assertEnv(t, env, "MINIMAX_BASE_URL", "")

	env = applyProviderEnv(nil, "openai", "minimax.io", "https://api.minimax.io/v1", "mm-key")
	assertEnv(t, env, "MINIMAX_API_KEY", "mm-key")
	assertEnv(t, env, "MINIMAX_BASE_URL", "https://api.minimax.io/v1")
	assertEnv(t, env, "XAI_API_KEY", "")
	assertEnv(t, env, "XAI_BASE_URL", "")
}

func TestApplyProviderEnvAnthropicStripsV1(t *testing.T) {
	env := applyProviderEnv(nil, "anthropic", "claude", "https://api.anthropic.com/v1", "ant-key")
	assertEnv(t, env, "CLAW_PROVIDER", "anthropic")
	assertEnv(t, env, "ANTHROPIC_BASE_URL", "https://api.anthropic.com")
	assertEnv(t, env, "ANTHROPIC_API_KEY", "ant-key")
	assertEnv(t, env, "OPENAI_API_KEY", "")
}

func TestUsableModelDropsPlaceholders(t *testing.T) {
	if usableModel("loading models…") != "" {
		t.Fatal("loading placeholder")
	}
	if usableModel("(endpoint returned no models)") != "" {
		t.Fatal("empty placeholder")
	}
	if usableModel("llama-3.3") != "llama-3.3" {
		t.Fatal("real id")
	}
}

func TestParseModelIDsBareShapes(t *testing.T) {
	got, err := parseModelIDs([]byte(`{"data":[{"id":"b"},{"id":"a"}]}`))
	if err != nil || strings.Join(got, ",") != "a,b" {
		t.Fatalf("envelope: %v %v", got, err)
	}
	got, err = parseModelIDs([]byte(`[{"id":"zeta"},{"id":"alpha"}]`))
	if err != nil || strings.Join(got, ",") != "alpha,zeta" {
		t.Fatalf("objects: %v %v", got, err)
	}
	got, err = parseModelIDs([]byte(`["m2","m1"]`))
	if err != nil || strings.Join(got, ",") != "m1,m2" {
		t.Fatalf("bare: %v %v", got, err)
	}
	if _, err = parseModelIDs([]byte(`not-json`)); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestCompleteChatUsesChatCompletions(t *testing.T) {
	var sawPath, sawAuth, sawModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		sawAuth = r.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		sawModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":[{"type":"text","text":"hello from endpoint"}]}}]}`)
	}))
	defer srv.Close()

	got, err := completeChat(srv.URL+"/v1", "secret", "llama-3.3", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello from endpoint" {
		t.Fatalf("reply %q", got)
	}
	if sawPath != "/v1/chat/completions" {
		t.Fatalf("path %s", sawPath)
	}
	if sawAuth != "Bearer secret" {
		t.Fatalf("auth %s", sawAuth)
	}
	if sawModel != "llama-3.3" {
		t.Fatalf("model %s", sawModel)
	}
}

func TestCompleteChatReportsHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := completeChat(srv.URL+"/v1", "secret", "llama", "hi")
	if err == nil || !wrongProtocol(err.Error()) {
		t.Fatalf("err %v", err)
	}
}

func TestCompleteChatRetriesAffordableBudget(t *testing.T) {
	var calls []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		n, _ := body["max_tokens"].(float64)
		calls = append(calls, int(n))
		if int(n) > 100 {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":{"message":"can only afford 100"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	}))
	defer srv.Close()

	got, err := completeChat(srv.URL+"/v1", "secret", "llama", "hi")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pong" {
		t.Fatalf("reply %q", got)
	}
	if len(calls) != 2 || calls[0] != defaultMaxTokens || calls[1] != 100 {
		t.Fatalf("calls %v", calls)
	}
}

func TestPickFreeModelSkipsRetiredAlias(t *testing.T) {
	got := pickFreeModel(
		[]string{"minimax/minimax-m2.1-highspeed:free"},
		[]string{"minimax/minimax-m2", "qwen/qwen3-max:free", "openai/gpt-oss-20b"},
		"minimax/minimax-m2",
	)
	if got != "qwen/qwen3-max:free" {
		t.Fatalf("got %q", got)
	}
}

func TestProbeOpenAIChatModelMissIsNotMissingRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"Function 'abc': Not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	ok, why := probeOpenAIChat(srv.URL+"/v1", "secret", "01-ai/yi-large")
	if ok {
		t.Fatal("a missing model is not a successful chat")
	}
	if !modelGone(why) {
		t.Fatalf("why %q", why)
	}
	if wrongProtocol(why) {
		t.Fatal("model miss must not switch protocol")
	}
}

func TestAssistantPartsKeepsReasoningBesideTheAnswer(t *testing.T) {
	text, reasoning, err := assistantParts([]byte(`{"choices":[{"message":{"content":"the answer","reasoning_content":"checked the file first"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if text != "the answer" || reasoning != "checked the file first" {
		t.Fatalf("text %q reasoning %q", text, reasoning)
	}
}

func TestParseOmniLineReadsThinking(t *testing.T) {
	kind, text, ok := parseOmniLine(`OMNI {"type":"thinking","text":"look at render.rs"}`)
	if !ok || kind != "thinking" || text != "look at render.rs" {
		t.Fatalf("got %q %q %v", kind, text, ok)
	}
	if _, _, ok := parseOmniLine("plain stderr"); ok {
		t.Fatal("plain stderr is not a progress line")
	}
}

func TestCoreResultListsToolActivity(t *testing.T) {
	msg, steps, reasoning := coreResult(`{"message":"done","reasoning":"traced the call","tool_uses":[{"name":"read_file","input":"{\"path\":\"src/main.rs\"}"},{"name":"bash","input":"{\"command\":\"go test\"}"}]}`)
	if msg != "done" || reasoning != "traced the call" {
		t.Fatalf("msg %q reasoning %q", msg, reasoning)
	}
	if len(steps) != 2 || steps[0] != "read_file · src/main.rs" || steps[1] != "bash · go test" {
		t.Fatalf("steps %v", steps)
	}
}

func TestCoreMessageReadsAgentJSON(t *testing.T) {
	got := coreMessage(`{"message":"bash\nread_file","model":"x","tool_uses":[{"name":"bash"}]}`)
	if got != "bash\nread_file" {
		t.Fatalf("got %q", got)
	}
	used := coreMessage(`{"message":"","tool_uses":[{"name":"read_file"},{"name":"read_file"},{"name":"edit_file"}]}`)
	if used != "Used tools: read_file, edit_file" {
		t.Fatalf("got %q", used)
	}
	if got := coreMessage("plain reply"); got != "plain reply" {
		t.Fatalf("got %q", got)
	}
}

func TestFindClawRootWalksUpToCheckout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "rust"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "CLAW.md"), []byte("# CLAW.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rust", "Cargo.toml"), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "omnicode", "go")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findClawRoot(nested); got != root {
		t.Fatalf("got %q want %q", got, root)
	}
	if findClawRoot(t.TempDir()) != "" {
		t.Fatal("unrelated directory must not match")
	}
}

func TestNormalizePermission(t *testing.T) {
	if normalizePermission(" workspace-write ") != "workspace-write" {
		t.Fatal("workspace-write")
	}
	if normalizePermission("bypass") != "danger-full-access" {
		t.Fatal("bypass")
	}
	if normalizePermission("nope") != "" {
		t.Fatal("unknown mode must stay unset")
	}
}

func TestModelGone(t *testing.T) {
	if !modelGone(`HTTP 410 The model 'meta/llama-3.1-8b-instruct' has reached its end of life`) {
		t.Fatal("410 should count as a retired model")
	}
	if modelGone("endpoint returned HTTP 404") {
		t.Fatal("a bare 404 is a missing route, not a retired model")
	}
}

func TestSuggestedModel(t *testing.T) {
	msg := `Model "old:free" has been retired. Use "minimax/minimax-m2.1-highspeed" instead.`
	if got := suggestedModel(msg); got != "minimax/minimax-m2.1-highspeed" {
		t.Fatalf("got %q", got)
	}
}

func TestWrongProtocolIgnoresModelMiss(t *testing.T) {
	if wrongProtocol(`endpoint returned HTTP 404 {"detail":"Function 'abc': Not found"}`) {
		t.Fatal("model miss must not switch protocol")
	}
	if !wrongProtocol("endpoint returned HTTP 404") {
		t.Fatal("bare 404 is a missing route")
	}
}

func TestResolveAgainstRequiresUniqueID(t *testing.T) {
	models := map[string]AgentModel{
		"groq/llama-3.3-70b":       {ID: "llama-3.3-70b-versatile"},
		"nvidia/llama-3.3-70b":     {ID: "meta/llama-3.3-70b-instruct"},
		"xkiro/mistral-medium-3.5": {ID: "mistralai/mistral-medium-3.5"},
	}
	if got := resolveAgainst(models, "llama-3.3-70b"); got != "llama-3.3-70b" {
		t.Fatalf("ambiguous slug rewritten to %s", got)
	}
	if got := resolveAgainst(models, "xkiro/mistral-medium-3.5"); got != "mistralai/mistral-medium-3.5" {
		t.Fatalf("unique slug got %s", got)
	}
	if got := resolveAgainst(models, "mistral-medium-3.5"); got != "mistralai/mistral-medium-3.5" {
		t.Fatalf("unique slug got %s", got)
	}
}

func assertEnv(t *testing.T, env []string, key, want string) {
	t.Helper()
	prefix := key + "="
	for _, line := range env {
		if strings.HasPrefix(line, prefix) {
			if got := strings.TrimPrefix(line, prefix); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Errorf("%s missing, want %q", key, want)
}
