# Golden Gate — Design Spec

> Project name: **Golden Gate** (after the bridge: it connects your local AI work to any assistant). Repo: `goldengate`; binaries: `goldengate` and `goldengatebridge`.

An open-source, self-hosted tool that lets any MCP-capable assistant (Gemini, ChatGPT, Claude, etc.) know what you have been working on in your AI coding tools, with **you choosing exactly which projects are shared**.

- **Sources (read from your machine):** Claude Code, Codex CLI, OpenCode, plus an imported claude.ai data export.
- **Ingester + TUI (runs locally):** discovers sessions, lets you pick which project folders to share, redacts, summarizes, pushes.
- **Server (runs in the cloud, e.g. Cloud Run):** stores summaries, serves them over MCP (streamable HTTP) to a remote assistant.

The MCP server never reaches into your machine. Data only flows laptop -> server, and only for projects you explicitly allowed.

## 1. Goals and non-goals

Goals
1. Give a remote assistant a useful, current picture of the user's daily work across several coding tools.
2. Opt-in by default: **nothing is shared until the user selects it** in the TUI (or config).
3. Raw transcripts never leave the machine by default; only redacted, compact summaries do.
4. Single static Go binaries, easy self-hosting, permissive license (Apache-2.0).
5. Parsers are isolated and fixture-tested, because session file formats are not stable public contracts.

Non-goals
- No multi-tenant hosted service. One deployment = one user (or one team that trusts each other).
- No real-time streaming of in-progress sessions.
- No attempt to scrape claude.ai; only the official data export is supported.

## 2. Components

```
 laptop                                              cloud (Cloud Run)               assistant
┌──────────────────────────────┐   HTTPS + bearer   ┌──────────────────────┐  MCP   ┌─────────┐
│ goldengate (TUI + CLI)        │ ─────────────────► │ goldengatebridge      │ ◄───── │ Gemini  │
│  adapters: claude-code,      │   POST /v1/ingest  │  /v1/ingest (write)  │  /mcp  │ etc.    │
│  codex, opencode, claude.ai  │                    │  /mcp (read-only)    │        └─────────┘
│  select -> redact -> summarize│                   │  store: Firestore/   │
│  state: ~/.config/goldengate  │                    │  SQLite/Postgres     │
└──────────────────────────────┘                    └──────────────────────┘
```

Two binaries, one module:
- `goldengate` — local CLI + TUI (default command opens the TUI).
- `goldengatebridge` — HTTP server exposing the ingest API and the MCP endpoint.

## 3. Repository layout

```
cmd/goldengate/            # CLI + TUI entry (cobra or urfave/cli)
cmd/goldengatebridge/     # server entry
internal/
  adapters/               # one package per source tool
    adapter.go            # Adapter interface + registry
    claudecode/
    codex/
    opencode/
    claudeai/             # data-export importer
  model/                  # Session, Project, Summary types (shared client/server)
  discover/               # find projects across adapters, group by folder
  selection/              # allow/deny rules, persistence, matching
  redact/                 # secret + path scrubbing
  summarize/              # heuristic summarizer, optional LLM summarizer
  sync/                   # idempotent push, state db, retry
  tui/                    # Bubble Tea models, views, styles
  server/
    mcp/                  # MCP tools
    ingest/               # ingest handler, validation
    auth/                 # bearer (hashed tokens), later OAuth
    store/                # Store interface + firestore, sqlite, postgres, memory
  config/                 # TOML config, XDG paths, keyring
testdata/                 # fixtures per adapter, per format version
deploy/                   # Dockerfile, cloudrun.md, firebase.json example
docs/
```

## 4. Source adapters

All adapters implement:

```go
type Adapter interface {
    Name() string                                   // "claude-code", "codex", "opencode", "claude-ai"
    Detect(ctx context.Context) (Detection, error)  // is it installed / where are its files
    ListSessions(ctx context.Context, opt ListOpts) iter.Seq2[SessionRef, error] // cheap, metadata only
    Load(ctx context.Context, ref SessionRef) (*RawSession, error)               // full parse, on demand
}
```

Rules
- `ListSessions` must be cheap (stat + header read) so the TUI can show hundreds of projects instantly. Parse bodies lazily in `Load`.
- Adapters are **read-only** and never modify the source tool's files.
- Each adapter pins a `FormatVersion` it was tested against and degrades gracefully (skip + warn, never crash) on unknown shapes.
- Each adapter ships fixtures in `testdata/<adapter>/<version>/` and golden-file tests.

### Known locations (starting points; VERIFY ON DISK before coding)

These come from memory and tool formats change. The implementer must inspect real files on the dev machine and write fixtures from them (scrubbed), not trust this table.

| Source | Expected location | Notes |
|---|---|---|
| Claude Code | `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl` (honor `CLAUDE_CONFIG_DIR`) | JSONL events; the project folder name is an encoding of the working directory, so prefer the `cwd` field inside events over decoding the folder name. |
| Codex CLI | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` (honor `CODEX_HOME`) | JSONL; working directory is recorded in session metadata events. |
| OpenCode | `~/.local/share/opencode/` (honor XDG data dir) | Historically JSON files under a `storage/` tree (sessions, messages, parts); newer versions may use SQLite. Support whichever exists; detect by presence. |
| claude.ai export | user-supplied `.zip` from Settings -> Privacy -> Export data | Conversations and projects as JSON. No folder concept; treat each claude.ai Project as a "project" and ungrouped chats as one "Chats" bucket. |

### Normalized model

```go
type SessionRef struct {
    Source    string    // adapter name
    ID        string    // source-native id
    ProjectKey string   // normalized absolute folder path, or "claude-ai:<project>" for exports
    Path      string    // file/db location
    StartedAt, UpdatedAt time.Time
    SizeBytes int64
}

type RawSession struct {
    Ref        SessionRef
    Title      string
    Turns      []Turn      // role, text, tool calls (name + args summary), timestamps
    FilesTouched []string  // relative to ProjectKey
    ToolsUsed  map[string]int
    GitBranch, GitRemote string // if recoverable
}
```

### Project identity (important)
The same folder shows up across tools. Group by **normalized absolute path** (resolve symlinks, clean, case-fold on case-insensitive filesystems). If the path is inside a git repo, also record the repo root and remote (remote URL with credentials stripped) so worktrees of one repo can be merged in the UI.

### 4.1 Work items: commits, pull requests, issues, artifacts

Besides the prose summary, every session yields structured **work items**, because "what shipped" is more useful to an assistant than "what was said".

```go
type WorkItem struct {
    Kind     string     // commit | pull_request | issue | file | artifact
    Provider string     // for issues and PRs: github | linear | jira | "" 
    Key      string     // stable id: "<repo>@<sha>", "<repo>#<n>", "linear:ENG-123", "jira:PLAT-45", "<relpath>", "artifact:<id>"
    Title    string     // commit subject, PR/issue title, file or artifact name
    URL      string     // PR/issue/commit URL when known (credentials stripped)
    Status   string     // mentioned | created | updated | merged | closed | completed | canceled
    RawState string     // the tracker's own state name as seen in the session ("In Progress", "Done")
    Evidence []Evidence // {session_id, turn, via: "tool_result" | "text"}
}
```

Status rules
- `mentioned` means the item only appears in conversation text (a SHA, `#123`, a PR URL). A mention never upgrades to a higher status on its own.
- `created`, `updated`, `merged`, `closed` require **evidence from a successful tool result**: `git commit` output containing a SHA, `gh pr create` returning a URL, `gh pr merge` succeeding, a write/edit/patch call that succeeded. A failed command is not evidence. Record which evidence was used so the assistant can say how sure it is.
- A bare mention never changes an existing status, so a later session that merely mentions an already merged PR does not downgrade it. Among evidence-backed statuses the **latest by evidence timestamp wins**, because PRs and issues can legitimately be reopened or moved back. Commits are immutable once `created`.
- Statuses are "as of the session", so always keep `observed_at`. They are not live state.

What each source can provide

| Source | Commits and PRs | Files and artifacts |
|---|---|---|
| Claude Code, Codex, OpenCode | From shell tool calls and results (`git commit`, `git push`, `gh pr create\|merge\|close`), plus SHAs, URLs and `#n` in text | Files created vs modified, from write/edit/patch tool calls (each tool names these differently; the adapter normalizes them) |
| claude.ai export | Mentions in text only (status `mentioned`) | Artifacts and files created or uploaded in chats, if the export includes them (verify against a real export) |

Privacy
- By default only metadata is sent: kind, key, title, URL, status, type, size, content hash. **No file or artifact contents.** Sending contents requires `level = "full"` and `artifacts.include_content = true`, and the TUI preview must show it.
- File paths are project-relative and scrubbed. Files matching secret patterns (`.env*`, `*.pem`, `id_rsa*`, `credentials*`) are omitted entirely.
- Commit messages and PR titles go through the same redaction pipeline as everything else.

Issue trackers (Linear, Jira/Atlassian, GitHub Issues)

Issues use the same model with `Kind = "issue"`, a `Provider`, a `Key` such as `linear:ENG-123` (include the workspace or site when known), and `RawState` holding the tracker's own state name.

Status mapping
- `mentioned`: the key or URL appears in text, or the issue was only read, listed or fetched.
- `created`: a create call succeeded and returned the key.
- `updated`: an edit, comment, or transition into a non-done state succeeded.
- `completed`: the result shows a done state (Linear state type `completed`, Jira status category Done, GitHub closed as completed).
- `canceled`: Linear state type `canceled`, Jira resolutions like Won't Do, GitHub closed as not planned.

Detection
- Anything above `mentioned` needs tool-result evidence: MCP calls to the tracker (for example Linear's issue save/get tools or Atlassian's Jira tools), CLI calls (`gh issue create|close`, similar), or API-shaped results. Exact tool names and result shapes must be inspected per tool and per server version, not assumed.
- URLs: `linear.app/<workspace>/issue/<KEY-N>`, `<site>.atlassian.net/browse/<KEY-N>` (and Jira board URLs with `selectedIssue=`), `github.com/<owner>/<repo>/issues/<n>`.
- Bare keys like `ENG-123` are ambiguous (`UTF-8`, `SHA-256`, `ISO-8601`, `CVE-2024-1234`). Accept a bare key only if its prefix is known from a URL or tool result in the same project, is listed in config (`[issues]`), or came from a tool result. Otherwise ignore it, and keep a denylist of common false-positive prefixes.

Privacy
- Send only key, title, URL, provider, state and timestamps. **Never issue descriptions or comments**, even though tool results may contain them. Titles go through redaction because they can contain client names. Each provider can be switched off.

Live state
- The assistant may already have its own connector to the tracker (Gemini with Linear, for example). Golden Gate emits canonical keys and URLs so the assistant can resolve current state itself, which also means Golden Gate never holds tracker credentials. Direct refresh through the tracker's API (`issues.refresh_state`) is an optional enrichment, off by default.

Optional enrichment (all off by default, shown in the TUI)
- `enrich.git`: verify commits against the project's local git repo using read-only commands (`git cat-file`, `git log`) and mark them `verified`. Limited to selected projects.
- `enrich.github`: look up PR state through the GitHub API with the user's own token. This is a network call to a third party, so it is strictly opt-in. Without it, PR state is only what the sessions show.

Deduplication
- The same commit or PR shows up in many sessions. The server keeps work items in their own table keyed by `(project, kind, key)` with `first_seen`, `last_seen`, status, and links to the sessions that mention them.

## 5. Selection model (what is shared)

Default policy: **deny all**. A project is shared only if matched by an allow rule.

Config (`~/.config/goldengate/config.toml`):

```toml
[server]
url = "https://mcp.example.com"
# token is stored in the OS keyring, not in this file

[share]
default = "deny"                 # deny | allow
allow = ["~/code/pharmfindr", "~/code/forge"]
deny  = ["~/code/**/secrets", "~/work/client-x"]   # deny always wins over allow
include_sources = ["claude-code", "codex", "opencode"]
since = "90d"                    # ignore older sessions
level = "summary"                # summary | titles-only | full (full requires --i-understand)

[artifacts]
include_content = false          # true requires level = "full"

[enrich]
git = false                      # verify commits against the local repo (read-only)
github = false                   # look up PR state via the GitHub API (network call, opt-in)

[issues]
providers = ["linear", "jira", "github"]   # trackers to extract; empty list disables
linear_teams = ["ENG"]                      # known key prefixes, helps bare-key detection
jira_projects = ["PLAT"]
refresh_state = false                       # look up live state via each tracker's API (network call, opt-in)

[redact]
extra_patterns = []
strip_home_prefix = true
```

Rules
- Deny beats allow. Globs supported (doublestar).
- Level `titles-only` sends just project, title, timestamps. `summary` (default) sends the redacted summary. `full` sends redacted transcripts and must require an explicit flag plus a TUI confirmation.
- **Un-sharing a project** (removing it from allow, or adding deny) offers to delete that project's data from the server (`DELETE /v1/projects/{key}`) and records the tombstone locally so it is never re-pushed.
- Selection state is also manageable headlessly: `goldengate share add <path>`, `share remove <path>`, `share list`.

## 6. TUI (the "rich" part)

Stack: Go, Bubble Tea + Bubbles + Lip Gloss (and `huh` for forms). Must work in a plain 80x24 terminal, degrade without color (`NO_COLOR`), and be keyboard-first with mouse support as a bonus. Responsive to resize.

### Screens
1. **First-run wizard**: detect installed tools -> enter server URL + token (stored in keyring) -> choose share level -> pick projects -> dry-run preview -> first sync.
2. **Projects (main screen)**: the project picker. Two panes.
3. **Preview**: shows exactly what would be sent for the highlighted project (redacted summary), with redactions highlighted.
4. **Sync**: live progress, per-project results, errors, retry.
5. **Server**: connection health, token check, what the server currently holds per project, delete remote data.
6. **Settings**: share level, since-window, redaction patterns, sources on/off, schedule/hook installation.
7. **Help**: keybinding overlay (`?`).

### Main screen mockup

```
┌ goldengate ───────────────────────────────── server: ● connected ┐
│ Sources  ● Claude Code 142  ● Codex 37  ● OpenCode 58  ○ claude.ai│
├───────────────────────────────┬──────────────────────────────────┤
│ Filter: /pharm_               │ pharmfindr                       │
│                               │ ~/code/pharmfindr   main         │
│ [x] ~/code/pharmfindr    ▲ 3  │ Sources: claude-code 41, codex 6 │
│ [x] ~/code/forge         ▲ 1  │ Last activity: 2h ago            │
│ [ ] ~/code/vihayas             │ Level: summary                   │
│ [~] ~/code/website  (partial) │                                  │
│ [-] ~/work/client-x   denied  │ Recent sessions                  │
│ [ ] ~/scratch/tmp              │  • Fix OAuth redirect mismatch   │
│                               │  • Add Go gRPC health checks     │
│                               │ Redactions in preview: 2 secrets │
├───────────────────────────────┴──────────────────────────────────┤
│ space toggle  a all  d deny  p preview  s sync  / filter  ? help  │
└──────────────────────────────────────────────────────────────────┘
```

Legend: `[x]` shared, `[ ]` not shared, `[~]` some sources shared, `[-]` denied, `▲ n` = new unsynced sessions.

### Required TUI features
- Project list grouped by normalized folder across all tools, with per-source badges, session counts, last-activity time, and unsynced counts. Sort by recent / name / size.
- Fuzzy filter (`/`), multi-select (space, `v` range, `a` all visible, `A` none), `d` deny, `u` undo.
- Toggle sharing per project and optionally per source within a project.
- Tree mode (`t`) to browse by directory hierarchy and select a parent folder to cover children (writes a glob rule).
- Live preview pane of the exact payload (redacted), with a diff of what changed since last sync.
- Secret-redaction highlights, and a "paranoid" toggle that previews every string leaving the machine.
- Dry-run (`--dry-run` and a TUI toggle): shows what would be sent and removes nothing.
- Sync screen with progress bars, per-project status, and error details.
- Remote view: list/delete what the server holds; "forget project" action.
- Theme: adaptive light/dark, consistent palette, no layout jank on resize.
- Non-interactive parity: everything the TUI does has a CLI equivalent so hooks/cron can use it.

## 7. CLI surface

```
goldengate                      # open TUI
goldengate init [--server URL] [--pair-code CODE]   # first-run wizard; headless via flags/env (see 7.1)
goldengate sync [--dry-run] [--project PATH] [--since 7d] [--source codex]
goldengate share add|remove|list
goldengate import-claude-ai ./export.zip
goldengate doctor               # detects tools, checks formats, server, token
goldengate install-hook claude-code   # adds a SessionEnd hook that runs `goldengate sync --quiet`
goldengate install-schedule     # launchd / systemd timer / Task Scheduler
goldengate purge --project PATH # delete remote data
goldengate token rotate read|ingest   # mint a replacement token; needs the admin token
goldengate version
```

Server binary (`goldengatebridge`):

```
goldengatebridge serve                       # run the server (config via flags/env)
goldengatebridge pair [--ttl 10m]            # print a single-use pairing code (self-host/Docker installs)
goldengatebridge token create|list|revoke    # local admin operations against the configured store
goldengatebridge version
```

Automation
- Claude Code: `SessionEnd` hook calling `goldengate sync --quiet --source claude-code`.
- Codex / OpenCode: a scheduled timer (every 15-30 min) running `goldengate sync --quiet`; also document their own hook/notify mechanisms if present, but do not depend on them.
- Sync is **idempotent**, keyed on `(source, session_id)` with a content hash; unchanged sessions are skipped via a local state DB (`~/.local/state/goldengate/state.db`, SQLite).

### 7.1 Setup and token bootstrap

There are two trust links to establish, and three token types.

- **Link 1: `goldengate` (laptop) to `goldengatebridge` (server).** Automated after one manual deploy-time step.
- **Link 2: the assistant (e.g. Gemini) to `goldengatebridge`.** One manual paste in v1; removable with OAuth in Phase 2.

Token types: `admin` (mints/lists/revokes tokens, nothing else), `ingest` (writes and deletes work data), `read` (MCP endpoint only).

**Server deployment (user does this once)**
1. Deploy `goldengatebridge` (see section 10) behind HTTPS.
2. Set one secret, `GOLDENGATE_ADMIN_TOKEN`, at deploy time (on Cloud Run: Secret Manager). The user generates it themselves, e.g. `openssl rand -base64 32` (minimum 32 random bytes; the server rejects weaker values). It is never printed to logs, never stored in the database, and compared in constant time.

**Client setup (`goldengate init`)**
1. Ask for the server URL and the admin token (hidden input, or `--admin-token-stdin`, or env vars for headless installs). Refuse plain `http://` except for loopback.
2. Check `GET /healthz`, then call `POST /v1/admin/tokens` to mint an `ingest` token and a `read` token.
3. Store the `ingest` token in the OS keyring. Keep the admin token in memory only and never write it to disk.
4. Show the `read` token **once**, with a copy action and short instructions for pasting `https://<host>/mcp` plus the token into the assistant's connector dialog.
5. Continue the wizard: choose share level, pick projects, dry-run preview, first sync.

**Pairing alternative (Docker/VPS where the operator has a shell on the server)**
- `goldengatebridge pair` prints a single-use code (e.g. `ABCD-EFGH-JKLM`, at least 40 bits), stored hashed, expiring after 10 minutes by default, burned on first successful use or after 5 failed attempts.
- `goldengate init --pair-code CODE` exchanges it at `POST /v1/pair` for an `ingest` token. The `read` token comes from `goldengatebridge token create --scope read`.

**Assistant link**
- v1: manual paste of the URL and `read` token. **Before building, verify which auth methods Gemini's custom MCP connector actually supports** (its "Additional settings" section) and adapt; do not guess.
- Phase 2: if OAuth is supported, implement the MCP authorization flow so the user approves on a consent page served by `goldengatebridge` and nothing is pasted.

**Lifecycle rules**
- The `read` token is shown once. If lost, rotate it with `goldengate token rotate read`.
- Rotation mints a new token and revokes the old one, with an optional grace period.
- Revoked, expired, and rotated-out tokens are rejected immediately.
- Headless installs: `--non-interactive` plus env vars `GOLDENGATE_SERVER_URL` and `GOLDENGATE_ADMIN_TOKEN`.

**Goal:** from a deployed server to the first successful sync in about ten minutes, with two pastes at most (the admin secret into `init`, the `read` token into the assistant). The README quickstart must follow exactly this flow, and an end-to-end test must cover it.

## 8. Redaction and summarization

Pipeline per session: `Load -> redact -> summarize -> redact again -> push`.

Redaction (`internal/redact`)
- Pattern rules for common secrets (cloud keys, API tokens, private key blocks, JWTs, connection strings, `Authorization:` headers) plus high-entropy-string detection. Allow user `extra_patterns`.
- Strip `.env`-style content and never include file contents from files whose names match secret patterns (`.env*`, `*.pem`, `id_rsa*`, `credentials*`).
- Path scrubbing: replace the home directory with `~`, drop absolute paths outside shared projects.
- Redact twice (before and after summarization) because summaries can re-emit secrets.
- Redactions are logged locally as counts and kinds only, never the secret.
- State plainly in docs that regex redaction is best-effort, not a guarantee.

Summarization (`internal/summarize`)
- **Default: heuristic, fully local.** Title (first user prompt, trimmed), goal, files touched (top N), tools used, commands run (names only), last assistant message (trimmed), outcome guess (tests passed, commit made), duration, turn count.
- **Opt-in: LLM summarizer**, behind an interface, provider-agnostic (local command, Ollama, or an API key the user supplies). Off by default and the TUI must say clearly that transcript text will go to that model.
- Hard size caps per summary (e.g. 2 KB) so MCP responses stay small. Work items (see 4.1) are carried as separate structured records alongside the summary, with their own caps, and are shown in the preview exactly as they will be sent.

## 9. Server

### Endpoints
- `POST /v1/ingest` — batch upsert of summary records. Bearer token with `ingest` scope.
- `DELETE /v1/projects/{key}` and `DELETE /v1/sessions/{id}` — remove data. `ingest` scope.
- `GET /v1/projects` — what the server holds (for the TUI Server screen).
- `POST|GET /mcp` — MCP streamable HTTP. Bearer token with `read` scope.
- `POST /v1/admin/tokens`, `GET /v1/admin/tokens`, `DELETE /v1/admin/tokens/{id}` — mint, list, revoke tokens. `admin` scope only.
- `POST /v1/pair` — exchange a single-use pairing code for an `ingest` token. Unauthenticated, so strictly rate limited and burn-after-5-failures.
- `GET /healthz` — unauthenticated, no data.

Run in **stateless mode with plain JSON responses** (no server-held sessions, no long-lived SSE), so it works on Cloud Run scale-to-zero and behind Firebase Hosting rewrites.

### MCP tools (all annotated read-only)
| Tool | Purpose |
|---|---|
| `recent_activity(days=7, project?, source?)` | What was worked on, grouped by project, newest first |
| `list_projects(source?)` | Shared projects with last activity and session counts |
| `get_project_context(project)` | Rolling summary: goals, recent sessions, files, open threads |
| `search_work(query, project?, limit=10)` | Keyword search over titles/summaries |
| `get_session(id)` | One session's summary and its work items |
| `list_commits(project?, days=14, status?)` | Commits created or mentioned, with evidence and verification state |
| `list_pull_requests(project?, state?, days=30)` | PRs opened, merged, closed, plus "mentioned but not confirmed" |
| `list_artifacts(project?, source?, days=14)` | Files and artifacts created in coding and chat sessions (metadata only) |
| `list_issues(project?, provider?, state?, days=14)` | Linear, Jira and GitHub issues created, updated, completed or mentioned, with state as of the session |

`get_project_context` should also surface unfinished threads, such as PRs that were opened or mentioned but never confirmed merged.

Guidance
- Return structured content plus a short text rendering; cap result size; support pagination cursors.
- Tool descriptions should tell the assistant when to use each tool ("use when the user asks what they've been working on").
- Consider also exposing a `work://projects/{key}` resource and a "daily digest" prompt.

### Storage
`Store` interface with implementations: `memory` (tests), `sqlite` (self-host, single volume), `postgres`, `firestore` (Cloud Run friendly). Keep queries simple (by project, by time range, text search) so the interface is easy to implement. SQLite uses FTS5 for `search_work`; Firestore can use simple token matching. Work items live in their own table/collection keyed by `(project, kind, key)`, so one commit or PR seen in several sessions is a single record; index on status and `last_seen`.

### Auth
- Three scopes: `admin` (token management only; its secret comes from `GOLDENGATE_ADMIN_TOKEN` and is never stored), `ingest` (write/delete work data), `read` (MCP only). No scope can do another's job, and `admin` cannot read or write work data. The token given to the assistant can neither write nor delete.
- Minted tokens are high-entropy (at least 256 bits) and carry a recognizable prefix (e.g. `ggb_read_`, `ggb_ingest_`) so secret scanners can detect leaks. They are stored **hashed** (SHA-256 is sufficient for high-entropy random tokens), compared in constant time, shown once at creation, revocable, with optional expiry (`--expires`) and a recorded `last_used_at`.
- Rotation: `token rotate` mints a replacement and revokes the old token, with an optional grace period.
- Audit log: append-only record of token use (token id, scope, action, project key, time, source IP), never payload content, with configurable retention.
- Rate limiting per token and per IP, backoff on repeated bad tokens, request size limits, and no secrets in logs.
- TLS required: refuse to bind to a non-loopback address over plain HTTP unless an explicit flag (e.g. `--behind-tls-proxy`) says a proxy terminates TLS.
- Phase 2: OAuth 2.1 / OIDC (MCP authorization spec) so clients that expect OAuth can connect. Check what the target client (Gemini's custom connector dialog) actually supports in "Additional settings" before choosing, and do not guess.
- Refuse to start without `GOLDENGATE_ADMIN_TOKEN` or at least one stored token. No anonymous mode.

### Treat transcript-derived text as untrusted
Summaries are produced from content that may include prompt-injection text (from web pages, READMEs, tool output). The server should return summaries clearly delimited as data, strip control characters and markup that looks like instructions where practical, and keep the tool descriptions free of anything the stored data could override. Document this risk in `SECURITY.md`.

## 10. Deployment (reference: Cloud Run + custom domain)

- `deploy/Dockerfile`: multi-stage, distroless, non-root, `CGO_ENABLED=0`.
- `deploy/cloudrun.md`: `gcloud run deploy`, secrets via Secret Manager, Firestore setup, scale-to-zero.
- Custom domain options: Cloud Run domain mapping (e.g. `mcp.<your-domain>`) or a Firebase Hosting rewrite for `/mcp` and `/v1/**`. Note Hosting-to-Cloud-Run request timeouts; stateless JSON mode avoids the issue. Verify current regional support for domain mappings in the docs.
- Ship `deploy/cloudrun.sh` (or a Terraform module) that creates the admin secret, deploys the service, and prints the URL, plus a documented "one command" path for users who do not want to hand-run `gcloud`.
- Also document a plain `docker compose` self-host with SQLite and Caddy for automatic HTTPS, and note other hosts (Fly.io, a VPS) as long as they provide TLS.
- The quickstart in the README is the deploy step followed by the flow in 7.1.

## 11. Privacy and security posture

- Opt-in selection, deny-wins, shown-before-sent previews, remote delete, tombstones.
- Local config and state files are mode 0600; tokens live in the OS keyring (fallback to an encrypted file with a clear warning).
- Every outbound payload goes through one function (`sync.Push`) so there is a single place to audit.
- `SECURITY.md` with threat model: leaked server token, malicious transcript content, redaction misses, shared deployments.
- No telemetry. None.

### Hardening checklist (required before the first public release)

Disclosure and process
- `SECURITY.md` with a reporting channel (GitHub private vulnerability reporting or a security email), supported versions, and response expectations.

Supply chain
- Pin GitHub Actions by commit SHA; Dependabot or Renovate enabled; minimal, deliberate dependencies.
- CI runs `govulncheck` and a secret scanner (gitleaks) on every push, and the repo has a pre-commit secret check.
- Releases ship checksums, cosign signatures, an SBOM, and build provenance attestations.

Server hardening
- Strict ingest validation: reject unknown fields, enforce per-field, per-request, per-project, and total size quotas, and reject control characters, so a leaked `ingest` token cannot stuff the store with junk or injected text.
- Container: distroless, non-root, read-only filesystem, no shell.
- No CORS and no browser-facing surface by default.
- Encryption in transit is mandatory (see Auth). At rest: rely on the store's encryption (Firestore does this by default); document disk encryption for SQLite and support optional app-level encryption of stored summaries with a key from the environment.

Client hardening
- Read only from known session directories; handle symlinks carefully; skip files above a size cap.
- The claude.ai export importer must defend against zip-slip and zip bombs (path checks, size and entry-count limits).
- Config and state files 0600; tokens in the OS keyring.

Residual risks to document honestly
- A compromised server leaks stored summaries, so recommend `titles-only` for sensitive projects.
- The `read` token is stored by the assistant vendor's connector settings.
- Regex redaction will occasionally miss secrets; previews and opt-in sharing are the main defense.
- Prompt injection via transcript content is reduced, not eliminated.

## 12. Testing

- Golden tests per adapter against scrubbed real fixtures, one set per observed format version, plus "unknown shape" fuzz tests that must not panic.
- Property tests for selection matching (deny beats allow, globs, symlinks, case folding).
- Work-item extraction golden tests per adapter, including negative cases: a commit that is only mentioned, a failed `git commit`, a PR URL pasted from elsewhere, and a later mention of an already merged PR (must not downgrade). Issue cases too: bare strings like `UTF-8`, `SHA-256` and `CVE-2024-1234` must not become issues, an issue that is reopened must move back from `completed`, and tool results containing issue descriptions or comments must not leak them into the payload.
- Redaction corpus tests with known secrets (must catch) and benign text (must not mangle).
- Store contract tests run against every `Store` implementation.
- Server tests: auth scopes, size limits, MCP tool schemas, stateless behavior.
- TUI: model/update unit tests with Bubble Tea's test helpers, plus golden snapshots of key views at 80x24 and 120x40.
- CI (GitHub Actions): `go vet`, `staticcheck`, `golangci-lint`, `govulncheck`, tests with `-race`.

## 13. Milestones

1. **M1 core:** module, `model`, `Adapter` interface, claude-code adapter, heuristic summarizer, redactor, selection engine, local state DB. `goldengate sync --dry-run` prints payloads.
2. **M2 server:** Store (memory + sqlite), ingest endpoint, bearer auth, MCP tools, stateless streamable HTTP, admin-token bootstrap, token mint/rotate/revoke, `pair`, audit log. End-to-end local test of the 7.1 setup flow.
3. **M3 more sources:** codex and opencode adapters, project grouping across tools, claude.ai export importer.
4. **M4 TUI:** wizard, project picker, preview, sync, server screens, settings, help.
5. **M5 deploy:** Firestore/Postgres stores, Dockerfile, Cloud Run deploy script + domain docs, docker-compose with Caddy, hook/schedule installers.
6. **M6 release:** goreleaser (linux/macOS/windows, amd64/arm64) with checksums, cosign signatures, SBOM and provenance, Homebrew tap, `go install`, README with screenshots/GIF and the 7.1 quickstart, SECURITY.md (with the hardening checklist complete), CONTRIBUTING.md, Apache-2.0 LICENSE, adapter-authoring guide.

## 14. Acceptance criteria

- Fresh install -> wizard -> pick two projects -> sync -> assistant answers "what did I work on this week?" using only those two projects.
- A project not selected, or matched by a deny rule, never appears in any payload, log, or server record (test this).
- Removing a project from the selection and confirming deletes its remote data.
- Re-running sync twice sends nothing the second time.
- A planted fake secret in a transcript does not appear in the preview or the pushed summary.
- Server refuses requests without a valid token of the right scope.
- Scope separation is tested: `admin` cannot read or write work data, `read` cannot ingest or delete, `ingest` cannot use the MCP endpoint.
- Setup from scratch works: deploy with an admin secret -> `goldengate init` -> `ingest` token in the keyring, admin token never written to disk, `read` token shown once -> first sync succeeds.
- Pairing codes are single-use, expire, and are burned after repeated failures; revoked, expired, and rotated-out tokens are rejected immediately.
- The server refuses to start without an admin secret or token, and refuses plain HTTP on a non-loopback address without the explicit proxy flag.
- A commit made in a session shows as `created`, one only mentioned in text shows as `mentioned`, and a mention never upgrades the status.
- A commit, PR or issue seen across several sessions appears once, and a bare mention never changes its status.
- An issue created through a tracker tool shows as `created`; one only mentioned shows as `mentioned`; bare strings like `UTF-8` and `SHA-256` are never treated as issues; issue descriptions and comments never reach a payload.
- At `summary` level no file or artifact contents reach any payload (test this).
- All three tool adapters pass their golden tests, and an unknown-format file is skipped with a warning, not a crash.
- Runs on Linux, macOS, Windows; TUI usable at 80x24.

## 15. Open questions for the implementer to resolve by inspection

- Exact on-disk formats for the installed versions of Claude Code, Codex, OpenCode (JSONL vs SQLite, field names, where cwd lives).
- Exact structure of the claude.ai export zip, including whether it contains artifacts or file metadata.
- How each tool records shell and tool results, which is what decides whether a commit or PR counts as `created` or only `mentioned`.
- How Linear and Atlassian MCP tools (and `gh issue`) appear in each tool's session logs, including exact tool names and result shapes.
- Which auth schemes Gemini's custom MCP connector supports.
- Whether the Go MCP SDK in use supports stateless JSON-response mode for streamable HTTP; choose `modelcontextprotocol/go-sdk` unless a blocking gap is found, and say why if switching.
