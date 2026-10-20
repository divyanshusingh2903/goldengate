#!/usr/bin/env python3
"""Scrub a real Codex rollout into a safe test fixture.

Usage: scrub_codex_fixture.py IN.jsonl OUT.jsonl

Keeps the envelope structure, event/tool names, roles and exit codes; replaces
all text, ids, paths, commands, patch bodies and timestamps with placeholders.
Review the output by eye before committing it.
"""
import json, re, sys, datetime

BASE = 1_700_000_000
ids = {}

def fid(v):
    ids.setdefault(v, f"00000000-0000-4000-8000-{len(ids):012d}")
    return ids[v]

def ts(i):
    return datetime.datetime.fromtimestamp(BASE + i, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")

def command(c):
    c = c.strip()
    for pre, repl in (("git commit", 'git commit -m "synthetic"'), ("gh pr create", 'gh pr create --title "synthetic"'),
                      ("git status", "git status"), ("go test", "go test ./...")):
        if c.startswith(pre):
            return repl
    return "echo scrubbed"

def scrub_output(txt):
    m = re.search(r"Process exited with code (-?\d+)", txt)
    if m:
        return f"Chunk ID: 000000\nWall time: 0.0000 seconds\nProcess exited with code {m.group(1)}\nOriginal token count: 1\nOutput:\nscrubbed output\n"
    if "Process running with session ID" in txt:
        return "Chunk ID: 000000\nWall time: 1.0000 seconds\nProcess running with session ID 1\nOriginal token count: 1\nOutput:\nscrubbed\n"
    m = re.search(r"Exit code:\s*(-?\d+)", txt)
    if m:
        return f"Exit code: {m.group(1)}\nWall time: 0.0 seconds\nOutput:\nscrubbed\n"
    return "scrubbed output"

def scrub_patch(inp):
    out, n = ["*** Begin Patch"], 0
    for line in inp.split("\n"):
        for tag in ("Add File", "Update File", "Delete File"):
            if line.startswith(f"*** {tag}: "):
                out.append(f"*** {tag}: src/file{n}.go"); n += 1
                if tag != "Delete File":
                    out.append("@@" if tag == "Update File" else "+scrubbed")
                    out.append("-a" if tag == "Update File" else "")
                    if tag == "Update File": out.append("+b")
    out.append("*** End Patch")
    return "\n".join(out)

def main(src, dst):
    out, i = [], 0
    for line in open(src):
        try: o = json.loads(line)
        except Exception: continue
        t, p = o.get("type"), o.get("payload") or {}
        env = {"timestamp": ts(i), "ordinal": i, "type": t}
        i += 1
        if t == "session_meta":
            env["payload"] = {"id": fid(p.get("id", "x")), "timestamp": ts(0), "cwd": "/home/user/projects/demo", "originator": p.get("originator", "codex-tui"),
                              "cli_version": p.get("cli_version", "0.150.0"), "source": p.get("source", "cli"),
                              "git": {"commit_hash": "0" * 40, "branch": "main", "repository_url": "https://github.com/example/demo.git"}}
        elif t == "response_item":
            pt = p.get("type")
            if pt == "message":
                if p.get("role") == "developer":
                    continue
                typ = "output_text" if p.get("role") == "assistant" else "input_text"
                env["payload"] = {"type": "message", "role": p.get("role"), "content": [{"type": typ, "text": "scrubbed text"}]}
            elif pt == "function_call":
                args = {}
                try: args = json.loads(p.get("arguments", "{}"))
                except Exception: pass
                a = {"cmd": command(args.get("cmd", "")), "workdir": "/home/user/projects/demo"} if p.get("name") == "exec_command" else {}
                env["payload"] = {"type": pt, "name": p.get("name"), "arguments": json.dumps(a), "call_id": fid(p.get("call_id", "c"))}
            elif pt == "function_call_output":
                o2 = p.get("output")
                txt = o2 if isinstance(o2, str) else " ".join(x.get("text", "") for x in o2 if isinstance(x, dict))
                env["payload"] = {"type": pt, "call_id": fid(p.get("call_id", "c")), "output": scrub_output(txt)}
            elif pt == "custom_tool_call":
                inp = p.get("input", "")
                new = scrub_patch(inp) if p.get("name") == "apply_patch" else "text = 'scrubbed'"
                env["payload"] = {"type": pt, "name": p.get("name"), "input": new, "call_id": fid(p.get("call_id", "c"))}
            elif pt == "custom_tool_call_output":
                o2 = p.get("output")
                txt = o2 if isinstance(o2, str) else " ".join(x.get("text", "") for x in o2 if isinstance(x, dict))
                env["payload"] = {"type": pt, "call_id": fid(p.get("call_id", "c")), "output": [{"type": "input_text", "text": scrub_output(txt)}]}
            else:
                continue  # reasoning (encrypted), web_search_call, ...
        elif t == "event_msg" and p.get("type") in ("user_message", "agent_message", "task_started", "task_complete"):
            env["payload"] = {"type": p["type"], **({"message": "scrubbed text"} if "message" in p else {})}
        else:
            continue
        out.append(env)
    with open(dst, "w") as fh:
        for e in out:
            fh.write(json.dumps(e, separators=(",", ":")) + "\n")

if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
