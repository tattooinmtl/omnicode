import sqlite3

DB = r"C:\Users\ThePa\.omnicode\omnicode.db"

conn = sqlite3.connect(DB)
cur = conn.cursor()

fixes = [
    # (id, name, kind, base_url)  -> base_url None means leave unchanged
    ("kimi", "Moonshot Kimi", "custom", "https://api.moonshot.ai/v1"),
    ("openrouter", "OpenRouter", "openrouter", "https://openrouter.ai/api/v1"),
    ("grok", "xAI (grok)", "openai", "https://api.x.ai/v1"),
    ("xai", "xAI", "openai", "https://api.x.ai/v1"),
    ("minimax.io", "MiniMax", "custom", "https://api.minimax.io/v1"),
    ("minimax", "MiniMax (legacy)", "custom", "https://api.minimax.io/v1"),
    ("cursor", "Cursor", "custom", "https://api.cursor.sh/v1"),
    ("atria", "Atria ASI", "custom", "https://api.atria-asi.ai/v1"),
    ("agnes", "Agnes AI", "custom", "https://apihub.agnes-ai.com/v1"),
    ("xkiro", "xKiro", "custom", "https://api.xkiro.com/v1"),
    ("ollama", "Ollama (local)", "custom", "http://localhost:11434/v1"),
    ("local", "llama.cpp (local)", "custom", "http://127.0.0.1:8080/v1"),
    ("nvidia", "NVIDIA NIM", "openai", "https://integrate.api.nvidia.com/v1"),
    ("openai", "OpenAI", "openai", "https://api.openai.com/v1"),
    ("groq", "Groq", "openai", "https://api.groq.com/openai/v1"),
    ("deepseek", "DeepSeek", "openai", "https://api.deepseek.com/v1"),
    ("google", "Google Gemini", "openai", "https://generativelanguage.googleapis.com/v1beta/openai"),
    ("mistral", "Mistral", "openai", "https://api.mistral.ai/v1"),
    ("together", "Together AI", "openai", "https://api.together.xyz/v1"),
    ("fireworks", "Fireworks AI", "openai", "https://api.fireworks.ai/inference/v1"),
    ("claude", "Claude (Anthropic)", "anthropic", "https://api.anthropic.com/v1"),
]

for pid, name, kind, url in fixes:
    cur.execute(
        "UPDATE providers SET name=?, kind=?, base_url=? WHERE id=?",
        (name, kind, url, pid),
    )
    if cur.rowcount == 0:
        cur.execute(
            "INSERT INTO providers (id, name, kind, base_url, api_key_ref, default_model,"
            " enabled, metadata) VALUES (?,?,?,?,?,?,?,?)",
            (pid, name, kind, url, "", "", 1, ""),
        )
        print("added  ", pid, "->", url)
    else:
        print("updated", pid, "->", url)

# Drop stale/duplicate rows: the old seeded Anthropic row, and 'minimax' which
# points at the exact same endpoint as the canonical 'minimax.io' id.
for gone in ("prov_20261011204723", "minimax"):
    cur.execute("DELETE FROM providers WHERE id = ?", (gone,))
    if cur.rowcount:
        print("removed duplicate row", gone)

conn.commit()

cur.execute("SELECT id, name, kind, base_url FROM providers ORDER BY id")
rows = cur.fetchall()
conn.close()
print()
print("Total providers:", len(rows))
for r in rows:
    print("  ", r[0], "|", r[1], "|", r[2], "|", r[3])