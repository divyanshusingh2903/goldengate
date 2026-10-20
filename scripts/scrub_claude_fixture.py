#!/usr/bin/env python3
"""Scrub a real Claude Code session file into a safe test fixture.

Usage: scrub_claude_fixture.py IN.jsonl OUT.jsonl

Keeps the event structure and enum-like values; replaces every free-text
value, id, path, command and timestamp with a deterministic placeholder.
Review the output by eye before committing it. Contains no private data
itself: it only reads the file it is given.
"""
import json, sys

KEEP = {"type", "subtype", "role", "name", "stop_reason", "version", "userType",
        "entrypoint", "permissionMode", "mode", "operation", "level", "isSidechain",
        "isMeta", "is_error", "isSnapshotUpdate", "prNumber"}
DROP = {"thinking", "signature", "usage", "originalFile", "structuredPatch", "oldString",
        "newString", "snapshot", "trackedFileBackups", "rendered", "attachment", "hookInfos",
        "stdout", "stderr", "file", "content_blocks", "context_management", "diagnostics",
        "input_transformations", "shape", "params", "tail", "cacheControl", "modelUsage"}
IDKEYS = {"uuid", "parentUuid", "sessionId", "requestId", "promptId", "id", "tool_use_id",
          "messageId", "leafUuid", "toolUseID", "snapshotMessageId"}
BASE = 1_700_000_000
ids, paths = {}, {}

def fake_id(v):
    ids.setdefault(v, f"00000000-0000-4000-8000-{len(ids):012d}")
    return ids[v]

def fake_path(v):
    paths.setdefault(v, f"/home/user/projects/demo/src/file{len(paths)}.go")
    return paths[v]

def fake_time(i):
    import datetime
    return datetime.datetime.fromtimestamp(BASE + i, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")

def command(c):
    c = c.strip()
    for pre, repl in (("git commit", 'git commit -m "synthetic message"'),
                      ("gh pr create", 'gh pr create --title "synthetic" --body "synthetic"'),
                      ("gh pr merge", "gh pr merge 1 --squash"),
                      ("git push", "git push origin main"),
                      ("git status", "git status"), ("go test", "go test ./...")):
        if c.startswith(pre):
            return repl
    return "echo scrubbed"

def scrub(v, key, ctx, n):
    if isinstance(v, dict):
        out = {}
        for k, x in v.items():
            if k in DROP:
                continue
            nctx = dict(ctx)
            if k == "input" and ctx.get("tool"):
                out[k] = scrub_input(x, ctx["tool"]); continue
            if k == "content" and ctx.get("role_user_result"):
                pass
            out[k] = scrub(x, k, nctx, n)
        if out.get("type") == "tool_use":
            ctx["tool"] = out.get("name")
        return out
    if isinstance(v, list):
        return [scrub(x, key, ctx, n) for x in v]
    if isinstance(v, str):
        if key in KEEP: return v
        if key in IDKEYS: return fake_id(v)
        if key == "timestamp": return fake_time(n[0])
        if key == "cwd": return "/home/user/projects/demo"
        if key == "gitBranch": return "main"
        if key in ("customTitle", "lastPrompt", "agentName"): return "scrubbed title"
        if key == "prUrl": return "https://github.com/example/demo/pull/1"
        if key == "prRepository": return "example/demo"
        if key in ("model", "requestedModel", "advisorModel"): return "model-x"
        if key in ("filePath", "file_path"): return fake_path(v)
        return "scrubbed text"
    return v

def scrub_input(inp, tool):
    if not isinstance(inp, dict): return {}
    out = {}
    if "command" in inp: out["command"] = command(inp["command"]); out["description"] = "scrubbed"
    if "file_path" in inp: out["file_path"] = fake_path(inp["file_path"])
    if tool == "Write": out["content"] = "scrubbed"
    if tool == "Edit": out["old_string"] = "a"; out["new_string"] = "b"; out["replace_all"] = False
    if tool == "Read": pass
    return out

def main(src, dst):
    out = []
    pending = {}
    with open(src) as fh:
        for i, line in enumerate(fh):
            try: ev = json.loads(line)
            except Exception: continue
            if ev.get("type") in ("api-request", "api-request-shape", "api-request-blob",
                                   "file-history-snapshot", "file-history-delta", "queue-operation", "attachment"):
                continue
            n = [i]
            ctx = {}
            msg = ev.get("message")
            if isinstance(msg, dict) and isinstance(msg.get("content"), list):
                for b in msg["content"]:
                    if isinstance(b, dict) and b.get("type") == "tool_use":
                        pending[b["id"]] = (b["name"], command((b.get("input") or {}).get("command", "")))
            s = scrub_event(ev, n, pending)
            out.append(s)
    with open(dst, "w") as fh:
        for e in out:
            fh.write(json.dumps(e, separators=(",", ":")) + "\n")

def scrub_event(ev, n, pending):
    msg = ev.get("message")
    blocks = None
    if isinstance(msg, dict) and isinstance(msg.get("content"), list):
        blocks = msg["content"]
    # handle tool_use inputs with tool context
    if blocks:
        for b in blocks:
            if isinstance(b, dict) and b.get("type") == "tool_use":
                b["input"] = scrub_input(b.get("input"), b.get("name"))
                b["__done"] = True
            if isinstance(b, dict) and b.get("type") == "tool_result":
                err = bool(b.get("is_error"))
                b["content"] = ("Exit code 1\nscrubbed output" if err else "scrubbed output")
            if isinstance(b, dict) and b.get("type") == "thinking":
                b["thinking"] = ""
    tr = ev.get("toolUseResult")
    if isinstance(tr, dict):
        keep = {k: tr[k] for k in ("type", "interrupted", "isImage", "noOutputExpected", "replaceAll", "userModified") if k in tr}
        if "filePath" in tr: keep["filePath"] = fake_path(tr["filePath"])
        keep["stdout"] = "scrubbed output" if "stdout" in tr else None
        keep["stderr"] = "" if "stdout" in tr else None
        ev["toolUseResult"] = {k: v for k, v in keep.items() if v is not None}
    elif tr is not None:
        ev["toolUseResult"] = "scrubbed"
    res = scrub_walk(ev, n)
    n[0] += 1
    return res

def scrub_walk(v, n, key=None):
    if isinstance(v, dict):
        out = {}
        for k, x in v.items():
            if k in DROP or k == "__done" or k == "toolUseResult" and False:
                continue
            if k in ("input", "toolUseResult") and isinstance(x, dict):
                out[k] = {kk: (vv if isinstance(vv, (bool, int, float, type(None))) or kk in KEEP or kk in ("command", "description", "content", "old_string", "new_string", "type", "stdout", "stderr", "filePath", "file_path") and True else vv) for kk, vv in x.items()}
                continue
            out[k] = scrub_walk(x, n, k)
        return out
    if isinstance(v, list):
        return [scrub_walk(x, n, key) for x in v]
    if isinstance(v, str):
        return scrub(v, key, {}, n)
    return v

if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
