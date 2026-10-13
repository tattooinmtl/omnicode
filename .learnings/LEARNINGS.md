# Learnings

Corrections, knowledge gaps, best practices.

## Entries

## [LRN-20251012-001] user_wants_visible_edit_pipeline_not_settings_panels

**Logged**: 2025-10-12T00:00:00Z
**Priority**: critical
**Status**: pending
**Area**: frontend

### Summary
Treat every integration request as "make the agent visible to me as I work",
not "build me another settings panel". The first OmniCode shell has 14 sidebar
entries that are all CRUD for stored rows in SQLite — none of them let the
user see or edit a project file. The IDE surface must lead with what the
agent is producing.

### Details
User feedback: "no real coding IDE functions in this app … the interface is
good missing the editor to code in do add it. add a tab system and a well
organized creation pipeline."

### Suggested Action
- Add the Editor view as a primary nav item, not buried under Settings.
- Plan.md is the brainstorming trace and should be the first opened tab on
  any new session.
- The creation pipeline's invariant is: tabs are added in arrival order from
  the assistant's `write_file` tool uses. The user can close / reorder them,
  but the sequence is the audit trail.

### Metadata
- Source: user_feedback
- Related Files: AUDIT.md
- See Also: FEAT-20251012-001

---

## [LRN-20251013-001] agentic_loop_must_be_visible_and_strict

**Logged**: 2025-10-13T00:00:00Z
**Priority**: critical
**Status**: resolved
**Area**: backend

### Summary
The user reported Claude Code parity in `omnicode` and called out that
"the clawd-code for sure aint working at all — we ll have to build the
agentic loop to be a bit like a gauntlet-loop and superpowers mix".
The fix that finally made the loop usable was *not* more code paths —
it was a strict, four-step system prompt.

### Details
- Step 1 — PLAN to plan.md before any multi-file task.
- Step 2 — EXECUTE write_file/edit_file/bash in the listed order.
- Step 3 — VERIFY every file landed (bash ls / syntax check).
- Step 4 — REFLECT with a "Step complete" line.

The strict prompt also enforces: write_file for new files (never paste
code in chat), WebSearch → distil as a learned skill, consult memory
before re-doing web searches, never invent tool outputs.

### Suggested Action
Treat the system prompt as a first-class artifact under source control.
Any drift between what the prompt promises and what the tools actually
do is a parity regression.

### Metadata
- Source: user_feedback
- Related Files:
  - omnicode/go/server.go (omnicodeSystemPrompt)
  - AUDIT-addendum.md
- See Also: LRN-20251012-001, FEAT-20251012-001
