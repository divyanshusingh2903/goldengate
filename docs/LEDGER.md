# Product v2 work ledger

This file lets any Claude Code session (or person) pick up the Product v2 work where the last one stopped. **Read it before starting work, and update it as part of every issue.**

- Plan and reasons: `DESIGN.md` section 1.1 (product principles) and the GitHub [Product v2 milestone](https://github.com/divyanshusingh2903/goldengate/milestone/1).
- Each issue's body is its spec: scope, rules, and "Done when". Read it in full with `gh issue view <n>` before writing code.
- Target: every issue in the queue merged by **2026-11-30**.
- Roadmap page (Claude artifact, private to the owner): https://claude.ai/artifact/8LFj2xzfz1Vm6vKamiBS3p. It shows the same order day by day, with progress. Keep it in step with this file (loop step 11).

## Status

_Last updated 2026-11-14. Update this section whenever a row in the queue changes._

**Progress:** 3 of 31 scheduled issues done (plus 2 stretch). Phase 1, Foundations, is under way.
**In progress:** #49 Summaries: facts plus narrative, size M, planned Nov 15. **Next after it:** #19 Server: stable project IDs, migration.

### Done

| Issue | What changed | PR | Merged |
|---|---|---|---|
| #17 | DESIGN.md 1.1 product principles, 1.2 glossary, revised non-goals, M7 milestone | #50 | 2026-11-14 |
| #37 | MCP time-bounded tools default to the last 5 days; `since`/`until` on every one; results state their window; store queries gain `Until` | #55 | 2026-11-14 |
| #18 | Named projects: `[[projects]]` with members in the config (membership shares, deny still wins, one folder per project), legacy allow rules read as projects, `goldengate projects` commands, TUI `n`/`N` | #59 | 2026-11-14 |

Supporting changes (not queue issues):

| What | PR | Merged |
|---|---|---|
| Go toolchain go1.26.11 (GO-2026-6859 broke `security` on every branch) | #51 | 2026-11-14 |
| First `pr-template` check (unblocked the `main` ruleset); later replaced | #52 | 2026-11-14 |
| The user's stricter `pr-template` check (Checklist with every item ticked; skips Dependabot) | #9 | 2026-11-14 |
| This ledger, then its checklist rule, status section and roadmap sync | #54, #56, #57, #58 | 2026-11-14 |

### Open loose ends

- **User's stash** `list-tools wip (user)` (branch `fix/list-tools-empty-hint`): not touched by the loop. Popping it will conflict with #37 in `listWork` (`bridgemcp/tools.go`).
- **Dependabot #53**: shows a `pr-template` failure from before #9 merged. It clears once the PR is rebased or updated (the user's call).
- **#49 follow-ups**: the user's own narrative is honoured through `summarize.Options.UserNarrative`, but nothing stores or edits one until #30. Model summaries pushed before #49 (bullets in `description`) are not counted as stale; they gain a narrative when their session changes or the length setting is changed and the user re-summarizes. `get_work_item` (#36) should return the full narrative like `get_session`.
- **#37 follow-up**: the per-agent maximum window is enforced in `resolveWindow` once agents exist (#24).

## The loop

Work one issue at a time, in queue order. For each issue:

1. **Sync main.**
   ```bash
   git switch main && git pull --ff-only origin main
   ```
2. **Pick the next issue:** the first row in the queue whose status is not `done`. If its "after" issues are not all `done`, stop and report rather than skipping ahead (unless the row is marked as safe to reorder).
3. **Branch** from the fresh main: `<type>/<issue>-<slug>`, e.g. `feat/37-five-day-window`, `docs/17-product-principles`. Types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`.
4. **Update this ledger first:** in the branch's first commit, set the previous issue's row to `done` (with its PR) if it is not already, and this issue's row to `in progress`.
5. **Implement** the issue against its "Done when" list. Keep DESIGN.md, README.md and SECURITY.md in step with behaviour changes.
6. **Verify locally:** `make check` (vet, race tests, staticcheck, golangci-lint) and `govulncheck ./...`. Update golden files only when the change is intended (`go test ./... -update` where a package supports it), and review the diff.
7. **Commit** in Conventional Commit style (`feat(server): ...`) with `Closes #<n>` in the body. **No `Co-Authored-By` trailers.** Stage only the files this issue touched.
8. **Push and open a PR** against `main` using `.github/pull_request_template.md`, in the `Product v2` milestone, with `Closes #<n>` in the summary. End the PR body with the line `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
   The `pr-template` check fails unless the description has **Summary**, **Changes**, **Testing** and **Checklist** sections, Summary and Changes are filled in, and **every checklist item is ticked**. Tick an item only when it is true; for an item that does not apply, tick it and add "(n/a: reason)", never leave it unticked. Editing the description re-runs the check.
9. **Wait for CI** to finish (`gh pr checks <pr> --watch`). All checks must pass: `pr-template` (required by the `main` ruleset; skipped for Dependabot), `lint`, `security`, `test-goldengate`, `test-goldengatebridge`, `test-shared`, `test-e2e`.
   - A failure caused by the change: fix it on the same branch and push again.
   - A failure that also fails on `main` (not caused by the change): fix it in its own small PR first (`chore/...`), merge that, rebase this branch on main, and continue. Record it in the log below.
10. **Merge** with a merge commit and delete the branch:
    ```bash
    gh pr merge <pr> --merge --delete-branch
    ```
    If `main` moved since the branch was cut, run `gh pr update-branch <pr>` and wait for CI again. If the ruleset blocks a merge while every check is green, find out which rule (`gh api repos/divyanshusingh2903/goldengate/rules/branches/main`) and fix the cause. Never merge with `--admin` without the user's explicit OK.
11. **Sync main again** (step 1), confirm the issue closed, update the Status section (progress, done table, next up), add a log line below, update the roadmap page, and go back to step 2. Status changes ride on the next issue branch's first commit, with step 4.

### Updating the roadmap page

The page reads all progress from one JSON block near its top, `<script id="status" type="application/json">`. Nothing else on the page needs editing when work moves.

1. Read it with the Artifact tool: `action: "read"`, `url: https://claude.ai/artifact/8LFj2xzfz1Vm6vKamiBS3p`. The result names a saved local file holding the full page.
2. In that file's status JSON, set `"updated"` to today, `"next"` to the next issue number, and add or change the issue's entry: `"<n>": {"state": "done", "pr": <pr>, "merged": "YYYY-MM-DD"}`. States: `done`, `in-progress`, `in-review`, `blocked`; issues not listed show as planned. Add PRs that are not queue issues to `"supporting"`.
3. Publish that file with the Artifact tool and the same `url`, so the link stays the same. Keep the JSON valid.

### Rules that hold across sessions

- Product principles in DESIGN.md 1.1 win over anything an issue body implies. If they conflict, stop and ask the user.
- Never commit, stash-pop, or discard the user's own work in progress. Check `git stash list` at the start of a session: an entry named `list-tools wip (user)` holds the user's uncommitted list-tools fix (`docs/brand/BRAND.md`, `bridgemcp/tools.go`, `e2e/e2e_test.go`, from branch `fix/list-tools-empty-hint`). Leave it for the user.
- Never commit real session data, secrets, or home paths (the pre-commit hook in `.githooks` checks; enable it with `git config core.hooksPath .githooks`).
- One issue per branch and PR. If an issue turns out to need splitting, file the new issue in the milestone, add it to the queue, and note it in the log.
- If an issue cannot be finished (blocked on a decision, an external fact, or a failing dependency), set its row to `blocked`, write why in the log, and stop for the user. Do not start the next issue around it unless it does not depend on the blocked one.

## Queue

Status values: `todo`, `in progress`, `in review`, `blocked`, `done`. Size: S about half a day, M about a day, L a day or more. Planned dates are from the roadmap; the order matters more than the dates.

| # | Issue | Size | After | Planned | Status | PR |
|---|---|---|---|---|---|---|
| 1 | #17 Write down the product principles | S | | Nov 13 | done | #50 |
| 2 | #37 Default 5-day window, custom time ranges | S | | Nov 13 | done | #55 |
| 3 | #18 Named projects with members (client) | L | #17 | Nov 14 | done | #59 |
| 4 | #49 Summaries: facts plus narrative | M | | Nov 15 | in progress | |
| 5 | #19 Server: stable project IDs, migration | M | #18 | Nov 16 | todo | |
| 6 | #24 Server: agents and grants model | M | #19 | Nov 16 | todo | |
| 7 | #25 Enforce grants in every MCP tool | M | #24 | Nov 17 | todo | |
| 8 | #26 API and CLI for agents and grants | M | #24 | Nov 17 | todo | |
| 9 | #27 OAuth consent picks agent and projects | M | #24, #25 | Nov 18 | todo | |
| 10 | #28 TUI Agents screen, view as agent | M | #26 | Nov 18 | todo | |
| 11 | #20 Local inbox: pending, approved, excluded | L | #17 | Nov 19 | todo | |
| 12 | #29 Session include, exclude, retract | S | #20 | Nov 20 | todo | |
| 13 | #30 Editable summaries that stay edited | M | #20 | Nov 20 | todo | |
| 14 | #21 Threads from work items and branch names | M | | Nov 21 | todo | |
| 15 | #35 GitLab issues and merge requests | S | | Nov 21 | todo | |
| 16 | #36 MCP context built around work items | M | #21 | Nov 22 | todo | |
| 17 | #45 Design and security model for `goldengate ui` | S | #17 | Nov 22 | todo | |
| 18 | #31 Rules and per-project trust levels | L | #20, #18 | Nov 23 | todo | |
| 19 | #33 Receipts for automatic sends | S | #31, #29 | Nov 24 | todo | |
| 20 | #34 Background sync, rule-approved only | M | #31, #33 | Nov 24 | todo | |
| 21 | #32 Suggest rules from manual choices | S | #31 | Nov 25 | todo | |
| 22 | #22 Suggest projects (inbox only) | M | #18, #20, #21 | Nov 25 | todo | |
| 23 | #44 Cursor adapter: spike, then build | L | | Nov 26 | todo | |
| 24 | #39 Spike: share-link import | S | #17 | Nov 27 | todo | |
| 25 | #41 Per-conversation sharing for chats | M | #18, #29 | Nov 27 | todo | |
| 26 | #42 Incremental export re-import | S | #20 | Nov 27 | todo | |
| 27 | #40 Share-link import | M | #39, #20 | Nov 28 | todo | |
| 28 | #23 Plain words in UI and docs | S | #17 | Nov 28 | todo | |
| 29 | #46 HTTP JSON API over `tui.Backend` | M | #45 | Nov 29 | todo | |
| 30 | #47 Web UI v1: the TUI flow in a browser | L | #46 | Nov 29–30 | todo | |
| 31 | #48 Web UI: inbox, agents, receipts | M | #47, #20, #26, #33 | Nov 30 | todo | |
| — | #38 Tracker lookups, opt-in per tracker (stretch) | M | #21 | after Nov 30 | todo | |
| — | #43 Spike: browser extension (stretch) | S | #40 | after Nov 30 | todo | |

**Slip list** (move these past Nov 30 first if behind, in this order): #48, #40, #32, #22, #28. Rows 1–10 never slip: they change the payload and the server schema.

**Safe to reorder:** rows with an empty "After" column (#37, #49, #21, #35, #44) can move earlier if a session is blocked on something else.

## Log

Newest last. One line per event: date, what happened, PR or issue.

- 2026-11-13: Product v2 issues filed (#11–#49), roadmap set.
- 2026-11-14: #17 opened as #50. CI `security` failed on every branch from GO-2026-6859 in go1.26.10; fixed separately in #51 (toolchain go1.26.11).
- 2026-11-14: User's uncommitted list-tools work stashed as `list-tools wip (user)` so branches can switch cleanly. It edits `listWork` in `bridgemcp/tools.go`, which #37 also changes: expect a conflict there when the user pops it.
- 2026-11-14: Every PR was blocked because the `main` ruleset requires a `pr-template` check that no workflow produced. Added `.github/workflows/pr-template.yml` in #52 (merged first; its only red check was the pre-existing `security` failure).
- 2026-11-14: Merged #52 (pr-template check), #51 (go1.26.11), then #50: #17 done.
- 2026-11-14: Merged #54 (this ledger). #37 implemented: 5-day default, since/until on every time-bounded tool. The per-agent maximum window waits for #24.
- 2026-11-14: #37 done (#55). Replaced the pr-template check from #52 with the user's stricter version from #9 (merged): adds the Checklist section, requires every item ticked, skips Dependabot PRs.
- 2026-11-14: Merged #56 (ledger checklist rule). Added the Status section.
- 2026-11-14: Merged #57 (ledger status section). Roadmap page now reads progress from a status JSON block and shows #17 and #37 done; loop step 11 updates it.
- 2026-11-14: Merged #58 (roadmap page kept in step with the ledger). #18 implemented: `[[projects]]` in the config (members are what is shared, one folder per project, `exclude_sources` per member), legacy `allow`/`deny_source` read as projects, `goldengate projects` commands, TUI `n`/`N`. Server project ids stay as they are until #19.
- 2026-11-14: #18 done (#59). #49 implemented: summaries split into facts (always the local rules) and a narrative (model passages with short/standard/detailed lengths, or the user's); separate narrative caps on client and server; list MCP tools return an abstract and only `get_session` the full narrative.
