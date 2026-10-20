# Codex fixtures

All fixtures are synthetic or scrubbed; none contains real user data.

- `0.15x/basic.jsonl`: structure of a real Codex rollout (0.15x), produced by `scripts/scrub_codex_fixture.py` (all text, ids, paths, commands, patch bodies and timestamps replaced).
- `0.15x/workitems.jsonl`: hand-built rollout in the same shape: successful/failed/still-running commits, the legacy `shell` function form, `gh pr create|merge`, `apply_patch` (success, failure, secret file), a code-mode `exec` call that must not count as evidence, injected user messages and developer instructions that must be dropped, and a repository URL with embedded credentials.
- `unknown_*.jsonl`: malformed or future shapes; skipped with warnings, never a crash.
- `*.golden.json`: expected normalized output; regenerate with `go test ./... -update` and review the diff.
