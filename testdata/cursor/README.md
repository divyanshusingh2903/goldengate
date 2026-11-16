# Cursor fixtures

Cursor keeps conversations in SQLite (`globalStorage/state.vscdb`), which cannot be diffed or committed safely, so fixtures are SQL scripts that tests load into a temporary database. Everything is synthetic, written in the shape seen on disk with Cursor 3.30 on Linux.

- `3.30/schema.sql`: the real DDL of the `ItemTable`, `cursorDiskKV` and `composerHeaders` tables (structure only), plus decoy sign-in rows holding `TOPSECRET-*` values that the adapter must never read.
- `3.30/basic.sql`: synthetic rows: a conversation with user and assistant messages, thinking that must be dropped, file-read, search and shell tool calls (completed, cancelled, errored), a second prompt; a conversation started from the agent sidebar (no user message in the database); one without a folder; a never-used one, a draft and a sub-agent that must not be listed; a header of an unknown shape; a conversation in a future message format; and one with a single unreadable message. Every conversation also has a `composerData` row with decoy keys.
- `3.30/projects/`: the transcript file Cursor writes next to the database, for the sidebar conversation: the only place its prompt is recorded.
- `*.golden.json`: expected normalized output; regenerate with `go test ./... -update` and review the diff.
