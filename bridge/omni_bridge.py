"""OmniCode provider bridge.

claw-core (the OmniCode Rust REPL) only speaks the Anthropic Messages API at
ANTHROPIC_BASE_URL. This bridge accepts those requests on localhost and
forwards them to any provider configured in Omni (~/.omni/agent/settings.json),
translating Anthropic Messages <-> OpenAI chat/completions, streaming and tool
use included.

Model names are "<provider>/<model>" (e.g. nvidia/glm-5.2, xkiro/mistral-large),
an Omni model key, or an alias from providers.json. API keys are never copied:
they are read at request time from Omni's settings.json, ~/.omni/.env, or
OMNI_<PROVIDER>_KEY environment variables.

Usage:
  python omni_bridge.py                 serve (default port from providers.json)
  python omni_bridge.py --sync          regenerate providers.json from Omni
  python omni_bridge.py --list          list providers / models / aliases
  python omni_bridge.py --resolve NAME  show where a model name routes
"""

import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

HOME = Path(os.environ.get("USERPROFILE") or Path.home())
OMNI_HOME = Path(os.environ.get("OMNI_HOME") or HOME / ".omni")
OMNI_SETTINGS = OMNI_HOME / "agent" / "settings.json"
OMNI_ENV = OMNI_HOME / ".env"
BRIDGE_DIR = Path(__file__).resolve().parent
CONFIG_PATH = BRIDGE_DIR.parent / "providers.json"
LOG_PATH = BRIDGE_DIR / "bridge.log"
DEFAULT_PORT = 47821

_log_lock = threading.Lock()


def log(msg):
    line = time.strftime("%Y-%m-%d %H:%M:%S ") + msg
    with _log_lock:
        try:
            with open(LOG_PATH, "a", encoding="utf-8") as f:
                f.write(line + "\n")
        except OSError:
            pass
    if sys.stderr:
        try:
            print(line, file=sys.stderr, flush=True)
        except Exception:
            pass


def read_json(path, default=None):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return default


def read_dotenv(path):
    out = {}
    try:
        for raw in Path(path).read_text(encoding="utf-8").splitlines():
            line = raw.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            k, v = line.split("=", 1)
            out[k.strip()] = v.strip().strip('"').strip("'")
    except OSError:
        pass
    return out


def is_local(url):
    return url.startswith(("http://localhost", "http://127."))


# Cloudflare-fronted providers (xkiro) reject Python's default User-Agent.
USER_AGENT = "OmniCode-Bridge/1.0 (+claw-core)"


def env_name(name):
    return "OMNI_" + "".join(c if c.isalnum() else "_" for c in name).upper() + "_KEY"


# ---------------------------------------------------------------- config ----

def sync_config():
    """Build providers.json from Omni's settings (no keys are written)."""
    omni = read_json(OMNI_SETTINGS, {}) or {}
    old = read_json(CONFIG_PATH, {}) or {}
    # Endpoints where Omni's URL doesn't match the key (kimi keys are moonshot.ai,
    # not moonshot.cn). Edit freely in providers.json; --sync keeps them.
    overrides = old.get("baseUrlOverrides") or {"kimi": "https://api.moonshot.ai/v1"}
    providers, skipped = {}, []
    for name, p in (omni.get("providers") or {}).items():
        if not isinstance(p, dict) or not p.get("baseUrl"):
            continue
        if name in overrides:
            p = dict(p, baseUrl=overrides[name])
        # Only providers we can actually call: a key somewhere, or a local server.
        if not (is_local(p["baseUrl"]) or provider_key(name, p)):
            skipped.append(name)
            continue
        entry = {"label": p.get("label", name), "baseUrl": p["baseUrl"].rstrip("/")}
        if name == "claude":
            entry["api"] = "anthropic"
        else:
            entry["api"] = "openai"
        if isinstance(p.get("accounts"), dict) and p["accounts"]:
            entry["accounts"] = list(p["accounts"].keys())
        providers[name] = entry

    models = {}
    for key, m in (omni.get("models") or {}).items():
        if not isinstance(m, dict) or m.get("provider") not in providers:
            continue
        models[key] = {k: m[k] for k in ("provider", "id", "maxTokens", "contextWindow", "vision", "reasoning", "free") if k in m}

    # Ask every callable provider which models its key can use right now.
    live_counts = {}
    for name, entry in providers.items():
        ids = fetch_models(name, entry)
        live_counts[name] = None if ids is None else len(ids)
        if ids is None:
            continue
        live = set(ids)
        for key in [k for k, m in models.items() if m["provider"] == name and m.get("id") not in live]:
            del models[key]  # retired / no access
        known = {m.get("id") for m in models.values() if m["provider"] == name}
        for mid in ids:
            if mid not in known:
                models[f"{name}/{mid}"] = {"provider": name, "id": mid}
    print("live models per provider: " + ", ".join(f"{k}={'unreachable' if v is None else v}"
                                                     for k, v in live_counts.items()))

    default = old.get("defaultModel") or omni.get("defaultModel") or next(iter(models), "")
    if default not in models and default not in (old.get("aliases") or {}):
        default = next((k for k in models if k.startswith("xkiro/")), next(iter(models), ""))
    # Keep hand-made aliases that still point somewhere callable.
    aliases = {k: v for k, v in (old.get("aliases") or {}).items()
               if v in models or v.split("/", 1)[0] in providers}
    # Short aliases: the part after "provider/" when it is unique.
    tails = {}
    for key in models:
        tails.setdefault(key.split("/", 1)[-1], []).append(key)
    for tail, keys in tails.items():
        if len(keys) == 1 and tail not in models and tail not in aliases:
            aliases[tail] = keys[0]
    aliases.setdefault("default", default)

    cfg = {
        "_comment": "Generated by omni_bridge.py --sync from ~/.omni/agent/settings.json. "
                    "Keys are NOT stored here; they are read live from Omni. "
                    "Edit defaultModel / aliases / clawModelMap freely; --sync keeps them.",
        "port": old.get("port", DEFAULT_PORT),
        "baseUrlOverrides": overrides,
        "defaultModel": default,
        # claw-core's built-in names (its default is claude-opus-4-6). Used only
        # when the 'claude' provider has no key; otherwise they go to Anthropic.
        "clawModelMap": old.get("clawModelMap") or {
            "claude-opus-4-6": default,
            "claude-sonnet-4-6": default,
            "claude-haiku-4-5-20251213": default,
        },
        "aliases": dict(sorted(aliases.items())),
        "providers": providers,
        "skippedNoKey": skipped,
        "models": models,
    }
    CONFIG_PATH.write_text(json.dumps(cfg, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    return cfg


def fetch_models(name, entry):
    """GET <baseUrl>/models with the provider's key. None when unreachable."""
    keys = provider_key(name, entry) or ["none"]
    for key in keys:
        r = urllib.request.Request(entry["baseUrl"].rstrip("/") + "/models", headers={
            "authorization": f"Bearer {key}", "user-agent": USER_AGENT, "accept": "application/json"})
        try:
            with urllib.request.urlopen(r, timeout=15) as resp:
                data = json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as e:
            if e.code in (401, 403, 429):
                continue
            return None
        except (urllib.error.URLError, OSError, ValueError):
            return None
        items = data.get("data") if isinstance(data, dict) else data
        return sorted({i.get("id") for i in items or [] if isinstance(i, dict) and i.get("id")})
    return None


def load_config():
    cfg = read_json(CONFIG_PATH)
    if not cfg:
        cfg = sync_config()
    return cfg


def provider_key(name, cfg_provider):
    """Look up a provider's key live: Omni settings > env var > ~/.omni/.env."""
    omni = read_json(OMNI_SETTINGS, {}) or {}
    p = (omni.get("providers") or {}).get(name) or {}
    dotenv = read_dotenv(OMNI_ENV)

    def lookup(n):
        return os.environ.get(env_name(n)) or dotenv.get(env_name(n)) or ""

    keys = []
    accounts = p.get("accounts") if isinstance(p.get("accounts"), dict) else {}
    active = p.get("activeAccount")
    order = ([active] if active in accounts else []) + [a for a in accounts if a != active]
    for acct in order:
        keys.append(accounts.get(acct) or lookup(acct))
    keys.append(p.get("apiKey") or lookup(name))
    if name == "atria":
        keys.append(os.environ.get("ATRIA_API_KEY") or dotenv.get("ATRIA_API_KEY") or "")
    if name == "claude":
        keys.append(os.environ.get("OMNI_REAL_ANTHROPIC_KEY", ""))
    seen, out = set(), []
    for k in keys:
        if k and k not in seen:
            seen.add(k)
            out.append(k)
    return out


def resolve(model, cfg):
    """Return (provider_name, provider_cfg, upstream_model_id, model_meta)."""
    providers = cfg.get("providers", {})
    models = cfg.get("models", {})
    name = (model or "").strip()
    for _ in range(5):  # follow alias chains
        if name in cfg.get("aliases", {}):
            name = cfg["aliases"][name]
        else:
            break
    if name.startswith("claude-") and name not in models:
        if "claude" in providers and provider_key("claude", providers["claude"]):
            return "claude", providers["claude"], name, {}
        name = cfg.get("clawModelMap", {}).get(name) or cfg.get("defaultModel") or name
        name = cfg.get("aliases", {}).get(name, name)
    if name in models:
        meta = models[name]
        prov = meta["provider"]
        return prov, providers.get(prov, {}), meta.get("id", name.split("/", 1)[-1]), meta
    if "/" in name:
        prov, rest = name.split("/", 1)
        if prov in providers:
            return prov, providers[prov], rest, {}
    raise LookupError(f"unknown model '{model}'. Use <provider>/<model> with one of: "
                      + ", ".join(sorted(providers)))


# ------------------------------------------------- Anthropic -> OpenAI ------

def _text_of(content):
    if isinstance(content, str):
        return content
    parts = []
    for b in content or []:
        if isinstance(b, dict) and b.get("type") == "text":
            parts.append(b.get("text", ""))
    return "\n".join(parts)


def to_openai(body, upstream_model, meta):
    msgs = []
    system = body.get("system")
    if system:
        msgs.append({"role": "system", "content": _text_of(system)})
    for m in body.get("messages", []):
        role, content = m.get("role"), m.get("content")
        if isinstance(content, str):
            msgs.append({"role": role, "content": content})
            continue
        if role == "assistant":
            text, calls = [], []
            for b in content or []:
                t = b.get("type")
                if t == "text":
                    text.append(b.get("text", ""))
                elif t == "tool_use":
                    calls.append({"id": b.get("id"), "type": "function",
                                  "function": {"name": b.get("name"),
                                               "arguments": json.dumps(b.get("input") or {})}})
            out = {"role": "assistant", "content": "\n".join(text) if text else None}
            if calls:
                out["tool_calls"] = calls
            elif out["content"] is None:
                out["content"] = ""
            msgs.append(out)
        else:
            parts, has_image = [], False
            for b in content or []:
                t = b.get("type")
                if t == "tool_result":
                    c = b.get("content")
                    txt = c if isinstance(c, str) else _text_of(c)
                    if b.get("is_error"):
                        txt = "ERROR: " + txt
                    msgs.append({"role": "tool", "tool_call_id": b.get("tool_use_id"), "content": txt or ""})
                elif t == "text":
                    parts.append({"type": "text", "text": b.get("text", "")})
                elif t == "image":
                    src = b.get("source") or {}
                    if src.get("type") == "base64":
                        url = f"data:{src.get('media_type')};base64,{src.get('data')}"
                    else:
                        url = src.get("url", "")
                    parts.append({"type": "image_url", "image_url": {"url": url}})
                    has_image = True
            if parts:
                if has_image:
                    msgs.append({"role": "user", "content": parts})
                else:
                    msgs.append({"role": "user", "content": "\n".join(p["text"] for p in parts)})

    req = {"model": upstream_model, "messages": msgs}
    max_tokens = body.get("max_tokens")
    if max_tokens:
        cap = meta.get("maxTokens")
        req["max_tokens"] = min(max_tokens, cap) if cap else max_tokens
    for k in ("temperature", "top_p"):
        if k in body:
            req[k] = body[k]
    if body.get("stop_sequences"):
        req["stop"] = body["stop_sequences"]
    tools = body.get("tools") or []
    if tools:
        req["tools"] = [{"type": "function", "function": {
            "name": t["name"], "description": t.get("description", ""),
            "parameters": t.get("input_schema") or {"type": "object", "properties": {}}}}
            for t in tools if t.get("name")]
        tc = body.get("tool_choice") or {}
        if tc.get("type") == "any":
            req["tool_choice"] = "required"
        elif tc.get("type") == "tool":
            req["tool_choice"] = {"type": "function", "function": {"name": tc.get("name")}}
        elif tc.get("type") == "none":
            req["tool_choice"] = "none"
    if body.get("stream"):
        req["stream"] = True
    return req


STOP_MAP = {"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use",
            "function_call": "tool_use", "content_filter": "end_turn"}


def _parse_args(s):
    try:
        v = json.loads(s) if s else {}
        return v if isinstance(v, dict) else {"value": v}
    except ValueError:
        return {"raw": s}


def from_openai(resp, model):
    choice = (resp.get("choices") or [{}])[0]
    msg = choice.get("message") or {}
    content = []
    if msg.get("content"):
        content.append({"type": "text", "text": msg["content"]})
    for tc in msg.get("tool_calls") or []:
        fn = tc.get("function") or {}
        content.append({"type": "tool_use", "id": tc.get("id") or "toolu_" + uuid.uuid4().hex[:20],
                        "name": fn.get("name"), "input": _parse_args(fn.get("arguments"))})
    usage = resp.get("usage") or {}
    stop = STOP_MAP.get(choice.get("finish_reason"), "end_turn")
    if any(b["type"] == "tool_use" for b in content):
        stop = "tool_use"
    return {"id": "msg_" + uuid.uuid4().hex[:24], "type": "message", "role": "assistant",
            "model": model, "content": content or [{"type": "text", "text": ""}],
            "stop_reason": stop, "stop_sequence": None,
            "usage": {"input_tokens": usage.get("prompt_tokens", 0),
                      "output_tokens": usage.get("completion_tokens", 0)}}


# ---------------------------------------------------------------- server ----

def estimate_tokens(body):
    return max(1, len(json.dumps(body, ensure_ascii=False)) // 4)


class Handler(BaseHTTPRequestHandler):
    server_version = "OmniBridge/1.0"

    def log_message(self, *a):
        pass

    def _json(self, status, obj):
        data = json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _error(self, status, msg, kind="api_error"):
        log(f"error {status}: {msg}")
        self._json(status, {"type": "error", "error": {"type": kind, "message": msg}})

    def do_GET(self):
        cfg = load_config()
        if self.path.startswith("/health"):
            return self._json(200, {"ok": True, "default": cfg.get("defaultModel")})
        if self.path.startswith("/v1/models"):
            ids = sorted(set(cfg.get("models", {})) | set(cfg.get("aliases", {})))
            return self._json(200, {"data": [{"id": i, "type": "model"} for i in ids]})
        self._error(404, "not found", "not_found_error")

    def do_POST(self):
        try:
            n = int(self.headers.get("content-length") or 0)
            body = json.loads(self.rfile.read(n) or b"{}")
        except ValueError:
            return self._error(400, "invalid JSON body", "invalid_request_error")
        path = self.path.split("?")[0]
        if path.endswith("/messages/count_tokens"):
            return self._json(200, {"input_tokens": estimate_tokens(body)})
        if not path.endswith("/messages"):
            return self._error(404, f"unsupported path {path}", "not_found_error")

        cfg = load_config()
        try:
            prov_name, prov, upstream, meta = resolve(body.get("model"), cfg)
        except LookupError as e:
            return self._error(404, str(e), "not_found_error")
        keys = provider_key(prov_name, prov)
        if not keys and not is_local(prov.get("baseUrl", "")):
            return self._error(401, f"provider '{prov_name}' has no API key in Omni "
                               f"(set it with `/apikey {prov_name} <key>` in Omni or {env_name(prov_name)})",
                               "authentication_error")
        keys = keys or ["none"]
        log(f"{body.get('model')} -> {prov_name}:{upstream} stream={bool(body.get('stream'))}")

        if prov.get("api") == "anthropic":
            return self._passthrough_anthropic(prov, upstream, body, keys)

        req = to_openai(body, upstream, meta)
        url = prov["baseUrl"].rstrip("/") + "/chat/completions"
        resp, err = None, None
        for key in keys:  # multi-account failover on 401/429
            r = urllib.request.Request(url, data=json.dumps(req).encode(), method="POST", headers={
                "content-type": "application/json", "authorization": f"Bearer {key}", "user-agent": USER_AGENT,
                "accept": "text/event-stream" if req.get("stream") else "application/json"})
            try:
                resp = urllib.request.urlopen(r, timeout=600)
                break
            except urllib.error.HTTPError as e:
                err = (e.code, e.read().decode("utf-8", "replace")[:2000])
                if e.code not in (401, 403, 429):
                    break
            except (urllib.error.URLError, OSError) as e:
                err = (502, f"cannot reach {prov_name} at {prov['baseUrl']}: {e}")
                break
        if resp is None:
            code, text = err
            kind = {410: "not_found_error", 401: "authentication_error", 403: "permission_error", 429: "rate_limit_error",
                    404: "not_found_error", 400: "invalid_request_error"}.get(code, "api_error")
            code = 404 if code == 410 else code
            return self._error(code if code in (400, 401, 403, 404, 429) else 502,
                               f"{prov_name}: {text}", kind)

        model_label = body.get("model") or upstream
        if req.get("stream"):
            self._stream(resp, model_label, estimate_tokens(body))
        else:
            with resp:
                data = json.loads(resp.read() or b"{}")
            self._json(200, from_openai(data, model_label))

    def _passthrough_anthropic(self, prov, upstream, body, keys):
        body = dict(body, model=upstream)
        url = prov["baseUrl"].rstrip("/") + "/messages"
        r = urllib.request.Request(url, data=json.dumps(body).encode(), method="POST", headers={
            "content-type": "application/json", "x-api-key": keys[0], "user-agent": USER_AGENT,
            "anthropic-version": self.headers.get("anthropic-version", "2023-06-01")})
        try:
            resp = urllib.request.urlopen(r, timeout=600)
        except urllib.error.HTTPError as e:
            data = e.read()
            self.send_response(e.code)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(data)))
            self.end_headers()
            return self.wfile.write(data)
        self.send_response(200)
        self.send_header("content-type", resp.headers.get("content-type", "application/json"))
        self.end_headers()
        with resp:
            while chunk := resp.read(8192):
                self.wfile.write(chunk)
                self.wfile.flush()

    def _sse(self, event, data):
        self.wfile.write(f"event: {event}\ndata: {json.dumps(data)}\n\n".encode())
        self.wfile.flush()

    def _stream(self, resp, model, input_estimate):
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.send_header("cache-control", "no-cache")
        self.send_header("connection", "close")
        self.end_headers()
        self.close_connection = True
        self._sse("message_start", {"type": "message_start", "message": {
            "id": "msg_" + uuid.uuid4().hex[:24], "type": "message", "role": "assistant",
            "model": model, "content": [], "stop_reason": None, "stop_sequence": None,
            "usage": {"input_tokens": input_estimate, "output_tokens": 0}}})

        block = -1          # index of the open Anthropic content block
        text_open = False
        tools = {}          # openai tool index -> anthropic block index
        finish, usage = None, {}

        def close_text():
            nonlocal text_open
            if text_open:
                self._sse("content_block_stop", {"type": "content_block_stop", "index": block})
                text_open = False

        try:
            with resp:
                for raw in resp:
                    line = raw.decode("utf-8", "replace").strip()
                    if not line.startswith("data:"):
                        continue
                    payload = line[5:].strip()
                    if payload == "[DONE]":
                        break
                    try:
                        chunk = json.loads(payload)
                    except ValueError:
                        continue
                    if chunk.get("usage"):
                        usage = chunk["usage"]
                    for ch in chunk.get("choices") or []:
                        delta = ch.get("delta") or {}
                        if delta.get("content"):
                            if not text_open:
                                block += 1
                                text_open = True
                                self._sse("content_block_start", {"type": "content_block_start", "index": block,
                                                                  "content_block": {"type": "text", "text": ""}})
                            self._sse("content_block_delta", {"type": "content_block_delta", "index": block,
                                                              "delta": {"type": "text_delta", "text": delta["content"]}})
                        for pos, tc in enumerate(delta.get("tool_calls") or []):
                            ti = tc.get("index", pos)
                            fn = tc.get("function") or {}
                            if ti not in tools:
                                close_text()
                                if tools:
                                    self._sse("content_block_stop", {"type": "content_block_stop", "index": block})
                                block += 1
                                tools[ti] = block
                                self._sse("content_block_start", {"type": "content_block_start", "index": block,
                                          "content_block": {"type": "tool_use",
                                                            "id": tc.get("id") or "toolu_" + uuid.uuid4().hex[:20],
                                                            "name": fn.get("name") or "", "input": {}}})
                            if fn.get("arguments"):
                                self._sse("content_block_delta", {"type": "content_block_delta", "index": tools[ti],
                                          "delta": {"type": "input_json_delta", "partial_json": fn["arguments"]}})
                        if ch.get("finish_reason"):
                            finish = ch["finish_reason"]
        except Exception as e:  # upstream dropped mid-stream
            log(f"stream error: {e}")
        close_text()
        if tools:
            self._sse("content_block_stop", {"type": "content_block_stop", "index": block})
        if block < 0:
            self._sse("content_block_start", {"type": "content_block_start", "index": 0,
                                              "content_block": {"type": "text", "text": ""}})
            self._sse("content_block_stop", {"type": "content_block_stop", "index": 0})
        stop = "tool_use" if tools else STOP_MAP.get(finish, "end_turn")
        self._sse("message_delta", {"type": "message_delta",
                                    "delta": {"stop_reason": stop, "stop_sequence": None},
                                    "usage": {"input_tokens": usage.get("prompt_tokens", input_estimate),
                                              "output_tokens": usage.get("completion_tokens", 0)}})
        self._sse("message_stop", {"type": "message_stop"})


def main(argv):
    if "--sync" in argv:
        cfg = sync_config()
        print(f"wrote {CONFIG_PATH}: {len(cfg['providers'])} providers, "
              f"{len(cfg['models'])} models, {len(cfg['aliases'])} aliases")
        return
    cfg = load_config()
    if "--list" in argv:
        print(f"default: {cfg.get('defaultModel')}\n\nproviders:")
        for name, p in cfg["providers"].items():
            state = "key ok" if provider_key(name, p) else "no key"
            if is_local(p["baseUrl"]):
                state = "local"
            print(f"  {name:<12} {state:<7} {p['baseUrl']}")
        print("\nmodels:")
        for k, m in cfg["models"].items():
            print(f"  {k:<42} -> {m.get('id')}")
        print("\naliases:")
        for k, v in cfg["aliases"].items():
            print(f"  {k:<28} -> {v}")
        print("\nAny <provider>/<model-id> also works, e.g. openrouter/anthropic/claude-sonnet-4.5")
        return
    if "--resolve" in argv:
        name = argv[argv.index("--resolve") + 1]
        prov, p, up, _ = resolve(name, cfg)
        print(f"{name} -> {prov} ({p.get('baseUrl')}) model={up}")
        return
    port = int(os.environ.get("OMNI_BRIDGE_PORT") or cfg.get("port") or DEFAULT_PORT)
    srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    log(f"omni bridge listening on http://127.0.0.1:{port} (default {cfg.get('defaultModel')})")
    srv.serve_forever()


if __name__ == "__main__":
    main(sys.argv[1:])
