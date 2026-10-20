# Golden Gate

Let a remote AI assistant know what you have been working on in your coding tools, with **you choosing which projects are shared**.

- `goldengate` (laptop): reads your local session history, applies your allow/deny choices, redacts and summarizes, and pushes to your own server.
- `goldengatebridge` (server you run): stores the summaries and serves them read-only over MCP (streamable HTTP, stateless JSON).

Status: pre-release (M4). Sources: Claude Code, Codex, OpenCode, and claude.ai / ChatGPT data exports. Deployment scripts and release packaging are on the roadmap in [DESIGN.md](DESIGN.md). See [SECURITY.md](SECURITY.md) before connecting an assistant.

## Quickstart (about ten minutes)

1. **Run the server** behind HTTPS. Generate the admin secret once and keep it:

   ```bash
   export GOLDENGATE_ADMIN_TOKEN="$(openssl rand -base64 32)"
   goldengatebridge serve --addr :8080 --behind-tls-proxy --db /data/bridge.db
   ```

2. **Connect your laptop.** Paste the admin secret when asked (it is never written to disk):

   ```bash
   goldengate init --server https://bridge.example.com
   ```

   This stores an `ingest` token in your OS keyring and prints a `read` token **once**. On a server where you have a shell you can pair instead: run `goldengatebridge pair` there and `goldengate init --server URL --pair-code CODE` here.

3. **Choose what to share, preview, sync:**

   ```bash
   goldengate share add ~/code/myproject
   goldengate sync --dry-run     # exactly what would be sent
   goldengate sync
   ```

4. **Connect the assistant.**
   - If its connector lets you set a header: use `https://bridge.example.com/mcp` and send the read token as `Authorization: Bearer <token>`.
   - If it only supports OAuth (Gemini's custom apps do; it supports dynamic client registration): start the server with `--oauth --public-url https://bridge.example.com`, then add `https://bridge.example.com/mcp` as the connector URL (Gemini: Settings > Connected Apps > Add a custom app). A consent page opens; approve it with your admin secret. No token to paste. If the assistant asks for a client ID and secret instead, run `goldengatebridge oauth clients create --redirect-uri <the address it shows>`.
   - Revoke a connected assistant with `goldengatebridge oauth clients delete <id>`.

Un-share with `goldengate share remove ~/code/myproject --purge-remote`. Rotate the assistant's token with `goldengate token rotate read`.

## The interactive UI

Run `goldengate` in a terminal. The first run starts a short setup (find your tools, connect to your server, choose the share level, pick projects, see a dry run, first sync); after that it opens the project picker.

| Screen | Keys |
|---|---|
| Projects (`1`) | `space` share/un-share · `v` range · `a`/`A` mark all/none · `d` deny · `u` undo · `e` choose sources · `/` fuzzy filter · `t` folder tree (space on a folder shares everything below it) · `o` sort · `p` preview · `P` paranoid (raw payloads) |
| Sync (`2`, or `s`) | plan, `enter` start, `D` dry run / real, `c` cancel, `r` retry or replan |
| Server (`3`) | health, token check, what the server holds, `D` delete a project from it |
| Settings (`4`) | share level, look-back window, sources, redaction patterns, commit verification |

`?` shows every key. The preview is the exact outgoing payload, redactions highlighted, with what changed since the last sync. Sync is a dry run until you press `D`. It works at 80x24, follows resizes, supports the mouse (wheel, click), and honors `NO_COLOR` (nothing relies on color alone). `GOLDENGATE_THEME=light|dark` overrides background detection. Everything the UI does is also available as a command.

A source can be switched off for one project (`e`) without un-sharing the rest; that is stored as `deny_source = ["codex@~/code/app"]` in the config.

## Sources

| Source | How it is read |
|---|---|
| Claude Code | `~/.claude/projects/**.jsonl` (honors `CLAUDE_CONFIG_DIR`) |
| Codex CLI / Desktop | `~/.codex/sessions/**/rollout-*.jsonl` (honors `CODEX_HOME`) |
| OpenCode | its SQLite database, read-only (`$XDG_DATA_HOME/opencode/opencode.db`) |
| claude.ai | `goldengate import-claude-ai export.zip` |
| ChatGPT | `goldengate import-chatgpt export.zip` |

The same folder in several tools is one project. Worktrees are filed under their repository when it is selected. Chat exports become projects like `claude-ai:Chats` and are shared only with an explicit `goldengate share add 'claude-ai:Chats'` (globs work: `chatgpt:*`). Exports stay where you keep them; `goldengate imports list|remove` manages them.

## Development

```bash
make check          # vet, race tests, staticcheck, golangci-lint
govulncheck ./...
```

The pre-commit hook (`git config core.hooksPath .githooks`) blocks secret-shaped strings, real home paths, `.env` files and raw transcripts. Test fixtures are synthetic or scrubbed; never commit real session data.

License: Apache-2.0.
