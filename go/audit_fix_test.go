package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLearnedSkillUpsert(t *testing.T) {
	dir := t.TempDir()
	orig := config
	closeSharedDB()
	config.InstallPath = dir
	config.Database = filepath.Join(dir, "omnicode.db")
	t.Cleanup(func() {
		closeSharedDB()
		config = orig
	})

	if err := saveLearnedSkill("probe-skill", "WebSearch", "probe query", "https://example.com", "body one"); err != nil {
		t.Fatal(err)
	}
	if err := saveLearnedSkill("probe-skill", "WebSearch", "probe query 2", "https://example.com/2", "body two"); err != nil {
		t.Fatal(err)
	}

	db, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	var body, desc, path string
	if err := db.QueryRow(`SELECT body FROM learned_skills WHERE name = ?`, "probe-skill").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "body two" {
		t.Fatalf("body %q", body)
	}
	if err := db.QueryRow(`SELECT description, path FROM skills WHERE id = ?`, "learned_probe-skill").Scan(&desc, &path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "probe query 2") {
		t.Fatalf("description %q", desc)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM skills`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("skills rows %d", n)
	}
}

func TestSystemPromptLoadsEditedFile(t *testing.T) {
	dir := t.TempDir()
	orig := config.InstallPath
	config.InstallPath = dir
	resetPromptCache()
	t.Cleanup(func() {
		config.InstallPath = orig
		resetPromptCache()
	})

	got := omnicodeSystemPrompt(dir, "s1", nil, nil)
	if !strings.Contains(got, "AGENT LOOP") {
		t.Fatalf("missing default instructions: %s", got)
	}
	written := filepath.Join(dir, "system.md")
	if _, err := os.Stat(written); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(written, []byte("CUSTOM RULE from the file"), 0644); err != nil {
		t.Fatal(err)
	}
	got = omnicodeSystemPrompt(dir, "s1", nil, nil)
	if !strings.Contains(got, "CUSTOM RULE from the file") {
		t.Fatalf("edit was not picked up: %s", got)
	}
	if strings.Contains(got, "AGENT LOOP") {
		t.Fatal("stale default still in the prompt")
	}
}

func TestSuggestedModelReadsJSONAndBareHint(t *testing.T) {
	got := suggestedModel(`{"error":{"message":"Use \"other-id\" instead","code":"model_not_found"}}`)
	if got != "other-id" {
		t.Fatalf("quoted %q", got)
	}
	got = suggestedModel(`{"error":{"message":"Please use foo-bar instead"}}`)
	if got != "foo-bar" {
		t.Fatalf("bare %q", got)
	}
}

func TestAffordableTokensReadsJSONMessage(t *testing.T) {
	if n := affordableTokens(`{"error":{"message":"can only afford 100"}}`); n != 100 {
		t.Fatalf("got %d", n)
	}
}

func TestSafeJoinRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := safeJoin(root, "../outside"); err == nil {
		t.Fatal("parent escape")
	}
	if _, err := safeJoin(root, "/v1/file"); err == nil {
		t.Fatal("rooted path")
	}
	if _, err := safeJoin(root, `..\outside`); err == nil {
		t.Fatal("backslash escape")
	}
	got, err := safeJoin(root, "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.ToLower(got), strings.ToLower(root)) {
		t.Fatalf("got %s", got)
	}
}

func TestSafeJoinRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	if _, err := safeJoin(root, "link/new.txt"); err == nil {
		t.Fatal("new file through symlink was allowed")
	}
}

func TestListenLocalHoldsThePort(t *testing.T) {
	ln, port, err := listenLocal(0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if port < 4450 || port >= 4500 {
		t.Fatalf("port %d", port)
	}
	if _, err := net.Listen("tcp", ln.Addr().String()); err == nil {
		t.Fatal("port was not held")
	}
}

func TestListenLocalPinnedReportsBusy(t *testing.T) {
	ln, port, err := listenLocal(0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, _, err := listenLocal(port, true); err == nil {
		t.Fatal("expected a busy-port error")
	}
}
