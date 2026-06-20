# Task

A Mattermost plugin that brings Lark-Suite-style task management into the chat flow — create tasks, assign teammates, set reminders, track progress, and discuss work without leaving the channel.

> **Status:** v0.1.0 — first test release. See [Known limitations](#known-limitations) before relying on it.

## Features

**Core task management**
- Task CRUD with a 4-status workflow: **To Do · In Progress · Done · Cancelled**.
- Single assignee per task, with a direct-message notification when assigned (skipped when the assignee is the creator).
- Due dates, descriptions, and all-day flag.
- Reminders that fire once at `due − offset` via the plugin bot (cluster-scheduled, fires on a single node).
- Interactive task cards posted to the channel and DM'd to the assignee, with `Done` / `Cancel` buttons.

**Subtasks & comments**
- Subtasks inherit the parent's channel and (by default) assignee; each has its own independent status.
- Parent can be marked `done` only when every subtask is `done`/`cancelled`; cancelling a parent cascade-cancels its open subtasks.
- Comments notify task participants (creator + assignee), excluding the commenter.

**Cross-platform access**
- Slash command `/task` works everywhere (desktop, mobile, API).
- Interactive message cards (with action buttons) render on both desktop and mobile.
- On mobile, `/task add`, `/task list`, and `/task show` open **Interactive Dialogs** so the experience matches desktop.

**Desktop webapp enhancement**
- Channel-header button opens the Right-Hand Sidebar: a **Quick List** (My Tasks / Channel Tasks tabs, status & due filters, cursor pagination) and a **Task Detail** panel (summary, status, due, assignee, subtasks with `x/y done`, comments).
- **New Task** popup dialog with a `@username` assignee resolver.
- Real-time updates across clients via WebSocket (`task_updated` events with seq-based stale-drop).
- **Bilingual UI** — English and Vietnamese, switching with the user's Mattermost locale.

## Slash commands

```
/task new ["<summary>"]                                        open the New Task dialog (blank or pre-filled)
/task add "<summary>"                                          create a task (opens a dialog)
/task list [mine|channel|all] [status] [due]                   list and filter tasks
/task show <id>                                                view task details
/task search <keyword>                                         search tasks by keyword
/task status <id> <todo|in_progress|done|cancelled>            change status
/task done <id>                                                mark a task done
/task cancel <id>                                              cancel a task
/task edit <id> [summary=...] [due=<ms>] [desc=...]            partial update
/task assign <id> @user                                        assign a task to a user
/task unassign <id>                                            remove the assignee
/task subtask <parentId> <summary>                             add a subtask
/task comment <id> <text>                                      add a comment
/task remind <id> <15m|1h|1d|off>                              set or turn off a reminder
/task help                                                     show this help
```

Status values: `todo` · `in_progress` · `done` · `cancelled`.
Due filter values: `overdue` · `today` · `week`.

## REST API

All endpoints are prefixed with `/plugins/com.mattermost.plugin-task/api/v1/` and require an authenticated session.

| Method | Path | Description |
|---|---|---|
| `POST` | `/tasks` | Create a task |
| `GET` | `/tasks` | List/filter tasks (`scope`, `channel_id`, `status`, `due`, `after_order_key`, `limit`) |
| `GET` | `/tasks/:id` | Get task details |
| `PATCH` | `/tasks/:id` | Partial update (`update_fields`) |
| `DELETE` | `/tasks/:id` | Delete (hard-delete cascade) |
| `PATCH` | `/tasks/:id/status` | Change status |
| `POST` | `/tasks/:id/assignee` | Set assignee |
| `DELETE` | `/tasks/:id/assignee` | Clear assignee |
| `POST` | `/tasks/:id/reminder` | Set reminder (`offset_ms`) |
| `DELETE` | `/tasks/:id/reminder` | Turn reminder off |
| `POST` | `/tasks/:id/subtasks` | Create a subtask |
| `GET` | `/tasks/:id/subtasks` | List subtasks |
| `POST` | `/tasks/:id/comments` | Add a comment |
| `GET` | `/tasks/:id/comments` | List comments |
| `POST` | `/actions` | Interactive task-card button callback |
| `POST` | `/dialogs/quicklist` | Quick List dialog submit |
| `POST` | `/dialogs/taskdetail` | Task Detail dialog submit |
| `POST` | `/dialogs/newtask` | New Task dialog submit |
| `POST` | `/dialogs/open-task-detail` | Open Task Detail dialog (opener) |
| `POST` | `/dialogs/open-new-task` | Open New Task dialog (opener) |

## Requirements

- **Mattermost server** ≥ 10.7.0.
- Plugin uploads enabled (`EnableUploads: true` in `PluginSettings`).

## Installation

Build the plugin:

```
make dist
```

This produces `dist/com.mattermost.plugin-task-<version>.tar.gz` (server binaries for linux/darwin/windows × amd64/arm64, plus the webapp bundle and i18n assets).

Install via **System Console → Plugins → Upload**, or with `mmctl`:

```
mmctl plugin upload dist/com.mattermost.plugin-task-0.1.0.tar.gz
```

Enable the plugin. On activation it ensures a `task-bot` account that authors DM/card posts.

## Configuration

System Console → Plugins → Task exposes two settings:

| Setting | Default | Description |
|---|---|---|
| Default Reminder Offset (minutes) | `15` | Default minutes before due to send a reminder. |
| Enable DM Notifications | `true` | When enabled, the bot DMs on assignment, completion, cancellation, comment, and reminder. |

The four Kanban statuses (To Do / In Progress / Done / Cancelled) are fixed and not configurable.

## Desktop vs mobile

| Capability | Desktop | Mobile |
|---|---|---|
| Slash command `/task *` | ✅ | ✅ |
| Interactive task card (with buttons) | ✅ | ✅ |
| Interactive Dialogs (`/task new`, `/task add`, `/task list`, `/task show`) | ✅ | ✅ |
| Channel-header "New Task" button (icon) | ✅ | ❌ |
| RHS Quick List + Task Detail (React) | ✅ | ❌ |
| New Task popup dialog (React) | ✅ | ❌ |
| Kanban board (drag-and-drop) | 🚧 planned | ❌ |

Mobile relies on slash commands + interactive cards/dialogs (cross-platform); the React RHS, New Task button, and Kanban are desktop enhancements. Use `/task new` on mobile to open the create-task dialog.

## Development

```
make            # build server + webapp
make deploy     # build and deploy to a local server (requires local mode or credentials)
make watch      # rebuild + redeploy on file change
```

For local-mode deploy, enable `EnableLocalMode` in `ServiceSettings` and set `MM_SERVICESETTINGS_SITEURL`. See the [Mattermost plugin docs](https://developers.mattermost.com/extend/plugins/) for details.

Tests:

```
cd server && go test ./...        # server unit + integration tests
cd server && golangci-lint run ./...
cd webapp && npm test             # webapp Jest suite
cd webapp && npm run lint
```

The i18n bundles live under `assets/i18n/{en,vi}.json` and are copied to `webapp/i18n/` at build time (single source for server and webapp).

## Releasing

The version is pinned in `plugin.json` (`"version"`). To cut a release, bump that field, tag `v<version>`, build `make dist`, and attach the tarball to a GitHub Release.

## Known limitations (v0.1.0)

- The Kanban board (drag-and-drop, `OrderKey` fractional indexing) is **not yet implemented** — planned for a later phase.
- Out-of-MVP items deferred: multi-assignee + completion mode, followers, tasklist (project) entity, custom fields, file attachments, repeat rules, AI task agent.
- `KVStore`-backed storage with a safe operating range of roughly ~2,000 tasks/user and ~10,000 tasks/channel. A dedicated-DB migration is a roadmap item if benchmarks exceed that.

## License

See [LICENSE](LICENSE).
