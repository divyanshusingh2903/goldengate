# Golden Gate reference

The full install, quickstart, command and screen reference. The short version is in the [README](../README.md); the guided walkthrough is [GUIDE.md](GUIDE.md); the hosted docs are at <https://divyanshusingh.site/goldengate/docs>.

## Install

The server is a versioned Docker image, `ghcr.io/divyanshusingh2903/goldengatebridge` (pin `:1.3.0`, or `:1.3` / `:1` to follow patch / minor releases, or `:latest`). The laptop app is one Go program (Go 1.26 or later):

```bash
go install github.com/divyanshusingh2903/goldengate/cmd/goldengate@v1.3.0
```

## Quickstart (about ten minutes)

1. **Run the server** behind HTTPS. Generate the admin secret once and keep it:

   ```bash
   export GOLDENGATE_ADMIN_TOKEN="$(openssl rand -base64 32)"
   goldengatebridge serve --addr :8080 --behind-tls-proxy --db /data/bridge.db
   ```

   Or run the published image, which needs no Dockerfile of your own. It listens on `$PORT` (default 8080) and keeps its database in `/data`, so mount a volume there:

   ```bash
   docker run -d --name bridge -p 8080:8080 -v goldengate-data:/data \
     -e GOLDENGATE_ADMIN_TOKEN="$(openssl rand -base64 32)" \
     ghcr.io/divyanshusingh2903/goldengatebridge:latest
   ```

   Images are versioned: pin `:1.3.0` (or `:1.3`) for a server you do not want to change under you; `:latest` follows the newest release and `:edge` follows `main`. It is built by `.github/workflows/release-image.yml`: pushing a tag `vX.Y.Z` publishes that version for amd64 and arm64. Run one container per database. When a release adds a source (1.3.0 added Antigravity), update the server before the laptops: an older server refuses a batch that holds a session from a source it does not know.

2. **Connect your laptop.** Paste the admin secret when asked (it is never written to disk):

   ```bash
   goldengate init --server https://bridge.example.com
   ```

   This stores an upload token (what this computer sends with) in your OS keyring and prints an assistant token (what your assistant reads with) **once**. On a server where you have a shell you can pair instead: run `goldengatebridge pair` there and `goldengate init --server URL --pair-code CODE` here.

3. **Choose what to share, preview, send:**

   ```bash
   goldengate share add ~/code/myproject
   goldengate sync --dry-run --approve    # exactly what would be sent
   goldengate sync --approve              # approve what you just looked at, and send it
   ```

   Sessions of a project you share wait for your approval; `--approve` approves and sends what is waiting. Later sessions wait again (see the inbox below), unless the project's trust level is auto (share with `--auto-approve`, or see rules and trust levels below).

4. **Connect the assistant.**
   - If its connector lets you set a header: use `https://bridge.example.com/mcp` and send the assistant token as `Authorization: Bearer <token>`.
   - If it only supports OAuth (Gemini's custom apps do; it supports dynamic client registration): start the server with `--oauth --public-url https://bridge.example.com` (on a host where you cannot change the start command, such as Railway, set the variables `GOLDENGATE_OAUTH=true` and `GOLDENGATE_PUBLIC_URL=https://bridge.example.com` instead), then add `https://bridge.example.com/mcp` as the connector URL (Gemini: Settings > Connected Apps > Add a custom app). A consent page opens; approve it with your admin secret. No token to paste. If the assistant asks for a client ID and secret instead, run `goldengatebridge oauth clients create --redirect-uri <the address it shows>`.
   - Revoke a connected assistant with `goldengatebridge oauth clients delete <id>`.

Un-share with `goldengate share remove ~/code/myproject --delete-from-server`. Replace the assistant's token with `goldengate token rotate assistant`.

## The interactive UI

Prefer a browser? `goldengate ui` opens the same product in your browser: an overview, setup, projects, preview, send, server and settings, plus the inbox (approve or exclude one session or several, edit a summary, retract, answer offers), the receipts of what was sent automatically, and agents (who sees which project, view as an agent, revoke a token). It runs on this computer only, for as long as the command runs.

Run `goldengate` in a terminal. The first run starts a short setup (find your tools, connect to your server, choose the share level, pick projects, review, first send); after that it opens the project picker.

| Screen | What it does |
|---|---|
| Projects (`1`) | Every project folder, with where it stands: `▲ 3 to send`, `✓ sent 2h`, `not shared`, `✕ blocked`. Views: All / Shared / To send / Not shared (`f`). `space` share or un-share · `v` range · `a`/`A` mark all/none · `d` deny · `n` put in a project · `N` rename or merge a project · `u` undo · `e` choose which tools are sent · `/` search · `t` folder tree (space on a folder shares everything below it) · `o` sort · `p` preview · `P` paranoid (raw payloads) |
| Send (`2`, or `s`) | Two steps. **Review** runs at once: every session is read and redacted exactly as it would be sent, and nothing leaves the machine. **Send** (`enter`) sends exactly what was reviewed. Progress follows each session; a failed project can be retried and skips what already arrived. |
| Server (`3`) | Health, token check, what the server holds (code or chat), `D` delete a project from it |
| Settings (`4`) | Theme, share level, look-back window, sources, redaction patterns, commit verification |
| Agents (`5`) | A grid of projects by assistants: `space` (or a click) ticks which assistant sees which project, `enter` shows the changes for review before anything is applied. `v` shows exactly what the highlighted assistant can read right now; `d` its tokens, with `R` to revoke one. Asks for the admin secret once per run and keeps it in memory only. |

The card beside the list says in words what happens to the highlighted project: "Not shared: nothing from this project leaves your machine", "Shared · never sent", "Shared · up to date". Every project and session carries the tag of the tool it came from: `CC` Claude Code, `CX` Codex, `OC` OpenCode (coding sessions), `AI` claude.ai, `GPT` ChatGPT (chat exports). The web UI (`goldengate ui`) shows each tool's logo instead, with its name on hover.

The preview is the exact outgoing payload, redactions marked, with what changed since it was last sent. `?` shows every key. It works at 80x24 and follows resizes.

**Mouse.** Click a row to select it, click its `[ ]` box to share it, double-click to preview, ctrl+click to mark. Tabs, view chips, buttons, settings and the keys in the bottom bar are all clickable, and the wheel scrolls.

**Themes.** Golden Gate Light (default) and Dark, plus GitHub, Gruvbox, Catppuccin and Solarized (light and dark), Dracula, Monokai, Nord and Tokyo Night. Pick one under Settings → Theme (a dropdown that previews as you move); it is saved as `[ui] theme` in the config. `GOLDENGATE_THEME=gruvbox-dark` overrides it for one run. While the UI runs it sets the terminal's background to the theme's and restores yours on exit. `NO_COLOR` turns color off; nothing relies on color alone.

**Summaries.** Each session is summarized on your machine before anything is sent: a title, goals, what was done, outcomes (tests, commits, PRs), the first and last message, files touched and command names. Raw transcripts, tool output and file contents are never sent. By default fixed rules write the summary. For better summaries of sessions that cover several tasks, you can opt in to a small model; it then receives the redacted conversation of shared projects only.

A summary has two parts. The **facts** (files, commands, test and commit outcomes, work items) always come from the local rules. The **narrative** is prose: with a model on, it is a few paragraphs on what was attempted, what was decided and why, what was rejected and what is left open. Choose its length under Settings → Summarizer → Narrative: short (one paragraph), standard, or detailed (up to about 4000 characters). The preview shows the whole narrative exactly as it will be sent. Assistants see a one or two sentence abstract in lists and the full narrative only when they open one session. See [SECURITY.md](../SECURITY.md).

Everything is in the TUI: **Settings → Summarizer** opens a dropdown (it asks before turning a model on), then the rows below it set the model, the endpoint, the API key (typed hidden, stored in the OS keyring) and run a test call.

| Choice | Default model | Key |
|---|---|---|
| Local heuristic (default) | none | none |
| Anthropic API (Claude) | `claude-haiku-5-5` | `ANTHROPIC_API_KEY` or keyring |
| OpenAI API (ChatGPT) | `gpt-5.4-nano` | `OPENAI_API_KEY` or keyring |
| Google Gemini API | `gemini-3.5-flash-lite` | `GEMINI_API_KEY` or keyring |
| OpenCode Zen API | `glm-5.3-flash` | `OPENCODE_API_KEY` or keyring |
| Custom, any OpenAI-compatible endpoint (Ollama, LM Studio, OpenRouter, …) | `llama3.2` at `http://localhost:11434/v1` | optional: `GOLDENGATE_SUMMARIZER_KEY` or keyring |

Model names change often: every default can be overridden, and "Test connection" tells you at once whether it works. The same from the command line:

```bash
goldengate summarizer use gemini --model gemini-3.5-flash-lite
goldengate summarizer use gemini --length detailed   # longer narratives; short | standard | detailed
goldengate summarizer set-key gemini
goldengate summarizer test                    # one tiny sample call (not your data)
goldengate summarizer use custom --base-url http://localhost:1234/v1 --model qwen3
goldengate summarizer use heuristic           # back to local rules
goldengate sync --resummarize                 # also upgrade sessions already sent with rule-based summaries
```

A session is summarized once per content: answers are cached, and an unchanged session is not sent to the model again.

Turning a model on only affects new and changed sessions. Sessions sent earlier keep their rule-based summaries until you ask: `goldengate sync --resummarize` (or, on the Send screen, the "N sent sessions have rule-based summaries" line and its **Include them in this sync** button) rebuilds them and sends their redacted conversations to the model. Only sessions whose summary actually changes are pushed again. Changing the narrative length works the same way: it applies to new and changed sessions, and synced ones are rebuilt only when you ask.

Everything the UI does is also available as a command.

The tools sent for one project can be chosen with `e`: on a shared project, untick a tool to stop sending it; on one that is not shared yet, ticking a tool shares the project with only that tool. That is stored as `exclude_sources` on the folder in the config.

**Named projects.** What you share is grouped into named projects: one project can hold several folders and chat groupings, and the card names the project a folder is in. Press `n` to put the marked folders in a project (typing a new name creates it), and `N` to rename one (typing an existing name merges them). Putting a folder in a project shares it, and a block (`goldengate share deny`) still wins. From the command line:

```bash
goldengate projects create "Pharmfindr" ~/code/pharmfindr ~/code/pharmfindr-web
goldengate projects add Pharmfindr 'claude-ai:Pharmfindr'
goldengate projects list
goldengate projects rename Pharmfindr "Pharmfindr app"
goldengate projects merge "Old name" "Pharmfindr app"
goldengate projects remove "Pharmfindr app" ~/code/pharmfindr-web   # stops sharing it
goldengate projects delete "Pharmfindr app"                          # stops sharing every member
```

**The inbox: nothing new leaves without your approval.** When you share a project, its sessions wait until you approve them. In the terminal UI that is the Send screen: Review shows every session exactly as it would be sent, and Send approves what you reviewed. From the command line, `goldengate sync` (and a hook or timer running it) sends only what is approved:

```bash
goldengate inbox                        # what is waiting, and what you excluded
goldengate sync --dry-run --approve     # look first: exactly what approving would send
goldengate inbox approve --all          # or name session ids
goldengate inbox exclude <session-id>   # keep one on this machine for good
goldengate inbox reset <session-id>     # undo a decision
goldengate sync                         # sends what is approved
goldengate sync --approve               # approve and send everything waiting, in one step
```

To decide one session at a time, wherever its project stands, use `sessions` (or press `i` in the terminal UI for the same list: `a` approve, `x` exclude, `u` undo, `R` retract):

```bash
goldengate sessions list                  # every session of your shared projects and its state
goldengate sessions include <session-id>  # approve it; also undoes an exclude or a retract
goldengate sessions exclude <session-id>  # never send it
goldengate sessions retract <session-id>  # delete a sent session from the server, and never send it again
```

**Editing a summary.** `goldengate sessions edit <session-id>` (or `e` in the inbox) opens the session's summary in your editor: change the text, delete a line to strike it, delete a work-item line to unlink it. Your version is what is sent. It is redacted like everything else, the preview shows it exactly, and no summarizer replaces it, `sync --resummarize` included. If the session continues afterwards, the new summary is not sent in place of yours and your edit is not applied blindly: the session is held until you edit it again (the form shows the new summary next to your earlier version), keep your version (`--keep`, or `K`), or drop the edit (`--discard`, or `D`).

A session that continues after you approved or sent it comes back to the inbox. Projects you were already sharing before this existed keep sending as they did.

**Rules and trust levels.** Each project has a trust level, and you can write rules for it:

```bash
goldengate projects trust "My app" auto            # ask (default) | suggest | auto
goldengate rules add "My app" --branch 'release/*' # approve sessions on release branches
goldengate rules add "My app" --exclude --source codex
goldengate rules list
goldengate inbox approve --suggested               # at "suggest": approve what your rules marked
```

- **ask**: everything waits in the inbox.
- **suggest**: sessions your rules approve are marked in the inbox; one command approves them. Nothing is sent before that.
- **auto**: sessions your rules approve go out the next time you send. With no approve rule, that is every session of the project (`goldengate share add <folder> --auto-approve` sets this up in one step).

Whatever auto sends is listed in a receipt, with the rule that sent it. `goldengate receipts` lists them, `goldengate receipts show` prints the latest (add `--agents` to see which assistants can read each project), and `goldengate receipts retract <receipt-id> --all` deletes those sessions from the server again. In the TUI, press `c` in the inbox.

**Offers.** If you send five or more sessions from the same folder by hand within 30 days, the inbox offers the rule that would have done it for you ("Share sessions from ~/code/app automatically?"), with the exact rule and what saying yes changes. `goldengate rules accept <offer-id>` says yes, `goldengate rules dismiss <offer-id>` says not now and the offer is not made again. Nothing changes without a yes. In the TUI inbox, press `o`.

**Project suggestions.** If a folder with sessions is in no project but looks like it belongs to one (a worktree of a shared repository, the same repository remote, or a branch named after one of the project's issues), the inbox suggests the project and says why. `goldengate projects accept <id>` adds the folder in one step and its sessions then wait in the inbox; `goldengate projects dismiss <id>` says not now. Comparing remotes and branches needs `git = true` under `[enrich]` in the config, which lets Golden Gate read each folder's git `HEAD` and `config` files; sessions of folders you have not shared are never read.

**Chats, one conversation at a time.** After importing a claude.ai or ChatGPT export, share the conversations you want rather than a whole grouping:

```bash
goldengate chats                                   # imported conversations that are in no project (shown here only)
goldengate chats --grep invoice                    # narrow by title
goldengate chats share <conversation-id> --project "Job search"
goldengate chats move <conversation-id> "Other project"
goldengate chats unshare <conversation-id>
```

Only the conversations you name are shared; the rest of the export stays on your machine and is not read when you send. When you export again later, import the new zip the same way: only new and continued conversations are added, and `goldengate imports list` shows what each export brought in. A shared conversation then waits in the inbox like any session. A project you made in claude.ai or ChatGPT is suggested as a Golden Gate project in the inbox. To share a whole grouping instead, add it as a member: `goldengate projects add <project> 'claude-ai:Chats'`.

**Send in the background.** To send without thinking about it, let the system run the background send:

```bash
goldengate install-schedule              # every 30 minutes (systemd, launchd or Task Scheduler); --every 15m to change
goldengate install-hook claude-code      # also right when a Claude Code session ends
goldengate install-schedule --print      # look first: shows the files and commands, changes nothing
goldengate uninstall-schedule            # and uninstall-hook claude-code
```

Both run `goldengate sync --auto`. It sends only what you approved in the inbox or what your rules approve, never what is still waiting, and everything it sends is in a receipt. With every project at trust level ask, the background sync only delivers what you approve by hand.

**The daemon (#189).** `goldengate install-service` runs `goldengate daemon` as a user service (a systemd user service with `Restart=on-failure`, a launchd agent with `KeepAlive`, a Task Scheduler task at logon); `uninstall-service` removes it; both take `--print`. Installing replaces the timer of `install-schedule`. The setting `[sync] daemon` (`""` not chosen, `on`, `off`) and the service are changed together by one function, and the setting is written only after the service was installed or removed, so a machine with no systemd user session reports an error and stays as it was. Every few minutes (`--interval`) the daemon lists the shared sessions, which it does not read, and runs `sync --auto` only when something changed since its last send: the sessions (source, id, size and last change), the config file, the newest inbox decision, a session it held back until it settles, or a failed send whose wait is over. A session waiting in the inbox is not a reason, so a project at trust ask does not make it run every time. It reads the config afresh at every look and stops by itself when `[sync] daemon` is `off`. `daemon status` and the TUI and web UI read `daemon.json` in the state directory (last send, last error, next look, whether the server answered); while the server is being left alone the next look shows the end of the wait. Only one daemon runs (a lock on `daemon.lock`). `goldengate daemon --check` reads the upload token (not only the keyring) and asks the server; `install-service` runs it on Linux in a transient unit of the user's systemd (`systemd-run --user --wait --pipe`) and prints how to fix a token the service cannot see (`systemctl --user import-environment DBUS_SESSION_BUS_ADDRESS`, or `goldengate init` where there is no keyring, which keeps the token in `secrets.json`). The question "Send automatically in the background?" is asked once when a project goes to trust auto, a rule offer is accepted, a folder is shared with `--auto-approve`, or after `init`; where nobody can answer it names the command instead.

A background send waits until a new or changed session has been left alone for 10 minutes (`settle_minutes` under `[sync]`, 0 for no wait; `--settle 5m` for one run), so a session still being written is not sent again and again; the Claude Code hook uses `--settle 0`, since its session has ended. A session already on the server is sent again only when 3 hours have passed since its last send (`resend_minutes` under `[sync]`, 0 for no gap; `--resend-gap 1h` for one run; the hook uses `--resend-gap 0`). Only one send runs at a time. When the server cannot be reached, the run says so in one line and later runs wait 1, 2, 4 … minutes (at most an hour) before trying it again.

A rule matches on facts your tools recorded: the tool, the folder or chat grouping, the repository remote, the branch, and issue or pull request keys (`--work-item linear:ENG`). It acts only inside the project: it cannot share anything you did not share, and a block always wins. An exclude rule keeps its matches on your machine. Even at auto, the first session from a folder or chat you did not name yourself (one reached through a parent folder or a pattern such as `chatgpt:*`) waits for you; after you send one, the rest follow automatically.

**Which assistant sees what.** Each assistant you connect is an *agent* with its own assistant token and its own list of projects. The assistant you set up first sees every project you share; any other starts with nothing. These commands ask for the admin secret (or read `GOLDENGATE_ADMIN_TOKEN`) and never store it:

```bash
goldengate agents list                               # agents and what each sees
goldengate agents add "ChatGPT"                      # a new agent; sees nothing yet
goldengate agents grant ChatGPT "Pharmfindr"         # by project name, id, or folder (send it first)
goldengate agents token ChatGPT                      # its assistant token, shown once
goldengate agents revoke ChatGPT "Pharmfindr"
goldengate agents grant ChatGPT --all                # every project, including future ones
goldengate agents revoke Default --all               # stop the first assistant seeing everything
goldengate agents limit ChatGPT 30                   # at most the last 30 days
goldengate agents remove ChatGPT                     # also revokes its tokens
```

The Agents screen does the same in `goldengate` (press `N` on an agent's tokens for a new one) and in `goldengate ui` (New token on each agent); either shows the token once.

An assistant that connects through OAuth is asked about on the consent page instead: after the admin secret, you choose which agent it is and tick the projects it may see (nothing is ticked for you).

Grants and revocations apply to the assistant's next request. A project an assistant may not see is, for that assistant, simply not there.

`goldengate share add <folder>` still works and puts the folder in a project of its own, named after it. Configs that use `[share] allow` are read as projects and written back in the new shape on the next save.

## Sources

| Source | How it is read |
|---|---|
| Claude Code | `~/.claude/projects/**.jsonl` (honors `CLAUDE_CONFIG_DIR`) |
| Codex CLI / Desktop | `~/.codex/sessions/**/rollout-*.jsonl` (honors `CODEX_HOME`) |
| OpenCode | its SQLite database, read-only (`$XDG_DATA_HOME/opencode/opencode.db`) |
| Cursor | its state database, read-only (`~/.config/Cursor/User/globalStorage/state.vscdb` on Linux); tag `CU`. Commits and pull requests from Cursor show as mentioned only, because Cursor records no exit code for commands |
| Antigravity | the IDE (`~/.gemini/antigravity`) and the `agy` command-line tool (`~/.gemini/antigravity-cli`), read-only: the conversation list, each conversation's transcript, and its branch; tag `AG`. Older conversations kept in an encrypted format are not read (`goldengate doctor` counts them). A config that lists its tools must have `antigravity` switched on under Tools. |
| claude.ai | `goldengate import-claude-ai export.zip` |
| ChatGPT | `goldengate import-chatgpt export.zip` |

The same folder in several tools is one project. Worktrees are filed under their repository when it is selected. Chat exports become groupings like `claude-ai:Chats`. Nothing of them is shared until you share single conversations (`goldengate chats`) or a whole grouping (`goldengate share add 'claude-ai:Chats'`; globs work: `chatgpt:*`). Exports stay where you keep them; `goldengate imports list|remove` manages them.

