# Golden Gate user guide

This guide walks you through the whole product on one computer: a server, the terminal UI (TUI), the browser UI, and an assistant reading what you shared. Follow it top to bottom the first time. Every part ends with a short list of things to check, so you can use it as a test plan too.

- `goldengate` runs on your laptop. It reads your coding tools' history, lets you choose what is shared, redacts and summarizes, and sends.
- `goldengatebridge` is your server. It stores the summaries and serves them, read-only, to the assistants you connect.

```
coding tools ──► goldengate ──(redacted summaries)──► goldengatebridge ──(MCP, read-only)──► assistant
 (read-only)     TUI · browser · commands             your server                          Claude, ChatGPT, …
```

Nothing leaves your computer until you send it, and every screen says what would leave.

---

## 0. Before you start

### Build and install both programs

From the repository:

```bash
cd ~/Divyanshu/goldengate
go install ./cmd/goldengate ./cmd/goldengatebridge
```

They land in `$(go env GOPATH)/bin` (here `~/go/bin`). Make sure that folder is on your `PATH`:

```bash
export PATH="$HOME/go/bin:$PATH"
goldengate version && goldengatebridge version
```

### Start from a clean slate (keeps your old setup)

Golden Gate keeps its settings in `~/.config/goldengate/config.toml` and what it has sent in `~/.local/state/goldengate/state.db`. To test the first-run experience, move any earlier setup aside. You can put it back at the end (section 10).

```bash
mv ~/.config/goldengate ~/.config/goldengate.before-test 2>/dev/null
mv ~/.local/state/goldengate ~/.local/state/goldengate.before-test 2>/dev/null
```

Your coding tools are only ever read, never changed, so you test against your real history safely.

### Check what Golden Gate can see

```bash
goldengate doctor
```

It lists the tools it found (Claude Code, Codex, OpenCode, Cursor) and how many sessions each has. Nothing goes over the network.

---

## 1. Run a server on this computer

For testing, the server runs on `127.0.0.1` (this computer only), so plain HTTP is fine. On a real deployment it sits behind HTTPS (see the README quickstart).

Open a terminal you can leave running. Create the **admin secret** once and keep it in a private file for this test:

```bash
mkdir -p ~/gg-test && cd ~/gg-test
openssl rand -base64 32 > admin-secret.txt && chmod 600 admin-secret.txt
GOLDENGATE_ADMIN_TOKEN="$(cat admin-secret.txt)" goldengatebridge serve --addr 127.0.0.1:8080 --db ~/gg-test/bridge.db
```

It prints `listening on 127.0.0.1:8080 …`. Leave it running. Stop it later with Ctrl-C.

The admin secret is the server's master key. Golden Gate uses it once to make two narrower tokens, and for changing who can read what. It is never written to disk by `goldengate`.

| Token | Who holds it | What it can do |
|---|---|---|
| **Admin secret** | you | create and revoke tokens, change which assistant sees which project |
| **Upload token** | your laptop, in the OS keyring | send summaries, delete a project from the server |
| **Assistant token** | an assistant | read what you shared, nothing else |

**Check**
- [ ] `curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/healthz` prints `200`.

---

## 2. The terminal UI (TUI)

In a second terminal:

```bash
goldengate
```

### 2.1 First-run setup (6 steps)

| Step | What you see | What to do |
|---|---|---|
| 1 · Welcome | The tools found on this computer, with session counts | `enter` |
| 2 · Server | Three choices: admin token, pairing code, or stay local | Keep **Connect with the admin token**. Server URL `http://127.0.0.1:8080`. Paste the admin secret (`cat ~/gg-test/admin-secret.txt`); the field hides it. `enter` |
| 2 · Connected | The assistant token (shown **once**) and the connector URL | Copy the token into a password manager or `~/gg-test/assistant-token.txt` (`chmod 600`). You need it in section 4. `enter` |
| 3 · Share level | **summary** (title, goal, files, commands, outcome, work items) or **titles-only** | Keep summary. `enter` |
| 4 · Choose projects | Every project folder, all `[ ] not shared` | Move with `↑ ↓`, `space` to share one or two projects. `p` shows exactly what would be sent. `enter` |
| 5 · Review | A dry run: how many sessions, projects and redactions the first send would have | `enter`. Nothing has been sent yet. |
| 6 · First send | The first send | Confirm to send, or finish later |

`esc` at any step skips setup; you land on the Projects screen and can rerun setup from the Server screen (`i`).

> The setup screens still say "read token" and "ingest token" for the assistant token and the upload token. They are the same thing.

### 2.2 Getting around

| Key | Where it goes |
|---|---|
| `1` … `5` or `tab` | Projects, Send, Server, Settings, Agents |
| `s` | Send (review first) |
| `i` | Inbox: single sessions |
| `?` | Every key on the current screen |
| `r` | Refresh |
| `q` or `ctrl+c` | Quit |

The mouse works too: click rows, tabs, buttons and the keys in the bottom bar; double-click a project to preview it; the wheel scrolls.

### 2.3 Projects (`1`)

The list on the left, a card about the highlighted project on the right.

| Mark | Meaning |
|---|---|
| `[x]` | shared |
| `[ ]` | not shared: nothing leaves |
| `[~]` | shared, but only some of its tools |
| `[-]` | blocked: never sent, even when a parent folder is shared |
| `▲ 3 to send` | 3 sessions new or changed since the last send |
| `✓ sent 2h` | up to date on the server |

Source tags: `CC` Claude Code, `CX` Codex, `OC` OpenCode, `CU` Cursor (coding tools), `AI` claude.ai, `GPT` ChatGPT (chat exports).

| Key | Action |
|---|---|
| `space` | share or un-share (the marked rows, or this one) |
| `v`, `a`, `A` | range select, mark all, clear marks |
| `d` | block |
| `e` | choose which tools of this project are sent |
| `n` | put in a named project (type a new name to create one) |
| `N` | rename a project (an existing name merges them) |
| `u` | undo the last change |
| `f` | view: All, Shared, To send, Not shared |
| `t` | folder tree: `space` on a folder shares everything below it |
| `/` | search, `o` sort |
| `p` or `enter` | preview: the exact payload, redactions marked |
| `P` | paranoid preview: raw payloads, every string |

Sharing changes your selection only. Nothing is sent from this screen.

**Check**
- [ ] The card for an unshared project says nothing from it leaves your machine.
- [ ] `p` on a shared project shows summaries, and any secret appears as `[REDACTED:…]`.
- [ ] `n` with a new name ("Test project") groups two folders; the card names the project.
- [ ] `u` undoes it.

### 2.4 Send (`2` or `s`)

Two steps. **Review** starts at once: every session is read and redacted exactly as it would be sent, and the screen says "nothing has left this machine yet". It lists each project with its session count, the number of redactions, and how many sessions in unshared projects stay put. **Send** (`enter`) sends exactly what was reviewed and approves those sessions. `r` reviews again, `esc` leaves without sending.

**Check**
- [ ] The count on the Send button matches the review.
- [ ] After sending, the projects show `✓ sent`.
- [ ] Sending again right away sends nothing ("unchanged").

### 2.5 Inbox (`i`)

New sessions of a shared project wait here for your approval; a background send never sends them on its own. Sections: waiting, excluded, sent or queued, retracted.

| Key | Action |
|---|---|
| `a` | approve: goes out with the next send |
| `x` | exclude: stays on this computer for good |
| `e` | edit the summary in your editor; your version is what is sent |
| `u` | undo a decision |
| `R` | retract: delete a sent session from the server |
| `o` | answer an offer (see 5.3) |
| `c` | receipts: what was sent automatically |

**Check**
- [ ] Start a short new session in Claude Code inside a shared project, then press `r`: it appears as pending.
- [ ] `e`, change the title, save: the row says edited, and the preview shows your title.
- [ ] `R` on a sent session asks first, then shows it under Retracted.

### 2.6 Server (`3`)

Whether the server is reachable, whether the upload token is accepted, and every project the server holds with its session count. `D` deletes the highlighted project from the server (it asks first; your files are not touched).

### 2.7 Settings (`4`)

Saved as you change them: theme (with a live preview), share level, look-back window, sources on or off, extra redaction patterns, commit verification with local git, and the summarizer (local rules by default; a model only if you turn it on, and it asks first). `↑ ↓` select, `enter` change.

> The "Session hook / schedule" row still says "not available yet". The commands exist (section 5.4).

### 2.8 Agents (`5`)

Asks for the admin secret once per run and keeps it in memory only. A grid of projects by assistants:

| Mark | Meaning |
|---|---|
| `[x]` | this assistant sees this project |
| `[*]` | sees it through "Every project" |
| `+` / `-` | a change you made that is not applied yet |

`space` ticks or unticks, `enter` shows your changes for review and applies them only when you confirm, `v` shows exactly what the highlighted assistant can read now, `d` lists its tokens (`R` revokes one), `u` clears your pending changes.

The assistant made during setup is called **Default** and sees every project you share.

---

## 3. The browser UI

The same product in a browser, on the same settings and state as the TUI. Changes in one show up in the other after a refresh.

```bash
goldengate ui
```

It opens your browser on `http://127.0.0.1:<port>/launch#…`. The link works once and expires after two minutes; press `Enter` in that terminal for a new one. Reloading the page ends its session on purpose; use a fresh link. The UI stops with Ctrl-C. Add `--no-open` to only print the link, or `--port 8090` for a fixed port.

Top bar: **Projects · Inbox · Send · Server · Agents · Settings · Setup**, the server you are connected to, and a Dark/Light switch.

| Screen | What to do there |
|---|---|
| **Setup** | Tools found; connect with the admin secret or a pairing code. The assistant token is shown once with a copy button. (Already done in 2.1; you can skip it.) |
| **Projects** | `●` shared, `○` not shared, `◐` partly, `✕` blocked. Views All / Shared / To send / Not shared, search, "Show as folders" (share a whole folder). Click a project: the card says in words what happens to it; Share, Block, Preview, tools, put in a named project. Double-click to preview. Undo. |
| **Preview** | The exact payload per session, redactions highlighted, and what changed since the last send. |
| **Send** | Step 1 Review (nothing leaves) then Step 2 Send, with progress per project. A failure offers review and retry; what already arrived is not sent again. |
| **Inbox** | Offers first. Then sessions waiting, grouped by project, with "Approve all"; per session Approve, Exclude, Edit summary, Undo, Retract (asks first). Excluded, Sent or queued, Retracted below. A link to Receipts. |
| **Receipts** | Each automatic send, the rule that sent each session, Retract per session. |
| **Server** | Reachability, upload token, what the server holds, delete a project there. |
| **Agents** | Asks for the admin secret (held by the local UI process for this run only, never shown again). Grid of projects by assistants: tick to stage, a review card lists the changes in words, nothing applies until **Apply**. Per assistant: tokens with Revoke, and "View as". "Forget the admin secret" when you are done. |
| **Settings** | Share level, look-back, tools, redaction patterns, git metadata, summarizer (with a write-only API key and a test call). |

**Check**
- [ ] Sharing a project in the browser shows `[x]` in the TUI after `r`.
- [ ] Inbox → Edit summary: change a line, Save my version; the row says "edited by you".
- [ ] Agents: a wrong admin secret is refused; the right one shows the grid.
- [ ] Tick a box: a review card appears and nothing changes until Apply.
- [ ] The page works at phone width (narrow the window): no sideways scrolling.
- [ ] Reload the page: it asks for a fresh link.

---

## 4. Connect an assistant and ask it

Use the assistant token from setup (2.1). Lost it? Make a new one: `goldengate token rotate assistant`.

### Claude Code

```bash
claude mcp add --transport http goldengate http://127.0.0.1:8080/mcp \
  --header "Authorization: Bearer $(cat ~/gg-test/assistant-token.txt)"
```

Start `claude` in any folder and ask, for example:

- "Use goldengate: what have I been working on this week?"
- "Where does *Test project* stand, and what should I pick up next?"
- "Which pull requests did I open in the last few days?"
- "Search my work for OAuth."

Remove it later with `claude mcp remove goldengate`.

### Other assistants

Any assistant that can call an MCP server over HTTP with a header: URL `http://127.0.0.1:8080/mcp`, header `Authorization: Bearer <assistant token>`. Hosted assistants (ChatGPT, Gemini, claude.ai) cannot reach `127.0.0.1`; they need the server deployed behind HTTPS (README quickstart; OAuth with `--oauth`).

### Check without an assistant

```bash
T="$(cat ~/gg-test/assistant-token.txt)"
curl -s http://127.0.0.1:8080/mcp -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}'
```

The tools an assistant gets, all read-only: `recent_activity`, `list_projects`, `get_project_context`, `get_work_item`, `search_work`, `get_session`, `list_commits`, `list_pull_requests`, `list_issues`, `list_artifacts`. Every answer is wrapped in a notice that the text is past session data, not instructions.

**Check**
- [ ] The assistant lists only projects you shared.
- [ ] Untick a project for Default in Agents (TUI or browser): the assistant stops seeing it on its next request.
- [ ] Retract a session: the assistant no longer finds it.
- [ ] Revoke the token in Agents: the assistant gets "unauthorized".

### A second assistant with its own projects

```bash
export GOLDENGATE_ADMIN_TOKEN="$(cat ~/gg-test/admin-secret.txt)"
goldengate agents add "Second"
goldengate agents grant Second "Test project"
goldengate agents token Second          # shown once
unset GOLDENGATE_ADMIN_TOKEN
```

It sees only "Test project". Compare with "View as Second" in Agents.

---

## 5. Let it run with less clicking

### 5.1 Trust levels and rules

Each project has a trust level:

| Level | What happens to new sessions |
|---|---|
| **ask** (default) | wait in the inbox |
| **suggest** | your rules mark them in the inbox; one command approves the marked ones |
| **auto** | sessions your rules approve go out with the next send; with no approve rule, every session of the project |

```bash
goldengate projects trust "Test project" auto
goldengate rules add "Test project" --source claude-code      # approve Claude Code sessions
goldengate rules add "Test project" --exclude --source codex  # keep Codex sessions local
goldengate rules list
goldengate sync
```

A rule only acts inside a project you already shared, and a block always wins.

### 5.2 Receipts

Everything sent without asking gets a receipt saying which rule sent it.

```bash
goldengate receipts
goldengate receipts show                 # the latest; --agents adds who can read each project
goldengate receipts retract <receipt-id> --all
```

TUI: `c` in the inbox. Browser: Inbox → Receipts.

### 5.3 Offers and suggestions

- **Rule offers**: after you send 5 or more sessions from one folder by hand within 30 days, the inbox offers the rule that would have done it. `goldengate rules accept <offer-id>` or `dismiss`; TUI `o`; browser Yes / Not now.
- **Project suggestions**: a folder with sessions in no project that looks like part of one (a worktree of a shared repository, for example) is suggested. `goldengate projects suggest`, `accept`, `dismiss`.

### 5.4 Send in the background

```bash
goldengate install-schedule --print      # look first: what it would install
goldengate install-schedule              # every 30 minutes (systemd on Linux)
goldengate install-hook claude-code      # also when a Claude Code session ends
```

Both run `goldengate sync --auto`, which sends only what you approved or your rules approved, never what is still waiting. Remove them with `goldengate uninstall-schedule` and `goldengate uninstall-hook claude-code`.

Background runs are safe to repeat often:

- **A session still being written waits.** A new or changed session is sent once it has been left alone for 10 minutes, so it is not summarized and sent again every few minutes. Change it with `settle_minutes` under `[sync]` in the config (0 sends at once). The Claude Code hook runs when a session has ended, so it does not wait (`--settle 0`).
- **One send at a time.** If a send is already running, a background run does nothing; pressing Send waits a few seconds, then says so.
- **An unreachable server is left alone for a while.** The run says so in one line and ends without an error; the next tries come after 1, 2, 4 … minutes, at most an hour apart, and the first success resets that.

**Check**
- [ ] At trust auto with a rule, a new session is sent by `sync` and listed in a receipt.
- [ ] At trust ask, `sync --auto` leaves new sessions in the inbox.
- [ ] A session changed a minute ago is reported as waiting to settle, and sent by a run 10 minutes later.

---

## 6. Chats from claude.ai and ChatGPT

Export your data from claude.ai or ChatGPT (Settings → Export data), then:

```bash
goldengate import-claude-ai ~/Downloads/claude-export.zip
goldengate import-chatgpt ~/Downloads/chatgpt-export.zip
goldengate chats                                   # conversations in no project (shown here only)
goldengate chats share <conversation-id> --project "Test project"
```

Only the conversations you name are shared; the rest of the export is not read when you send. Importing a newer export adds only new and continued conversations (`goldengate imports list`).

---

## 7. Command cheat sheet

| Task | Command |
|---|---|
| Open the TUI | `goldengate` |
| Open the browser UI | `goldengate ui` |
| Health check (local) | `goldengate doctor` |
| Share a folder | `goldengate share add ~/code/app` |
| Named projects | `goldengate projects create|add|list|rename|merge|remove|delete` |
| Look before sending | `goldengate sync --dry-run --approve` |
| Approve and send everything waiting | `goldengate sync --approve` |
| Send what is approved | `goldengate sync` |
| Inbox | `goldengate inbox`, `inbox approve --all`, `inbox exclude <id>`, `inbox reset <id>` |
| Single sessions | `goldengate sessions list|include|exclude|retract|edit <id>` |
| Stop sharing and delete from the server | `goldengate share remove ~/code/app --delete-from-server` |
| New assistant token | `goldengate token rotate assistant` |
| Assistants and grants | `goldengate agents list|add|grant|revoke|token|limit|remove` |
| Summarizer | `goldengate summarizer use heuristic|anthropic|openai|gemini|opencode|ollama|custom`, `summarizer test` |

`goldengate <command> --help` explains any of them.

---

## 8. A short test plan

Tick these off in order; each line points to its section.

1. [ ] Both programs install and print a version (0).
2. [ ] `doctor` finds your tools (0).
3. [ ] Server answers `/healthz` (1).
4. [ ] TUI setup connects and shows the assistant token once (2.1).
5. [ ] Preview shows redactions; nothing is sent from Projects (2.3).
6. [ ] Review then Send; a second send sends nothing (2.4).
7. [ ] A new session waits in the inbox; approve, exclude, edit, retract (2.5).
8. [ ] The browser UI shows the same state; edit and approve there (3).
9. [ ] An assistant reads only shared projects (4).
10. [ ] Grant and revoke change what it reads on the next request (2.8, 4).
11. [ ] Trust auto plus a rule sends on its own, with a receipt (5).
12. [ ] Retracting from a receipt removes the session from the server (5.2).
13. [ ] Background send leaves waiting sessions alone (5.4).

---

## 9. Troubleshooting

| Symptom | What to do |
|---|---|
| `goldengate: command not found` | `export PATH="$HOME/go/bin:$PATH"` |
| Setup says the server is unreachable | Is `goldengatebridge serve` still running? Is the URL `http://127.0.0.1:8080`? |
| "the server refused the admin token" | Paste the exact contents of `admin-secret.txt`, and make sure the server was started with the same value. |
| The browser says "This page needs a fresh link" | Press `Enter` in the `goldengate ui` terminal, or run it again. Links work once, for two minutes. |
| A project shows `▲ to send` but Send has nothing | Its sessions wait in the inbox: approve them (`i`), or send with approval from the Send screen. |
| The assistant sees nothing | Did you send? Does its agent see the project (Agents)? Is the token current? |
| A port is taken | Use another: `--addr 127.0.0.1:8081` for the server, `--port 8091` for the UI. |

---

## 10. Clean up after testing

1. Stop `goldengate ui` and the server (Ctrl-C in their terminals).
2. Remove what you installed: `goldengate uninstall-schedule`, `goldengate uninstall-hook claude-code`, `claude mcp remove goldengate`.
3. Remove the upload token from the OS keyring:
   ```bash
   secret-tool clear service goldengate username ingest-token@127.0.0.1:8080
   ```
   (`secret-tool` comes with `libsecret-tools`; the keyring app "Passwords and Keys" works too: look for `goldengate`.)
4. Delete the test server's data: `rm -r ~/gg-test`
5. Put your earlier setup back, if you moved one aside:
   ```bash
   rm -r ~/.config/goldengate ~/.local/state/goldengate
   mv ~/.config/goldengate.before-test ~/.config/goldengate 2>/dev/null
   mv ~/.local/state/goldengate.before-test ~/.local/state/goldengate 2>/dev/null
   ```

Your coding tools' history was only read and is unchanged.
