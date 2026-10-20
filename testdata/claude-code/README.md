# Claude Code fixtures

All fixtures are synthetic or scrubbed; none contains real user data.

- `2.1/basic.jsonl`: structure of a real 2.1.227 session, produced by `scripts/scrub_claude_fixture.py` (all text, ids, paths, commands and timestamps replaced).
- `2.1/workitems.jsonl`: hand-built session in the same shape, exercising work-item extraction: created/failed/mentioned commits, PR create/merge/mention-after-merge/failed create, file create/update/failed/secret/outside-project, `gh issue` lifecycle and Linear MCP issue results (with planted `SECRET-` strings that must never reach a payload).
- `unknown/*.jsonl`: malformed or future shapes; must be skipped with warnings, never crash.
- `*.golden.json`: expected normalized output; regenerate with `go test ./... -update` and review the diff.
