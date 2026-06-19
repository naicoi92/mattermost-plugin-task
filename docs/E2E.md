# Phase 1 Integration Tests & E2E Checklist

> Issue #19. This document records the integration-test coverage and the manual
> end-to-end checklist for the Task plugin's Phase 1 server layer. Items that
> depend on the webapp (Phase 3) or the New Task dialog (#8, deferred) are
> marked **(Phase 3)** and re-verified then.

## Automated coverage

All server packages pass `cd server && go test ./...`:

| Package | Coverage |
|---|---|
| `server/model` | Task/Comment/ReminderMetadata DTOs |
| `server/permission` | co-owner rules (modify/delete/view/comment) |
| `server/store/kvstore` | key-per-edge CRUD, atomic CAS, reminder index |
| `server/task` | create (ULID+OrderKey+indexes), get, patch, delete cascade, list/search, status workflow + cascade-cancel, reminder rebuild/fire, assign swap, subtask progress |
| `server/notification` | assign/done/cancel/comment/reminder DMs, actor exclusion, locale, best-effort |
| `server/command` | `/task` subcommands (status/done/cancel/edit/list/show/search/remind/assign/unassign/add) |
| `server` (main) | REST endpoints, task-card build/post/update, card-action callback, Quick List/Task Detail dialog build/submit |

Full lint: `cd server && golangci-lint run ./...` → 0 issues.

## Build verification

`make dist` produces `dist/com.mattermost.plugin-task-<version>.tar.gz`
(plugin binaries for linux/darwin/windows × amd64/arm64 + webapp bundle + i18n
assets). Uploadable via System Console → Plugins or `mmctl plugin upload`.

## Manual E2E checklist

> Run on a Mattermost dev server (`EnableUploads: true`, min server version
> 10.7.0). Upload the tarball from `make dist`, enable the plugin, and exercise
> each flow.

### Core lifecycle
- [ ] `make dist` succeeds and the plugin uploads + activates without errors.
- [ ] `POST /api/v1/tasks` (or a client) creates a task; the interactive task card
      posts in the channel (issue #15).
- [ ] Creating a task with an assignee (≠ creator) DMs the assignee the card
      (issue #15 / #14).
- [ ] `PATCH /api/v1/tasks/:id/status` to `done` updates the card (struck-through)
      and DMs creator + assignee (issue #11 / #14 / #15).
- [ ] `/task cancel <id>` updates the card, stops reminders, and cascades cancel
      to open subtasks (issue #11 / #13 / #15).
- [ ] `/task list mine` returns the user's assigned tasks (issue #9 server path).
- [ ] `/task remind <id> 15m` results in exactly one DM to the assignee at
      `due − 15m` (issue #13).
- [ ] `/task status <id> todo` after `done` reopens the task (clears
      CompletedAt) and re-arms the reminder if due+offset are set (issue #11/#13).
- [ ] `/task assign <id> @user` swaps the assignee, DMs the new assignee, and
      does NOT DM the unassigned user (issue #12 / #14).
- [ ] Card buttons `✓ Done` / `🚫 Cancel` apply the transition and refresh the
      card (issue #15).

### Permission boundaries (issue #18)
- [ ] Assignee can edit summary/due/status/assignee/subtask/reminder (co-owner).
- [ ] Assignee CANNOT delete the task.
- [ ] Creator can delete; a channel admin can delete channel-scoped tasks.
- [ ] Personal tasks (no ChannelID) are hidden from everyone except
      creator + assignee.

### Reminders (issue #13)
- [ ] `idx:reminder:` index is rebuilt on create/due-change/status-change/
      offset-change/assignee-change.
- [ ] The per-minute cluster job fires due reminders once and marks them fired.
- [ ] A missed reminder (past the grace window) is dropped, not fired late.
- [ ] Terminal statuses drop the reminder edge; self-heal drops any stale edge.

### Interactive dialogs (issue #17, mobile/fallback)
- [ ] Quick List dialog renders scope/status/due filters + top-N task select.
- [ ] Task Detail dialog edits summary/status/assignee/due/description; submit
      applies PATCH + status + assignee, reporting partial success on failure.
- [ ] Task Detail can clear the assignee (AssigneeSet distinguishes clear from
      unchanged).

## Deferred to Phase 3 (webapp)

These Phase 1 acceptance criteria require the React webapp (RHS, root
components) which lands in Phase 3, and are re-verified there:
- `/task add "<summary>"` opening the React `NewTaskDialog` (issue #8).
- `/task list` / `/task show` opening the RHS views (issue #9).
- `/task list` opening a Quick List Interactive Dialog from the command path on
  mobile (issue #9) — the dialog builder exists (#17); command wiring is Phase 3.

The server-side building blocks for all of the above are already merged
(task service, REST API, slash commands, dialog builders, notification layer),
so Phase 3 is primarily webapp React work.

## Test server setup

1. Mattermost server ≥ 10.7.0, dev config with `EnableUploads: true`.
2. `make dist` → upload `dist/com.mattermost.plugin-task-*.tar.gz` via System
   Console → Plugins → Upload, or `mmctl plugin upload <tarball>`.
3. Enable the plugin; the `task-bot` is ensured on activation.
4. Optional System Console settings: default reminder offset (minutes), DM
   notifications on/off (both have sensible defaults).
