# Golden Gate — Design Spec

> Project name: **Golden Gate** (after the bridge: it connects your local AI work to any assistant). Repo: `goldengate`; binaries: `goldengate` and `goldengatebridge`.

An open-source, self-hosted tool that lets any MCP-capable assistant (Gemini, ChatGPT, Claude, etc.) know what you have been working on in your AI coding tools, with **you choosing exactly which projects are shared**.

- **Sources (read from your machine):** Claude Code, Codex CLI, OpenCode, Cursor, plus imported claude.ai and ChatGPT data exports.
- **Ingester + TUI (runs locally):** discovers sessions, lets you pick which project folders to share, redacts, summarizes, pushes.
- **Server (runs in the cloud, e.g. Cloud Run):** stores summaries, serves them over MCP (streamable HTTP) to a remote assistant.

The MCP server never reaches into your machine. Data only flows laptop -> server, and only for projects you explicitly allowed.

## 1. Goals, principles and non-goals

Users
- Developers, and other professionals, who use both AI coding tools (Claude Code, Codex, Cursor, OpenCode) and AI chat apps (claude.ai, ChatGPT), and want the assistants they talk to to know what they have been doing in the others.
- The set of sources is deliberately small: a few data-producing tools, each supported well, rather than many supported loosely.

Goals
1. Keep every assistant the user connects up to date on the user's work across their coding tools and chat apps.
2. Opt-in by default: **nothing is shared until the user selects it** in the TUI, the web UI or config.
3. Raw transcripts never leave the machine by default; only redacted, compact summaries do.
4. Single static Go binaries, easy self-hosting, permissive license (Apache-2.0).
5. Parsers are isolated and fixture-tested, because session file formats are not stable public contracts.

### 1.1 Product principles

These decide every product question. When a feature conflicts with one of them, the principle wins and the feature changes.

1. **Only humans decide what is shared.** Assistants (agents) are readers. They never write, update or add to what Golden Gate holds: there are no write tools on the MCP endpoint, and nothing an agent says changes what is shared.
2. **Automation means the user's own rules, never a model's judgement.** Rules match deterministic facts (folder, repo remote, source, chat project, work-item key, branch) and the user writes or approves each one. A model may *suggest* (a project for a session, a rule to create), but a suggestion only ever lands in the inbox for a human to accept. Model output never feeds a rule and never publishes anything by itself.
3. **Deny by default; nothing leaves that the user would not approve.** New sessions wait in the inbox until a person or an approved rule lets them out. A broad rule that starts matching a new kind of thing asks first. Deny always beats allow and beats rules. Every automatic send leaves a receipt, and anything sent can be retracted.
4. **What an agent receives is a summary of history plus the work it touched.** Each session becomes deterministic *facts* (work items with evidence: Linear and Jira issues, GitHub and GitLab issues and pull requests, commits; files, commands, outcomes, time) and a *narrative*. The narrative is written by the local rules (short, the default), by a model the user opted into (passages with real detail), or by the user, whose edit always wins. Work items tie sessions from different tools into one piece of work.
5. **Agents see recent work by default and more only when they ask.** The default window is the last 5 days; every time-bounded MCP tool accepts a custom range. This is separate from how far back the client uploads.
6. **Each agent sees only what it was given.** Agents are first-class: each connected assistant (a read token or an OAuth client) is a named agent with an explicit list of projects. A new project is visible to no agent until the user grants it.
7. **Projects are what the user names, not folders.** A project is a space such as "Golden Gate" or "Job search". Folders, repos, chat conversations and work-item keys are its members. Suggestions about membership follow principle 2.
8. **Every screen says in words what leaves the machine, and when.** Previews show the exact payload. This holds in the TUI and the web UI alike.

### 1.2 Glossary

| Word | Meaning |
|---|---|
| **Source** | A tool that produces activity: Claude Code, Codex, Cursor, OpenCode, claude.ai, ChatGPT. |
| **Session** | One coding session or one chat conversation. Its raw content stays on the machine. |
| **Work item** | A commit, pull or merge request, or issue (GitHub, GitLab, Linear, Jira) a session created, changed or mentioned (section 4.1). |
| **Thread** | The sessions, across tools, that share a work item: one piece of work. |
| **Project** | A space the user names, made of members (folders, repos, chat groupings, work-item prefixes). |
| **Agent** | A connected assistant that reads over MCP, with its own grants. |
| **Inbox** | New and changed sessions waiting for a human (or an approved rule) to decide. |
| **Rule** | A user-approved instruction that approves matching sessions into a project automatically. |
| **Receipt** | The record of what an automatic send sent, to which projects and agents, and which rule sent it. |
| **Facts / narrative** | The two parts of a session summary: deterministic facts, and prose written by the rules, a model or the user. |

Non-goals
- No multi-tenant hosted service. One deployment = one user (or one team that trusts each other).
- No real-time streaming of in-progress sessions.
- No agent write-back. Agents cannot add to, edit or delete shared memory, even on the user's request through the agent; the user does that in Golden Gate.
- No scraping of claude.ai or ChatGPT, and no fetching of share links. Chats arrive only through actions the user takes with files they hand over: the official data exports. Golden Gate never crawls, never uses the user's browser session or cookies, and never requests a page from either service. Importing a conversation from a share URL was examined and rejected (see "Share-link import: spike result" in section 4).

## 2. Components

```
 laptop                                              cloud (Cloud Run)               assistant
┌──────────────────────────────┐   HTTPS + bearer   ┌──────────────────────┐  MCP   ┌─────────┐
│ goldengate (TUI + CLI)       │ ─────────────────► │ goldengatebridge     │ ◄───── │ Gemini  │
│  adapters: claude-code,      │   POST /v1/ingest  │  /v1/ingest (write)  │  /mcp  │ etc.    │
│  codex, opencode, claude.ai  │                    │  /mcp (read-only)    │        └─────────┘
│ select -> redact -> summarize│                    │  store: Firestore/   │
│  state: ~/.config/goldengate │                    │  SQLite/Postgres     │
└──────────────────────────────┘                    └──────────────────────┘
```

Two binaries, one module:
- `goldengate` — local CLI + TUI (default command opens the TUI).
- `goldengatebridge` — HTTP server exposing the ingest API and the MCP endpoint.

## 3. Repository layout

```
cmd/goldengate/           # CLI + TUI entry (thin main)
cmd/goldengatebridge/     # server entry (thin main)
internal/
  goldengate/             # code that ships only in the local tool
    adapters/             # one package per source tool (+ Adapter interface, registry)
      claudecode/ codex/ opencode/ cursor/ claudeai/ chatgpt/
    cli/ tui/             # cobra commands; Bubble Tea models, views, styles
    webui/                # (planned, see 6.1) local web UI: loopback server, request checks, embedded assets
    client/ config/       # bridge HTTP client; TOML config, XDG paths
    discover/ project/    # find projects across adapters, group by folder
    selection/            # allow/deny rules, persistence, matching
    secrets/              # keyring / token storage
    summarize/ sync/      # summarizers; idempotent push, state db, retry
  goldengatebridge/       # code that ships only in the server
    bridgecli/            # server CLI (serve, token, pair)
    server/               # ingest + MCP handlers
      auth/ oauth/ bridgemcp/ store/
  model/                  # wire types shared by both tools
  utils/                  # shared helpers: pathutil, redact, shellcmd, workitems
  tests/e2e/              # cross-tool end-to-end tests (unit tests sit beside their packages)
testdata/                 # fixtures per adapter, per format version
scripts/                  # secret checks, fixture scrubbers
deploy/                   # (M5) Dockerfile, cloudrun.md, firebase.json example
docs/                     # (M6)
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
| Cursor | `<user dir>/globalStorage/state.vscdb` (`~/.config/Cursor/User` on Linux), plus `~/.cursor/projects/<slug>/agent-transcripts/<id>/<id>.jsonl` | Verified on disk with Cursor 3.30 on Linux; see "Cursor" below. The macOS and Windows locations follow the platform convention and were not verified. |
| claude.ai export | user-supplied `.zip` from Settings -> Privacy -> Export data | Conversations and projects as JSON. No folder concept; treat each claude.ai Project as a "project" and ungrouped chats as one "Chats" bucket. |

### Cursor (spike result, 3.30 on Linux)

What was found on disk, and what the adapter (`adapters/cursor`, format version 3) does with it:

- **Where.** Conversations ("composers") are in the global state database `<user dir>/globalStorage/state.vscdb`, not in the per-workspace databases. Table `composerHeaders` has one row per conversation: id, workspace id, `createdAt`, `lastUpdatedAt`, `checkpointAt`, `isSubagent`, and a JSON `value` with the name, `isDraft`, and the workspace folder as `workspaceIdentifier.uri.fsPath`. That folder is the working directory; a conversation opened without a folder has none and is filed under the grouping `cursor:no-folder`.
- **Messages.** Rows of the key-value table `cursorDiskKV` under `bubbleId:<conversation>:<bubble>`, in insertion order. `type` 1 is the user, 2 the assistant. An assistant bubble holds text, thinking (dropped), or one tool call in `toolFormerData`: `name`, `status` (`completed`, `error`, `cancelled`), and `params` and `result` as JSON strings.
- **Prompts from the sidebar.** A conversation started from the agent sidebar had no user bubble at all. Its prompt was only in the transcript Cursor writes to `~/.cursor/projects/<slug>/agent-transcripts/<id>/<id>.jsonl` (`role`, `message.content[]` of `text` and `tool_use`, and `turn_ended` records; no timestamps, no tool results, and the folder only as a lossy slug). The adapter reads prompts from there when the database has none, strips the `<user_query>` wrapper, and puts them first, since their position is not recorded.
- **Tool calls and evidence.** The shell tool (`run_terminal_command_v2`) stores the command and its output, and a status, but **no exit code**. Whether a command that exits non-zero is stored differently from one that succeeds could not be established from the available conversations, so the adapter never marks a Cursor shell result as known: by the evidence rule in 4.1, commits and pull requests from Cursor are `mentioned` only. `read_file_v2` is mapped as a file read. No file-editing call was available, so edits are not mapped and `FilesTouched` stays empty. The git branch is not recorded.
- **Not listed.** Drafts, sub-agent conversations, and conversations without any message. A header of an unknown shape is skipped with a warning; a conversation whose bubbles are not version 3 is skipped rather than guessed at; single unreadable bubbles are dropped with a warning on the session.
- **What is not read.** The same database holds sign-in state (`ItemTable`) and per-conversation encryption keys (`composerData:*`). The adapter opens it read-only and reads `composerHeaders`, the `bubbleId:*` rows and one version entry, nothing else; fixtures plant decoy secrets there and tests check they never surface.
- **Open, to verify with real data:** how a failing shell command is recorded (to allow evidence), the file-editing tools, the `cursor-agent` CLI's own store, and the macOS and Windows locations.

### Share-link import: spike result (issue #39, 2026-11-15)

Question: can one conversation be imported reliably from a share URL the user created (`chatgpt.com/share/<id>`, `claude.ai/share/<id>`)? **Recommendation: do not build it.**

- **Terms of service decide it.** Both services forbid what the feature would do, a program fetching a page and extracting the conversation:
  - OpenAI Terms of Use (effective 2026-01-01), "What you cannot do": extracting data or Output automatically or programmatically is not allowed.
  - Anthropic Consumer Terms (effective 2025-10-08), section 3: no crawling or scraping, and no access through automated means such as a bot or script, except with an API key or where Anthropic explicitly permits it.
  Neither text makes an exception for a page the user created themselves, and neither mentions share links. This is a plain reading by the project, not legal advice.
- **robots.txt** (fetched 2026-11-15) is not the obstacle and does not change the answer: chatgpt.com allows `/share/` for general crawlers while disallowing nearly everything else; claude.ai disallows `/api/*` for everyone and `/share/` for ad bots only. A robots file speaks to crawlers; the terms bind the user whose account made the link.
- **Not established:** whether a share page is delivered as server-rendered HTML or JSON without a session, and which fields it carries (title, turns, timestamps, artifacts, project). Answering that means fetching share pages with a program, which is the thing the terms forbid, so it was not done. Both sites also sit behind bot protection, which Golden Gate will not work around.
- **Reliability would be poor anyway:** an undocumented page format, changed without notice, behind bot protection, is the opposite of the versioned, fixture-tested formats the other sources have.
- **What users have instead:** the official data exports (`import-claude-ai`, `import-chatgpt`), which carry every conversation with timestamps, and per-conversation selection inside an imported export (issue #41). Issue #40 (share-link import) should be closed as not planned. A browser extension (issue #43) is a different design, in which the user's own browser hands over a page they are looking at, and needs its own look at the same terms.

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
    Provider string     // for issues and PRs: github | gitlab | linear | jira | "" 
    Key      string     // stable id: "<repo>@<sha>", "<repo>#<n>", "<group>/<project>!<n>" (GitLab merge request), "linear:ENG-123", "jira:PLAT-45", "<relpath>", "artifact:<id>"
    Title    string     // commit subject, PR/issue title, file or artifact name
    URL      string     // PR/issue/commit URL when known (credentials stripped)
    Status   string     // mentioned | created | updated | merged | closed | completed | canceled
    RawState string     // the tracker's own state name as seen in the session ("In Progress", "Done")
    Evidence []Evidence // {session_id, turn, via: "tool_result" | "harness_event" | "text" | "branch"}
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
| Claude Code, Codex, OpenCode | From shell tool calls and results (`git commit`, `git push`, `gh pr create\|merge\|close`, `glab mr create\|merge\|close`), plus SHAs, URLs and `#n` in text | Files created vs modified, from write/edit/patch tool calls (each tool names these differently; the adapter normalizes them) |
| Cursor | Mentions only (status `mentioned`): a shell call records no exit code, so its result is never taken as proof | None yet: no file-editing call was available to verify |
| claude.ai export | Mentions in text only (status `mentioned`) | Artifacts and files created or uploaded in chats, if the export includes them (verify against a real export) |

Privacy
- By default only metadata is sent: kind, key, title, URL, status, type, size, content hash. **No file or artifact contents.** Sending contents requires `level = "full"` and `artifacts.include_content = true`, and the TUI preview must show it.
- File paths are project-relative and scrubbed. Files matching secret patterns (`.env*`, `*.pem`, `id_rsa*`, `credentials*`) are omitted entirely.
- Commit messages and PR titles go through the same redaction pipeline as everything else.

Issue trackers (Linear, Jira/Atlassian, GitHub Issues, GitLab)

Issues use the same model with `Kind = "issue"`, a `Provider`, a `Key` such as `linear:ENG-123` (include the workspace or site when known), and `RawState` holding the tracker's own state name.

Status mapping
- `mentioned`: the key or URL appears in text, the session's git branch is named after the issue, or the issue was only read, listed or fetched.
- `created`: a create call succeeded and returned the key.
- `updated`: an edit, comment, or transition into a non-done state succeeded.
- `completed`: the result shows a done state (Linear state type `completed`, Jira status category Done, GitHub closed as completed).
- `canceled`: Linear state type `canceled`, Jira resolutions like Won't Do, GitHub closed as not planned.

Detection
- Anything above `mentioned` needs tool-result evidence: MCP calls to the tracker (for example Linear's issue save/get tools or Atlassian's Jira tools), CLI calls (`gh issue create|close`, similar), or API-shaped results. Exact tool names and result shapes must be inspected per tool and per server version, not assumed.
- URLs: `linear.app/<workspace>/issue/<KEY-N>`, `<site>.atlassian.net/browse/<KEY-N>` (and Jira board URLs with `selectedIssue=`), `github.com/<owner>/<repo>/issues/<n>`.
- GitLab: `gitlab.com/<group>[/<subgroup>]/<project>/-/issues/<n>` and `/-/merge_requests/<n>`, and the same on self-hosted hosts listed in `issues.gitlab_hosts` (a host that is not listed is never read). A merge request is a `pull_request` with provider `gitlab` and the key `<group>/<project>!<n>`; an issue is `gitlab:<group>/<project>#<n>`; a self-hosted project's key starts with its host. `glab mr create|merge|close|reopen|update|note` and `glab issue create|close|reopen|update|note` are evidence under the same rules as `gh`: the command must have succeeded, a create counts only when its result holds the new URL, and viewing or listing changes nothing. "MR !12" in text is a mention. Merge requests are always extracted, as GitHub pull requests are; GitLab issues follow the `gitlab` entry in `issues.providers`.
- Bare keys like `ENG-123` are ambiguous (`UTF-8`, `SHA-256`, `ISO-8601`, `CVE-2024-1234`). Accept a bare key only if its prefix is known from a URL or tool result in the same project, is listed in config (`[issues]`), or came from a tool result. Otherwise ignore it, and keep a denylist of common false-positive prefixes.
- Branch names: a session on `fix/LIN-412-auth`, `lin-412-auth` or `user/eng-12_title` belongs with that issue even when nobody types the key. The key must be whole words of the branch name (either case) and its prefix must pass the same rule as a bare key, so `feature/utf-8-export` yields nothing. The evidence is recorded as `via: "branch"` and the status is `mentioned`: a branch proves which issue the session is about, not what happened to it. Plain numbers in branch names (`feat/21-threads`) are not read as GitHub issues: they are too ambiguous.

Privacy
- Send only key, title, URL, provider, state and timestamps. **Never issue descriptions or comments**, even though tool results may contain them. Titles go through redaction because they can contain client names. Each provider can be switched off.

Live state
- The assistant may already have its own connector to the tracker (Gemini with Linear, for example). Golden Gate emits canonical keys and URLs so the assistant can resolve current state itself, which also means Golden Gate never holds tracker credentials. Direct refresh through the tracker's API (`issues.refresh_state`) is an optional enrichment, off by default.

Optional enrichment (all off by default, shown in the TUI)
- `enrich.git`: verify commits against the project's local git repo using read-only commands (`git cat-file`, `git log`) and mark them `verified`. Limited to selected projects.
- `enrich.github`: look up PR state through the GitHub API with the user's own token. This is a network call to a third party, so it is strictly opt-in. Without it, PR state is only what the sessions show.

Deduplication
- The same commit or PR shows up in many sessions. The server keeps work items in their own table keyed by `(project, kind, key)` with `first_seen`, `last_seen`, status, and links to the sessions that mention them.

Threads (`internal/utils/threads`)

A **thread** is one piece of work: its issues and pull requests, and every session, from any source, that has one of them. It is computed from payloads alone (work items and their evidence), so the laptop and the server get the same answer, and it only ever combines items with the merge rules above: grouping never changes a status.

- Two sessions are in one thread when they carry the same issue or the same pull request, whatever the status. A chat that mentions LIN-412 and the coding session on its branch belong together.
- An issue and a pull request become one thread only when something ties them: a session that ran on the issue's branch (`via: "branch"`) and has evidence about the pull request (it opened, changed or merged it, or the tool recorded the pull request for the session), or a pull request title that names the issue key. A session that merely mentions two issues appears in both threads and joins neither, so a triage chat cannot glue unrelated work together.
- Tracker keys (`linear:LIN-412`) and full GitHub keys (`owner/repo#7`) mean the same thing in every project. A GitHub key without an owner (`app#7`, from a session that never saw the URL) is matched to the full key only when exactly one full key of that kind and number exists in the same project; otherwise it stays inside its project.
- A thread is named after its primary item: a tracker issue before a GitHub issue before a pull request, the earliest seen first. Each session in it lists what it touched, with the status as of that session and whether it was on the issue's branch.

`get_project_context` and `get_work_item` are built on this (see "MCP tools").

## 5. Selection model (what is shared)

> Named projects (below) landed in #18 and reach the server by id since #19. Still to come under milestone M7: a per-session inbox (#20) and user-approved rules. Deny-by-default and deny-wins are unchanged.

Default policy: **deny all**. Something is shared only if it is a member of a named project.

**Named projects.** A project has a stable id, a name the user picks, and members: folders, globs or chat groupings (`claude-ai:Chats`). One folder belongs to at most one project. Membership is what shares it, so adding a member shares it and removing one stops sharing it. A deny rule still wins over membership. When members nest, the most specific one decides which project a session belongs to. A member can leave out some tools with `exclude_sources`. Older configs with `[share] allow` and `deny_source` load unchanged: each allow entry becomes a project named after its folder. The next save writes projects only.

Config (`~/.config/goldengate/config.toml`):

```toml
[server]
url = "https://mcp.example.com"
# token is stored in the OS keyring, not in this file

[[projects]]
id = "p_3f9a1c"                  # stable; renaming keeps it
name = "Pharmfindr"
[[projects.members]]
path = "~/code/pharmfindr"
[[projects.members]]
path = "~/code/pharmfindr-web"
exclude_sources = ["codex"]      # this tool's sessions of this folder stay home
[[projects.members]]
path = "claude-ai:Pharmfindr"

[share]
default = "deny"                 # deny | allow
deny  = ["~/code/**/secrets", "~/work/client-x"]   # deny always wins over project membership
include_sources = ["claude-code", "codex", "opencode", "cursor"]
since = "90d"                    # ignore older sessions (upload look-back; agents still default to the last 5 days, see section 9)
level = "summary"                # summary | titles-only | full (full requires --i-understand)

[artifacts]
include_content = false          # true requires level = "full"

[enrich]
git = false                      # verify commits against the local repo (read-only)
github = false                   # look up PR state via the GitHub API (network call, opt-in)

[issues]
providers = ["linear", "jira", "github", "gitlab"]   # trackers to extract; empty list disables
gitlab_hosts = []                           # self-hosted GitLab hosts, e.g. ["gitlab.example.com"]
linear_teams = ["ENG"]                      # known key prefixes, helps bare-key detection
jira_projects = ["PLAT"]
refresh_state = false                       # look up live state via each tracker's API (network call, opt-in)

[redact]
extra_patterns = []
strip_home_prefix = true

[ui]
theme = "goldengate-light"       # a color preset; empty = goldengate-light
notify_receipts = false          # desktop notification after automatic sends (counts only)

[summarize]
provider = ""                    # "" / heuristic (local, default) | anthropic | openai | gemini | opencode | custom
model = ""                       # empty = the provider's default (see llm.Providers)
base_url = ""                    # endpoint override; custom defaults to http://localhost:11434/v1 (Ollama)
max_input_chars = 0              # conversation sent per session; 0 = 24000 (about 6k tokens)
length = "standard"              # narrative a model writes per session: short | standard | detailed
```

**Inbox (per-session approval).** Sharing a project selects it; it does not send its sessions. Each named project has a trust level (`trust`: ask, suggest or auto; see "Rules and trust levels" below). Unless trust and rules send it, a new or changed session waits as *pending* and is sent only once the user approved exactly that content (the decision stores the session's content hash). A session that changes after it was approved or sent is pending again. An *excluded* session is never read or sent again, in any project, until the user resets the decision. Decisions live in the local state DB (`decisions`: session, state, content hash, time, and the redacted title for listing); the states are approved, excluded and (from #29) retracted, while sent is the existing record of what was pushed. `RunOpts.Approve` plans pending sessions too: the TUI sets it because its Sync screen shows every session as it would be sent and sends only on the user's command, so sending is the approval (`PlanInfo.Approving` says how many); the CLI sets it only with `sync --approve`. Unattended sync (hooks, timers) never sets it. Configurations written before the inbox (`version` absent) load with trust `auto` for every existing project, once, so nothing changes for them; projects shared afterwards wait. The older `auto_approve = true` is read as `trust = "auto"` and not written again. A folder shared without any named project (a default-allow configuration) sends everything, as it always did.

**Rules and trust levels.** Automation is rules the user wrote (principle 2). A rule belongs to a project and acts only on sessions that are already in it: whether a folder or chat is shared at all is decided before a session is read, by the selection rules above, where deny always wins. A rule cannot pull in anything from outside the project's members, and folders outside projects are still never read.

```toml
[[projects]]
name = "App"
trust = "auto"                    # ask (default) | suggest | auto
[[projects.rules]]
id = "r_1a2b3c4d"
action = "approve"                # approve | exclude
branch = "release/*"              # every condition given must hold
[[projects.rules]]
id = "r_5e6f7a8b"
action = "exclude"
source = "codex"
```

- **Conditions are deterministic facts only** (`rules.Facts`): `source` (the tool), `folder` (a folder and what is under it, a glob, or a chat grouping such as `chatgpt:Work` or `chatgpt:*`), `remote` (the repository remote as host/path, `*` matches any run of characters), `branch`, and `work_item` (a key prefix: `linear:ENG` or `ENG` matches `linear:ENG-12`, `acme/app` matches `acme/app#7`). The facts come from the tool's own record and the local rules' work-item extraction. Nothing a model wrote (title, summary, narrative) is part of them, so model output never feeds a rule. A fact the session lacks never matches.
- **Trust level** says what an approve rule does. *ask*: nothing, everything waits. *suggest*: matching sessions wait but are marked in the inbox with the rule, and `goldengate inbox approve --suggested` approves them together. *auto*: matching sessions are sent on the next sync, by anything that syncs, and are listed in `Report.Auto` with the rule that sent them (the receipt, #33, is written from that list). With no approve rule, the trust level covers every session of the project; adding the first approve rule narrows it to what the rules match.
- **Exclude rules** keep matching sessions on the machine at every trust level, win over approve rules, and are not overridden by `sync --approve`. They are counted, not listed.
- **Order of decisions** for a session's new or changed content (`Syncer.gate`): a held edit conflict; the user's own decision for exactly this content (an explicit approval is sent even if an exclude rule matches; an explicit exclude or retract is checked before the session is read); an exclude rule; then the trust level applied to what the approve rules match.
- **A broad pattern asks first.** Automation acts without asking only in a folder or chat grouping the user named as a member, or one from which something was already sent. When a project reaches a grouping only through something broader (a parent folder, `~/work/*`, `chatgpt:*`) and nothing was ever sent from it, the rule-approved session waits, marked as the first from that grouping. Once the user has approved and sent one session from it, the rules take over there. A worktree counts as its repository.
- **The model.** A session that waits or is excluded by a rule is built with the local rules only and never reaches the model summarizer; one that the rules send is summarized like any approved session.
- **Receipts.** Every session sent by trust and rules, and only those, is written into a receipt: one receipt per sync (`rc_...`), in the local state DB (`receipts`, `receipt_items`). `Syncer.Plan` marks such a session and names the receipt; `sync.Push` writes the item right after the session was sent and recorded, so a receipt lists exactly what left, and a push in parts fills the same receipt. A dry run writes none. An item holds what the server received and nothing more: session id, source, project id and name, the redacted title, the rule id and the rule in words as of the send (a rule can be changed or removed later). A session sent again automatically after it changed is listed again, in that sync's receipt. Sessions the user approved by hand are in no receipt. `goldengate receipts` lists them, `receipts show [id]` prints one (`--agents` asks the server, with the admin token, which agents can read each project now), and `receipts retract <id> --all|<session>...` is the session retract of the inbox: it deletes from the server first, records the decision, and marks the item retracted in every receipt that lists it. The TUI shows receipts from the inbox (`c`) and retracts with `R`. With `[ui] notify_receipts = true` (off by default) a sync that sent something automatically shows a desktop notification holding counts and the receipt id only, never a title or project name.
- **Rule offers.** When the user has sent 5 or more sessions from one folder or chat grouping of a project by hand within 30 days, Golden Gate offers the rule that would have done it: "Share sessions from ~/code/app automatically?". An offer is computed, never stored, from local state only: the record of what was sent, minus what a receipt lists as sent by trust and rules. No model is involved. It shows the exact rule, how many sessions it would have matched in those 30 days with a few of their titles, and everything accepting changes (the rule, and the trust level if the project is not at auto). Nothing changes without a yes. "Not now" is remembered per project and folder (`offers_dismissed`), so that offer is not made again; closing the offer without answering leaves it for later. No offer is made where automation already covers the folder, or where an approve rule for it exists and the user left the trust level below auto. Offers appear in `goldengate inbox`, in `goldengate rules offers` (`rules accept|dismiss <offer-id>`), and in the TUI inbox (`o`: yes, not now, or ask me later); the web UI reuses `Inbox.Offers` and `Backend.OfferDecide`.
- **Project suggestions.** A folder that has sessions and is in no project may get a suggested project, with the reason, in the inbox. It is the same kind of offer as a rule offer: computed on the laptop, never applied by itself (not even for an automatic project), answered with yes or not now, and remembered when turned down. No session of the unshared folder is read. Signals, strongest first: the folder is a worktree of a project's member (the root its tool recorded, or the `.git` pointer file); it has the same repository remote as a member; its current branch names an issue the project already has (in what was sent) or one under a work-item prefix of the project. The last two, and the pointer file, read the folder's git `HEAD` and `config` files (`project.GitInfo`: two small regular files, read-only, never tracked content) and so happen only with `enrich.git = true`. A remote shared by members of two projects suggests nothing, denied folders are never suggested, and chat groupings get no suggestion (the only signal would be their content, which needs the opt-in model and is not built). Accepting adds the folder as a member of the project in one action (`goldengate projects accept <id>`, or `o` in the TUI inbox); its sessions then wait in the inbox like any others, unless the project is at trust auto with no approve rule, which the offer says before the user answers.
- Commands: `goldengate rules list|add|remove`, `goldengate projects trust <project> ask|suggest|auto`. `projects auto on|off` and `--auto-approve` remain as shortcuts for auto and ask.

**Per-session decisions.** `goldengate sessions list|include|exclude|retract`, and the TUI inbox (`i`), work on single sessions whatever their project's mode. States shown: pending (waits), queued (the next sync sends it), sent, changed (sent; a newer version waits or is queued), excluded, retracted. Include approves the session's current content and first removes an exclude or retract. Exclude blocks a session in any project, auto-approved ones included; if it already reached the server it stays there and the command says to retract. Retract calls `DELETE /v1/sessions/{id}` (which also removes the session's work-item links and, when it was the last one, its project), then records a retracted decision and drops the local record of the push, in that order, so a failed delete changes nothing locally. A retracted or excluded session is skipped before it is read, so it reaches no payload, no dry run and no model. A session that waits for approval is not sent to the model summarizer either: it is built with the local rules until it is approved, and listings never call a model.

**Edited summaries.** The user can replace a summary's text: title, goals, done, outcomes, open problems, narrative, and which work items stay linked (lines can be deleted, not added). The edit is stored in the state DB (`edits`) with the content hash it was made on. While the session's content is that hash, `summarize.Build` uses the edit instead of any summarizer: the rules still supply the facts, every edited string goes through the same redaction and caps, the summary is marked `edited`, and no model is called, so re-sync and `--resummarize` leave it alone and such a session is never counted as stale. When the content hash differs (the session continued), nothing of the edit is applied and the payload is flagged `EditConflict` on the client: the sync never plans it, in any project and with any option, and reports it in `Report.Conflicts`. The user resolves it by editing again (the form holds the new generated summary and lists, as comments, what their earlier version said), by keeping the edit for the new content, or by discarding it. Saving a form whose text equals the generated summary removes the edit. Editing a session that is already on the server keeps it approved, so the next sync sends the user's version. The editor works on a private file in the state directory that is deleted afterwards; the TUI hands the terminal to `$VISUAL`/`$EDITOR` and reads the form back through `tui.Backend` (`EditBegin`, `EditFinish`).

Per-source rules: `exclude_sources` on a member keeps one tool's sessions of that folder out while the rest stays shared. `deny_source = ["codex@~/code/app"]` under `[share]` still works for a folder that is in no project.

Rules
- Deny beats allow. Globs supported (doublestar).
- Level `titles-only` sends just project, title, timestamps. `summary` (default) sends the redacted summary. `full` sends redacted transcripts and must require an explicit flag plus a TUI confirmation.
- **Un-sharing a project** (removing it from allow, or adding deny) offers to delete that project's data from the server (`DELETE /v1/projects/{key}`) and records the tombstone locally so it is never re-pushed.
- Selection state is also manageable headlessly: `goldengate share add <path>` (puts the folder in a project of its own), `share remove <path>`, `share list`, and `goldengate projects list|create|rename|merge|add|remove|delete`.

## 6. TUI (the "rich" part)

Stack: Go, Bubble Tea + Bubbles + Lip Gloss (and `huh` for forms). Must work in a plain 80x24 terminal, degrade without color (`NO_COLOR`), and be fully usable with either the keyboard or the mouse. Responsive to resize.

Principle: every screen answers "what leaves this machine, and when?" in words, not only glyphs. A project's line says whether it is shared and what the next sync would do with it; nothing is described as "since the last sync" unless it has been synced.

### Screens
1. **First-run wizard**: detect installed tools -> enter server URL + token (stored in keyring) -> choose share level -> pick projects -> dry-run preview -> first sync.
2. **Projects (main screen)**: the project picker. Two panes.
3. **Preview**: shows exactly what would be sent for the highlighted project (redacted summary), with redactions highlighted.
4. **Sync**: two steps. *Review* runs on open: plan, then a dry pass that reads, summarizes and redacts every session and sends nothing, then a table of what would go (per project, totals, redactions, what stays local). *Send* sends exactly the reviewed batch (it is reused while the config is unchanged and it is under 15 minutes old; otherwise the send plans again). Progress is per session while preparing and per step of 10 while sending; failures are listed per project and a retry skips what already arrived.
5. **Server**: connection health, token check, what the server currently holds per project, delete remote data.
6. **Settings**: theme (dropdown with live preview), summarizer (dropdown: local heuristic, Anthropic, OpenAI, Gemini, OpenCode Zen or a custom OpenAI-compatible endpoint; choosing a model asks first and names what is sent; then rows for model, endpoint, API key (hidden input, stored in the OS keyring, never in the config) and a test call), share level, since-window, redaction patterns, sources on/off, schedule/hook installation.
7. **Help**: keybinding overlay (`?`).

### Main screen mockup

At 80x24 (the golden snapshot `internal/goldengate/tui/testdata/projects_80x24.golden`; wider windows add a source-tag column and the sources line):

```
┌ goldengate ────────────────────────────────── ● connected bridge.example.com ┐
│ [All 8]  Shared 4   To sync 3   Not shared 4                                 │
├──────────────────────────────────────┬───────────────────────────────────────┤
│ / search projects…                   │ pharmfindr                            │
│▌ [x] ~/code/pharmfindr    ▲ 3 to sync│ ~/code/pharmfindr                     │
│  [x] …aude/worktrees/fox  ▲ 2 to sync│                                       │
│  [x] ~/code/forge         ▲ 1 to sync│ ● Shared · ▲ 3 to sync                │
│  [ ] ~/code/vihayas        not shared│ 3 sessions waiting: 1 new, 1 changed  │
│  [~] ~/code/website      ✓ synced 40h│ since the last sync.                  │
│  [-] ~/work/client-x         ✕ denied│                                       │
│  [ ] ~/scratch/tmp         not shared│ Kind      code · coding-tool sessions │
│  [ ] claude-ai:Chats       not shared│ Sessions  47                          │
│                                      │           CC Claude Code 41           │
│                                      │           CX Codex 6                  │
│                                      │ Activity  2h ago                      │
│                                      │ Synced    2h ago · 44 on the server   │
│                                      │ Level     summary                     │
│                                      │ Rule      allowed by ~/code/pharmfindr│
│                                      │                                       │
│                                      │ [s Sync now] [space Unshare]          │
│                                      │ [p Preview] [e Sources]               │
├──────────────────────────────────────┴───────────────────────────────────────┤
│ space unshare  p preview  s sync  f view  / search  d deny  u undo  ? help   │
└──────────────────────────────────────────────────────────────────────────────┘
```

Legend: `[x]` shared, `[ ]` not shared, `[~]` some sources shared, `[-]` denied. Sync column: `▲ n to sync` (new or changed sessions the next sync sends; shared projects only), `✓ synced 2h` (up to date), `not shared` (stays local), `✕ denied`. Source tags: `CC` Claude Code, `CX` Codex, `OC` OpenCode (kind *code*), `AI` claude.ai, `GPT` ChatGPT (kind *chat*). The card states the project's sync state in a sentence: not shared ("nothing from this project leaves your machine"), shared and never synced, waiting, or up to date. The per-project sync record (`Synced`, "on the server") comes from the local state database.

### Required TUI features
- Project list grouped by normalized folder across all tools, with per-source badges, session counts, last-activity time, and unsynced counts. Sort by recent / name / size.
- Fuzzy filter (`/`), multi-select (space, `v` range, `a` all visible, `A` none), `d` deny, `u` undo.
- Views (`f`, or click a chip): All / Shared / To sync / Not shared, each with its count.
- Toggle sharing per project and optionally per source within a project (`e`). On a project that is not shared, ticking a source shares the project with only that source; a denied project must be shared first.
- Tree mode (`t`) to browse by directory hierarchy and select a parent folder to cover children (writes a glob rule).
- Live preview pane of the exact payload (redacted), with a diff of what changed since last sync.
- Secret-redaction highlights, and a "paranoid" toggle that previews every string leaving the machine.
- Dry-run (`--dry-run`; in the TUI the Review step): shows what would be sent and removes nothing.
- Sync screen with a Review → Send stepper, progress bars that follow each session, per-project status, and error details.
- Remote view: list/delete what the server holds; "forget project" action.
- Mouse: every action has a click target (rows, the share box, tabs, view chips, card and modal buttons, settings values, the theme dropdown, the hint-bar keys); wheel scrolls; double-click previews; ctrl+click marks. Views register click zones while they render, so a click can only hit what is on screen.
- Themes: named presets (Golden Gate Light, the default, and Dark; GitHub, Gruvbox, Catppuccin, Solarized in light and dark; Dracula, Monokai, Nord, Tokyo Night), each one palette from which every style is derived. The terminal background is set to the theme's while the UI runs (OSC 11) and restored on exit. Chosen in Settings, saved as `[ui] theme`, overridable with `GOLDENGATE_THEME`. No layout jank on resize.
- Non-interactive parity: everything the TUI does has a CLI equivalent so hooks/cron can use it.

### 6.1 Local web UI (`goldengate ui`): design and security model

Status: designed here (issue #45), not built yet. The API comes with #46, the screens with #47 and #48. Nothing below exists in a release until those land.

`goldengate ui` runs the same product in a browser, for people who do not live in a terminal. It is a second front end on `tui.Backend`, not a second implementation: every selection, preview, redaction, validation and send goes through the code the TUI and the commands already use, and through the single push path (`sync.Push`). It adds no capability. What it does add is a browser-facing surface on the laptop, so this section is mostly about keeping that surface closed.

**What it is not.** It is not a hosted app and not a remote admin console. It never listens on a network interface, has no accounts and serves one person: whoever started it. `goldengatebridge` keeps having no browser-facing surface apart from the OAuth consent pages.

#### Process and lifetime

- `goldengate ui` starts an HTTP server inside the `goldengate` process, prints one line saying where it listens, opens the default browser and runs until Ctrl-C. No daemon, nothing installed, nothing left running. `--no-open` only prints the link (for SSH port forwarding).
- It binds `127.0.0.1` on a port the OS picks (`:0`). There is no flag for the address: binding anything but loopback is not possible. `--port` may pin the port, for forwarding.
- Plain HTTP on loopback. TLS would need a locally trusted certificate, which is a larger risk and a worse first run than what it protects against here.
- One process serves one browser session. Closing the terminal ends it; an idle session expires after 30 minutes without a request and the page says so.

#### Getting in: launch token, then a session cookie

1. At start the process creates a **launch token**: 32 random bytes, single use, valid for 2 minutes. It opens `http://127.0.0.1:<port>/launch#<token>`. The token sits in the URL fragment, so it is not sent in a request line, not written to any log and not passed on as a referrer.
2. The launch page (static, no data) reads the fragment, removes it from the address bar and history (`history.replaceState`), and posts it to `/api/session`.
3. The server compares it in constant time, burns it, and answers with the **session cookie** and, in the body, a **request token**.
   - Cookie: 32 random bytes, `HttpOnly`, `SameSite=Strict`, `Path=/`, no `Domain`, no expiry (gone when the browser closes), and a name that carries the port (`gg_ui_<port>`). It is not marked `Secure` because the page is plain HTTP on loopback, where not every browser accepts `Secure`.
   - Request token: 32 random bytes held only in the page's memory and sent as the `X-GoldenGate-Request` header on **every** API call, reads included.
4. Wrong, used or expired launch tokens get the same plain 403, and five failures stop the server from accepting launch tokens until the user asks for a new link (Enter in the terminal prints one).
5. Both secrets live only in this process's memory. They are never written to disk, and they die with the process. A second `goldengate ui` has its own port, cookie name and secrets.

Why two secrets: browsers send cookies to every port of a host, so another web server on `127.0.0.1` would receive our cookie if the user opened it. The cookie alone therefore opens nothing: the request token is never sent by the browser on its own, and a page from any other origin cannot read it.

#### Request checks (every request, before any handler)

In this order; a failure is a bare 403 or 400 with no detail and no CORS headers.

1. **Host**: the `Host` header must be exactly `127.0.0.1:<port>` or `localhost:<port>`. This is the DNS-rebinding defence: a hostile site whose name is re-pointed to 127.0.0.1 still sends its own name as Host.
2. **Method and shape**: only `GET` and `POST`. `GET` never changes anything. `OPTIONS` is refused, so no cross-origin preflight can ever succeed. `POST` bodies must be `Content-Type: application/json` (a type an HTML form cannot send), at most 1 MB, with unknown fields rejected.
3. **Origin**: on every `POST`, `Origin` must equal `http://127.0.0.1:<port>` or `http://localhost:<port>` (the same host the request came in on). A missing `Origin` on a `POST` is refused. Where the browser sends `Sec-Fetch-Site`, it must be `same-origin` on API calls, and `none` or `same-origin` on page loads.
4. **Session**: the cookie and the `X-GoldenGate-Request` header must both match, compared in constant time. This is the CSRF protection: a cross-site request cannot carry the header, and `SameSite=Strict` keeps the cookie off it as well. The only paths served without a session are `/launch`, the static assets it needs and `POST /api/session`.

There is no CORS: the server never sends an `Access-Control-*` header.

Every response carries: `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin`, `Cross-Origin-Resource-Policy: same-origin`, and on API responses `Cache-Control: no-store`. The page loads nothing from any other origin: no CDN, no web fonts, no analytics. That keeps "no telemetry" true for the browser too.

#### Secrets go in and never come back

The page can set a secret but can never read one. This holds for the ingest token, the admin secret, pairing codes and summarizer API keys.

- Secret fields are write-only in the API: a request may carry a new value; responses carry only whether one is set and where it is kept (`"set": true, "store": "keyring"`). No endpoint returns a stored secret, in whole or masked, and none is ever placed in a URL.
- The admin secret (agents and grants) is posted once, held in the UI process's memory for the run, as the TUI does, and never stored. The page does not keep it; "Forget admin secret" drops it at once.
- One exception, stated on screen: a value the user must copy somewhere else is shown once, at the moment it is created. That is the read token (or connector URL) at the end of setup, exactly as the TUI and `goldengate init` show it. It is returned by the call that creates it and cannot be fetched again.
- Secrets are never logged. The UI's request log, off by default, records method, path and status only.

#### Session text is untrusted here too

Summaries, titles, work-item keys and file names come from transcripts and can contain markup written by a web page or a README. The page treats every such string as text: it is inserted with `textContent`, never as HTML, and the CSP above forbids inline script, inline style and `eval`, so a string that slips through still cannot run. Links built from stored URLs are shown as text with an explicit "open" control, open with `rel="noopener noreferrer"`, and only `https:` is linkable.

#### API shape (built in #46)

- One endpoint per `tui.Backend` method under `/api/`, `GET` for reads and `POST` for anything that changes state or contacts the server. The JSON types are the `tui` types, so the TUI's fake backend drives the contract tests.
- Sync progress: `POST /api/sync` starts a run and returns its id; `GET /api/sync/<id>/events` streams `SyncEvent`s as server-sent events over the same checks. One run at a time, cancelled when the stream's owner asks or the process ends.
- Three Backend methods do not map directly and need a web form of the same behaviour: `DeleteRemote` takes an opaque handle (the API will use the project id); `EditBegin`/`EditFinish` hand a private file to a terminal editor (the API returns the form's text and accepts it back, the file stays private to the process and is deleted as today); `Init` returns the show-once read token (see above).
- Errors are JSON with a message meant for the user. They never include a secret, a path outside the user's projects or a session from an unshared project (the Backend already guarantees the last two).

#### Frontend stack and embedding

- Hand-written HTML, CSS and JavaScript (ES modules), with **no build step and no third-party JavaScript**: no npm, no bundler, no framework. The screens are forms, tables and one event stream, which the platform handles. In return there is no Node toolchain in CI, no JavaScript supply chain to audit, and the strict CSP above works without exceptions.
- Assets live in `internal/goldengate/webui/assets/` and are compiled into the binary with `//go:embed`. `goldengate` stays a single static binary that works offline. Assets are served from memory with a content hash in the path and `Cache-Control: immutable`; HTML is `no-store`.
- Styling uses the brand tokens in `docs/brand/BRAND.md` as CSS custom properties, light and dark (following the system setting, with the same override as the TUI theme), system fonts only. Every state pairs colour with a glyph or label. Everything is reachable by keyboard, and layouts work at phone width.
- Each screen says in words what leaves the machine and when (principle 8), using the same wording as the TUI.
- Code layout: `internal/goldengate/webui` holds the server, the checks above as one middleware, and the embedded assets; `cli` adds the `ui` command. The package depends on `tui` for the Backend interface and types only.

#### Reviewed against SECURITY.md

| Promise in SECURITY.md | How the web UI keeps it |
|---|---|
| Default deny; a deny rule wins | Selection goes through `Backend.Apply`, the code the TUI uses. The API has no other way to change what is shared. |
| One door (`sync.Push`) | The UI calls `Backend.Sync`. It has no upload code and no access to the bridge client. |
| Previews are exact | Preview and the Review step return the payloads the send would use; the page renders them without rewording. |
| Nothing new leaves without approval | Approval is the same inbox decision; the page's Send is the reviewed batch, as in the TUI. |
| Retract, exclude, edits | Same Backend methods; edited text is redacted by `summarize.Build`, not by the page. |
| Tokens in the OS keyring, never displayed | Write-only secret fields; the show-once exception is the one the TUI already has. |
| No telemetry | No third-party origin is ever contacted by the page; the CSP enforces it. |
| No browser-facing surface on the server | Unchanged: this is the laptop binary, loopback only. |
| Stored text is untrusted | Rendered as text under a CSP without inline script. |

What it adds to the threat model, and what answers it:

| Threat | Defence |
|---|---|
| A website the user visits calls the local API (CSRF) | Request token header on every call, `SameSite=Strict`, Origin check, JSON-only bodies, no CORS, preflights refused |
| DNS rebinding | Exact Host allow-list |
| Another device on the network | Loopback bind only, no option to change it |
| The launch link leaks (history, logs, referrer, shoulder) | Token in the fragment, single use, 2 minutes, removed from history, `Referrer-Policy: no-referrer` |
| Another local web server receives the cookie | Cookie alone is not enough; per-port cookie name; per-run secrets |
| Script injected through session text (XSS) | Text-only rendering, strict CSP, no inline script or style |
| Clickjacking | `frame-ancestors 'none'`, `X-Frame-Options: DENY` |
| A secret read back through the page | No endpoint returns one |

Residual risks, to be listed in SECURITY.md when the UI ships:
- Another program running as the same user can already read the config, the state database and, on most systems, the keyring. The web UI does not make that worse, and does not defend against it.
- Another user on a shared machine can connect to the loopback port. Without the launch token they get nothing, but they can see that the port is open.
- A browser extension with access to all sites can read the page, like any other page.
- The show-once read token is on screen and in the page's memory until the user leaves that step.

Tests that #46 must include (the "security checks" its issue refers to): a wrong `Host`; a `POST` with a missing, foreign or `null` `Origin`; a missing or wrong cookie; a missing or wrong request token; a reused, expired and guessed launch token; `OPTIONS`; a form-encoded body; an oversized body; every response's headers; that no response to any endpoint contains a stored secret; and that the listener's address is loopback.

## 7. CLI surface

```
goldengate                      # open TUI
goldengate ui [--no-open] [--port N]   # (planned, see 6.1) the same product in a browser, on loopback only
goldengate init [--server URL] [--pair-code CODE]   # first-run wizard; headless via flags/env (see 7.1)
goldengate sync [--dry-run] [--project PATH] [--since 7d] [--source codex]
goldengate share add|remove|list
goldengate projects list|create|rename|merge|add|remove|delete   # named projects; members are what is shared
goldengate inbox [list|approve|exclude|reset]                      # sessions waiting for approval; sync sends only approved ones
goldengate sessions edit <id> [--keep|--discard]                   # your own version of a summary; never overwritten
goldengate sessions list|include|exclude|retract                   # decide single sessions; retract deletes a sent one from the server
goldengate sync --approve                                          # approve and send what waits (with --dry-run: look first)
goldengate projects trust <project> ask|suggest|auto               # how far a project's automation goes
goldengate rules list|add|remove                                   # rules that approve or exclude a project's sessions by facts
goldengate rules offers | accept <offer-id> | dismiss <offer-id>   # rules offered from your repeated manual choices; only a yes changes anything
goldengate projects suggest | accept <id> | dismiss <id>           # a project suggested for a folder that is in none; only a yes shares it
goldengate inbox approve --suggested                               # approve what the rules marked
goldengate receipts [show [id] [--agents] | retract <id> --all]    # what was sent automatically, and why; take it back
goldengate projects auto <project> on|off                          # shortcut: trust auto or ask
goldengate agents list|add|rename|limit|remove                    # one agent per assistant; needs the admin token
goldengate agents grant|revoke <agent> <project>... | --all       # which projects an agent sees
goldengate agents token <agent>                                   # a read token for that agent, shown once
goldengate import-claude-ai ./export.zip
goldengate doctor               # detects tools, checks formats, server, token
goldengate sync --auto          # unattended run: only what is approved or rule-approved; one-line report; everything sent is in a receipt
goldengate install-hook claude-code     # adds a SessionEnd hook that runs `goldengate sync --auto --quiet --source claude-code`
goldengate install-schedule [--every 30m]   # systemd user timer / launchd agent / Task Scheduler running `goldengate sync --auto`
goldengate uninstall-hook claude-code | uninstall-schedule      # remove them; every installer has --print
goldengate purge --project PATH # delete remote data
goldengate token rotate read|ingest   # mint a replacement token; needs the admin token
goldengate summarizer use heuristic|anthropic|openai|gemini|opencode|custom [--model M] [--base-url URL]   # opt-in model summaries
goldengate summarizer set-key <provider> [--delete]   # API key into the OS keyring
goldengate summarizer status|test   # where the key comes from; one sample call
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
- Unattended runs use `goldengate sync --auto`. It goes through the same plan and the same gate as every sync, so it sends only sessions the user approved or that a project's trust level and rules send, and never what waits in the inbox; it refuses `--approve` and `--resummarize`. It prints one line of counts (what was sent and under which receipt, what waits, what is held or excluded) and nothing with `--quiet`. Everything it sends is listed in the receipt, also sessions the user approved by hand earlier ("you approved it in the inbox"), because they too leave while nobody is watching (`RunOpts.Unattended`). The model summarizer runs only if the user turned it on, and only for sessions that are sent.
- Claude Code: `goldengate install-hook claude-code` adds a `SessionEnd` hook to Claude Code's `settings.json` that runs `goldengate sync --auto --quiet --source claude-code`. The rest of the file is kept, the file as it was is copied to `settings.json.goldengate-backup`, an existing Golden Gate hook is replaced rather than doubled, and a file that is not valid JSON is left alone.
- Codex, OpenCode and chats: `goldengate install-schedule` installs a timer for the user (default every 30 minutes, 5m to 24h): a systemd user service and timer on Linux, a launchd agent on macOS, a Task Scheduler task on Windows. The unit holds only the path of the binary and `sync --auto`: no token and no option that approves.
- Both installers print every file they write and every command they run, have `--print` to show that without changing anything, and have an uninstall command.
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

Redaction (`internal/utils/redact`)
- Pattern rules for common secrets (cloud keys, API tokens, private key blocks, JWTs, connection strings, `Authorization:` headers) plus high-entropy-string detection. Allow user `extra_patterns`.
- Strip `.env`-style content and never include file contents from files whose names match secret patterns (`.env*`, `*.pem`, `id_rsa*`, `credentials*`).
- Path scrubbing: replace the home directory with `~`, drop absolute paths outside shared projects.
- Redact twice (before and after summarization) because summaries can re-emit secrets.
- Redactions are logged locally as counts and kinds only, never the secret.
- State plainly in docs that regex redaction is best-effort, not a guarantee.

Summarization (`internal/goldengate/summarize`)
- **Two parts with different owners.** Every summary has *facts* and may have a *narrative*.
  - Facts are always written by the local rules, whichever summarizer is on: `outcomes[]`, files touched, tools used, command names, duration, turn count, git branch, first and last message, and the work items (4.1). Rules and project suggestions (M7) may use only this part.
  - The narrative is prose with one of three writers. The heuristic (default) writes none, so its payloads are unchanged. A model (opt-in) writes `narrative`: paragraphs covering what was attempted, the decisions and why, approaches rejected, what is left open and how the work relates to the issues it names, plus a one or two sentence `abstract`. The user's own narrative (`Options.UserNarrative`; the editing UI is #30) wins over both and a model run never replaces it. `narrative_by` names the writer (`<provider>:<model>` or `user`) and `narrative_length` the setting a model wrote under.
  - Length setting `[summarize] length`: `short` (800 characters), `standard` (2000, default) or `detailed` (4000). Code blocks are removed, the text is redacted before and after cutting, and a cut lands on a paragraph or sentence end.
  - Caps are separate: the facts and short fields stay under 3 KB on the client and `MaxSummaryJSON` (4 KB) on the server; the narrative has `MaxNarrativeLen` (4000 characters) and `MaxNarrativeJSON` (8 KB), checked on both sides. A long narrative never pushes facts out.
- **Summary schema.** A coding session often covers several tasks, so the work fields are lists: `goals[]` (what was asked), `description[]` (what was done; written by model summarizers before narratives existed and still read), `outcomes[]` (tests, commits, PRs, open problems), plus `title`, `first_message`, `last_message`, files touched, tools used, command names, duration, turn count, git branch and `summarized_by` (`heuristic` or `<provider>:<model>`). The older single `goal` / `outcome` / `last_assistant` fields are still accepted and read, so stored records keep working.
- **Default: heuristic, fully local.** Title (the tool's title or the first prompt), `goals` = the first prompt, `outcomes` from command results (tests passed or failing, commit made, PR opened), `first_message`, `last_message`, files touched (top N), tools used, commands run (names only), duration, turn count. No `description`.
- **Opt-in: model summarizer** (`internal/goldengate/llm`, wired in `summarize/model.go`). The provider catalog is `llm.Providers`: `anthropic` (Claude through the official SDK; default `claude-haiku-5-5`, low effort, structured JSON output); `openai` (ChatGPT API, `gpt-5.4-nano`), `gemini` (Google's OpenAI-compatible endpoint, `gemini-3.5-flash-lite`), `opencode` (OpenCode Zen, `glm-5.3-flash`) and `custom` (any OpenAI-compatible endpoint; default a local Ollama), all through one chat-completions client with `response_format: json_schema`. Endpoints and models are defaults, overridable per config, since providers rename models often. Keys: OS keyring, else each provider's env variable (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, `OPENCODE_API_KEY`, `GOLDENGATE_SUMMARIZER_KEY`). It runs on top of the heuristic's facts and writes the title, goals, open problems, the abstract and the narrative.
  - Input: the session's developer and assistant messages, each redacted then capped at 2000 characters, the whole cut in the middle beyond `max_input_chars`, then redacted again; plus the file list, command names and observed results. Never tool output, file contents or command arguments.
  - Output: constrained by a JSON schema (`title`, `abstract`, `narrative`, `goals`, `open_problems`), then every string is redacted again and capped (8 items per list, 300 characters each; the narrative by its length setting), and the facts are shrunk to 3 KB. The prompt forbids code, command arguments, tool output, secrets and the text of issue descriptions or comments.
  - Only sessions of shared projects reach it (the plan filters before loading). Previews run offline: cached answers only, never a call.
  - Answers are cached in the state DB (`model_summaries`) by a hash of prompt version, model, narrative length and exact input, so a session is summarized once per content and payload hashes stay stable.
  - Calls run 4 at a time during a plan; results keep their order. A failed or unreadable answer keeps the heuristic summary and is counted (`ModelFailures`).
  - Re-summarizing: the plan skips sessions whose file stat is unchanged, so turning the model on affects only new or changed sessions. `RunOpts.Resummarize` (CLI `sync --resummarize`; Sync screen button "Include them in this sync", which re-runs the review with the flag) also loads already-pushed sessions of shared projects whose stored payload's `summary.summarized_by` differs from the configured model's name (the heuristic, or another model; titles-only payloads have no summary and are left alone), or whose model-written narrative was made under another length setting. Changing the length therefore re-summarizes synced sessions only when the user asks. A narrative written by the user is never stale. They are rebuilt like any session: the model cache prevents a second call for the same input, and the content-hash comparison pushes only those whose payload changed (a failed call keeps the heuristic summary, so nothing is pushed). Without the flag such sessions are counted in `Report.Stale`, which the CLI prints and the review shows.
  - Off by default. Turning it on (CLI `summarizer use`, or Settings → Summarizer) states what will be sent; the TUI asks for confirmation. Keys come from the OS keyring (`summarizer set-key`) or `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`.
- Hard size caps per summary (e.g. 2 KB) so MCP responses stay small. Work items (see 4.1) are carried as separate structured records alongside the summary, with their own caps, and are shown in the preview exactly as they will be sent.

## 9. Server

### Endpoints
- `POST /v1/ingest` — batch upsert of summary records. Bearer token with `ingest` scope.
- `DELETE /v1/projects/{key}` and `DELETE /v1/sessions/{id}` — remove data. `ingest` scope. `{key}` is a project id (the whole project), else the key of a folder or chat grouping (only the sessions that came from it), else a project name.
- `GET /v1/projects` — what the server holds, with each project's `id` and name (for the TUI Server screen).
- `POST|GET /mcp` — MCP streamable HTTP. Bearer token with `read` scope.
- `POST /v1/admin/tokens`, `GET /v1/admin/tokens`, `DELETE /v1/admin/tokens/{id}` — mint, list, revoke tokens. `admin` scope only.
- `GET|POST /v1/admin/agents`, `PATCH|DELETE /v1/admin/agents/{id}`, `POST /v1/admin/agents/{id}/grants`, `DELETE /v1/admin/agents/{id}/grants/{project}`, `POST /v1/admin/agents/{id}/tokens` — manage agents, the projects each may see, and their read tokens. `admin` scope only: the laptop's `ingest` token cannot change who reads what. The listing shows project ids and names, never session content.
- `POST /v1/pair` — exchange a single-use pairing code for an `ingest` token. Unauthenticated, so strictly rate limited and burn-after-5-failures.
- `GET /healthz` — unauthenticated, no data.

Run in **stateless mode with plain JSON responses** (no server-held sessions, no long-lived SSE), so it works on Cloud Run scale-to-zero and behind Firebase Hosting rewrites.

### MCP tools (all annotated read-only)
| Tool | Purpose |
|---|---|
| `recent_activity(days=5, since?, until?, project?, source?)` | What was worked on, grouped by project, newest first |
| `list_projects(source?)` | Shared projects with last activity and session counts |
| `get_project_context(project)` | Where a project stands: active threads first, then recent sessions and files |
| `get_work_item(key, project?)` | One issue or pull request as a thread: related items, linked sessions across sources, status history with evidence |
| `search_work(query, project?, limit=10)` | Keyword search over titles, summaries and narratives |
| `get_session(id)` | One session in full: facts, work items and the whole narrative |
| `list_commits(project?, days=5, since?, until?, status?)` | Commits created or mentioned, with evidence and verification state |
| `list_pull_requests(project?, state?, days=5, since?, until?)` | PRs opened, merged, closed, plus "mentioned but not confirmed" |
| `list_artifacts(project?, source?, days=5, since?, until?)` | Files and artifacts created in coding and chat sessions (metadata only) |
| `list_issues(project?, provider?, state?, days=5, since?, until?)` | Linear, Jira, GitHub and GitLab issues created, updated, completed or mentioned, with state as of the session |

Time window (all time-bounded tools above)
- Default: the last 5 days. Agents see recent work by default and more only when they ask (principle 5 in 1.1).
- `days` widens it (at most 365). `since` and `until` take a `YYYY-MM-DD` date (UTC) or an RFC 3339 time; a bare `until` date includes that whole day. `since` takes precedence over `days`; `until` alone means `days` before `until`. A range may span at most 365 days.
- Every result states its window (a `Window:` line and a structured `window` field), and an empty result says how to look further back.
- This is separate from the client's upload look-back (`[share] since`), which decides what reaches the server at all.

Two tiers keep list responses small: `recent_activity`, `get_project_context` and `search_work` return each session's `abstract` (the model's, or the first sentence of the narrative) and never the narrative; only `get_session` and `get_work_item` return `narrative`, with `narrative_by`.

Context is organized around work items, so an assistant reads "LIN-412: approach decided in a chat, implemented in Claude Code, pull request open" rather than a list of sessions.

- `get_project_context` leads with the project's **active threads** (4.1), most recent first, at most 10 (the rest are counted). Each shows the item it is named after (key, title, URL, status as of the last session that touched it, and whether that status is confirmed by evidence), related items, a one or two line summary from the latest session (its abstract, else its recorded outcomes), which tools touched it, how many sessions, and the latest session. Recent sessions follow in short form, then frequent files and commands.
- A thread is active unless the sessions give evidence that it is finished: an issue that a session moved to a done state, or, when the thread has pull requests, every one of them merged or closed. A mention proves nothing either way, so a thread that was only ever mentioned stays active.
- `get_work_item` takes a key from another tool (`linear:LIN-412`, `owner/repo#7`), the bare tracker key (`LIN-412`), a URL, or a short `#7`, optionally narrowed to a project. It returns the thread: its items, every linked session newest first with what that session did to each item (status, and whether it was on the issue's branch), and the status history oldest first, one line per session and item with the evidence behind it. Full narratives are included newest first up to 8000 characters in total; older sessions carry their abstract and the result says so. A key that matches several items is refused with a count and no names; an unknown key and one outside the agent's grants answer alike.
- Both are built from the sessions the calling agent may see (grants and history window apply inside the store), so a thread never includes a session from a project the agent was not granted.

Guidance
- Return structured content plus a short text rendering; cap result size; support pagination cursors.
- Tool descriptions should tell the assistant when to use each tool ("use when the user asks what they've been working on").
- Consider also exposing a `work://projects/{key}` resource and a "daily digest" prompt.

### Storage
`Store` interface with implementations: `memory` (tests), `sqlite` (self-host, single volume), `postgres`, `firestore` (Cloud Run friendly). Keep queries simple (by project, by time range, text search) so the interface is easy to implement. SQLite uses FTS5 for `search_work`; Firestore can use simple token matching. Work items live in their own table/collection keyed by `(project, kind, key)`, so one commit or PR seen in several sessions is a single record; index on status and `last_seen`.

**Projects.** The server files everything under a stable project id, with a display name in a `projects` table (id, name, created, updated). Payload version 2 carries `project_id` and `project_name` next to `project`, which stays the key of the folder or chat grouping the session came from. A rename arrives with the next payload and applies to the whole project; a session that arrives under another id moves there, and a project left without sessions is removed. The older payload shape (no id) is still accepted: its key serves as both id and name. Existing SQLite databases upgrade in place at start: one project per distinct old key, named after it, so every tool answers as before. When a project is renamed, merged or first given an id, the client re-sends the payloads it already sent with only the project fields changed (`Report.Refiled`): nothing is read or summarized again. `ResolveProject` accepts an id, a name (case-insensitive) or a folder key; every MCP `project` argument goes through it, results show the name, and structured results also carry `project_id` and the session's `folder`. The audit log records project ids.

**Agents and grants.** A reader of the MCP endpoint is a named agent (`agents`: id, name, created, optional `max_days`). Every `read` token and OAuth client belongs to exactly one agent; one created without naming an agent belongs to `Default`. `agent_grants(agent_id, project_id)` lists what each agent may see. Nothing is granted automatically: a new project is visible to no agent until it is granted. On upgrade, readers that predate agents become `Default`, which is granted every project that exists at that moment, once. Grants follow a project that gets a new id (a session moving into a project created by that request), are not widened by a move into an existing project, and are removed with their project or agent. Deleting an agent revokes its tokens and deletes its OAuth clients. An agent may instead have `all_projects` set: it then sees every project, including ones that arrive later. That is a choice the user makes per agent (`goldengate agents grant <agent> --all`), never a default for a new agent. One exception is spelled out at setup: when `goldengate init` (or the wizard) connects to a server that has no agents yet, the read token it mints is the first assistant's, and its agent (`Default`) is set to see every shared project, and the setup says so. On a server that already has agents, init changes nothing.

**Agents screen (TUI, `5`).** A grid of projects by agents, with a first row for the "every project" switch. Toggling collects changes; nothing is sent until the user reviews the list and confirms, and a failed apply keeps what is left. `v` is "view as agent": the agent's projects plus the summaries and work items of the last 5 days in them. The admin scope cannot read work data, so the view is built on the laptop from the agent's grants (admin API) applied to the payloads this computer pushed (state DB); sessions synced from another computer are not listed, and the screen says so. `d` shows an agent's tokens, OAuth applications and last use, and revokes a token. The admin secret is asked for once per run, held in memory, passed to each `tui.Backend` call (`Agents`, `ApplyGrants`, `AgentView`, `RevokeAgentToken`) and never stored; the web UI can reuse the same four methods.

**OAuth consent.** Consent has two steps, both server-rendered without JavaScript. The first asks for the admin secret and shows only the application and its redirect address. When the secret is right, nothing is issued yet: the pending request is re-stored as approved under a new single-use id, and the second page lists agents and projects. The owner picks a new agent (name prefilled with the application's) or an existing one, ticks projects (none ticked by default) or "every project", and only then is the authorization code issued. The client is attached to that agent, so its tokens carry the agent's grants. A second-step form posted for a request that was never approved is a denial, form mistakes change nothing, and the approval is audited with the client and the agent.

**Enforcement.** For every `/mcp` request the auth middleware resolves the token's agent, loads its grants and history window, and passes them to the tools as a `store.Scope` (project ids, `NotBefore`). The tool wrapper puts that scope in the request context, and the store's read methods (`Sessions`, `GetSession`, `Search`, `WorkItems`, `ListProjects`, `ResolveProject`) apply it in the query itself, so no tool can forget the filter. A project outside the scope does not resolve by id, name or folder and its sessions do not exist for that request. Grants are read per request, so granting or revoking takes effect on the next call without a new token. A token with no agent, or whose agent is gone, sees nothing. `max_days` also cuts an explicitly wider `days`/`since`, and the result's window description says so. The audit log records the agent id next to the token id.

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
7. **M7 product v2:** the principles in 1.1 made real: named projects and stable project IDs, per-agent access, the inbox with session-level curation, editable summaries and user-approved rules with receipts, facts and narrative summaries built around work items (with GitLab and cross-tool threads), the 5-day default window, chat connectors (per-conversation sharing, incremental export import, share links), a Cursor adapter, and a local web UI (`goldengate ui`). Tracked in the GitHub "Product v2" milestone.

## 14. Acceptance criteria

- Fresh install -> wizard -> pick two projects -> sync -> assistant answers "what did I work on this week?" using only those two projects.
- A project not selected, or matched by a deny rule, never appears in any payload, log, or server record (test this).
- Removing a project from the selection and confirming deletes its remote data.
- Re-running sync twice sends nothing the second time.
- A planted fake secret in a transcript does not appear in the preview or the pushed summary.
- Server refuses requests without a valid token of the right scope.
- Per-agent access is tested: an agent sees only the projects granted to it through every MCP tool, including full-text search and a work item whose key exists in several projects; an ungranted project or session answers exactly like one that does not exist (no "forbidden" hint, no counts); a new project is visible to no agent until it is granted; an agent's maximum history window caps every tool; the audit log records the agent id.
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
