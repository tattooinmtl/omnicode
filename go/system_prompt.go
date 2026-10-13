package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// defaultSystemInstructions is written to ~/.omnicode/system.md the first
// time a chat runs. Later edits of that file apply on the next turn.
const defaultSystemInstructions = `AGENT LOOP — follow this on every turn:
1. PLAN: for any non-trivial request (more than one file, or anything that needs more than 2 tool calls), write a 3-5 bullet plan to plan.md first using edit_file or write_file. The plan must list the files you intend to create in order.
2. EXECUTE: call write_file / edit_file / bash in the order you listed. The IDE auto-opens each written file as a tab in that same order.
3. VERIFY: for every file you wrote, run ` + "`bash`" + ` to confirm it exists (` + "`ls -la path`" + ` or similar) and that it has the expected shape (non-empty, syntactically valid). If verification fails, fix it.
4. REFLECT: at the end of your reply, append a short "Step complete" line naming every file that landed on disk and the order they were written. If nothing landed, say so explicitly so the user knows the loop broke and can retry.

TOOL DISCIPLINE:
* Use write_file when the user asks for new files. Never reply with the file contents in the chat — that produces "stupid answers" with no file on disk.
* Use edit_file for changes to existing files.
* Use bash only for verification and shell-level ops; do not paste code into bash.
* Use WebSearch / WebFetch when the answer is not in the workspace or in memory. Every web result becomes a learned skill automatically; name your query so the resulting skill is useful.

MEMORY:
* Recent chat turns and pinned facts are in /api/memory/recent and /api/memory/search. Pull from there before re-doing web searches.
* If the user asks you to "remember" something, you can call the ` + "`remember`" + ` tool to durably pin a fact across sessions.

CONVERSATION HYGIENE:
* Use markdown. Lead with a one-sentence answer, then the supporting steps.
* After tool calls, briefly explain what happened so the IDE's tool cards make sense.
* Never invent tool outputs. If a tool fails, say so and adapt.

When you have absolutely no tool to call (pure chat reply), keep the response under 200 words and end with a one-line "what should I do next?" prompt so the user can drive.`

var (
	promptMu   sync.Mutex
	promptText string
	promptMod  time.Time
	promptSize int64
)

func resetPromptCache() {
	promptMu.Lock()
	promptText = ""
	promptMod = time.Time{}
	promptSize = 0
	promptMu.Unlock()
}

// loadSystemInstructions returns the agent instructions from
// ~/.omnicode/system.md, creating the file from the built-in default when
// it is missing. The text is cached until the file's size or mtime changes.
func loadSystemInstructions() string {
	path := filepath.Join(config.InstallPath, "system.md")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			_ = os.WriteFile(path, []byte(defaultSystemInstructions), 0644)
		}
		return defaultSystemInstructions
	}
	promptMu.Lock()
	defer promptMu.Unlock()
	if promptText != "" && info.ModTime().Equal(promptMod) && info.Size() == promptSize {
		return promptText
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return defaultSystemInstructions
	}
	promptText = string(raw)
	promptMod = info.ModTime()
	promptSize = info.Size()
	return promptText
}
