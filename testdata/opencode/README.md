# OpenCode fixtures

OpenCode keeps sessions in SQLite, which cannot be diffed or committed safely, so fixtures are SQL scripts that tests load into a temporary database.

- `1.18/schema.sql`: the real DDL of the `project`, `session`, `message` and `part` tables (structure only), plus decoy `credential` and `account` tables holding `TOPSECRET-*` values that the adapter must never read.
- `1.18/basic.sql`: synthetic rows in the real shape: user/assistant messages, synthetic and reasoning parts that must be dropped, bash results with and without an exit code, errored tools, write/edit/apply_patch, GitHub MCP issue tools (success and error), a child (sub-agent) session that must not be listed on its own, a worktree session, a placeholder title and an untested version.
- `*.golden.json`: expected normalized output; regenerate with `go test ./... -update` and review the diff.
