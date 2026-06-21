# Release Notes

## v0.2.0 — Mattermost redesign + context-driven listing + priority

Desktop-first redesign of the Right-Hand Sidebar (Quick List, Task Detail, New
Task) following the Mattermost design system, plus a context-driven task
listing model and a new per-task priority field.

> The slash-command layer (`/task list`, `/task add`, `/task show`) and the
> mobile Interactive Dialogs have been **removed** in this release. They will
> be rebuilt on top of the new APIs in a follow-up (see GitHub issues).

### Highlights

- **Mattermost design system.** The RHS now uses Mattermost Blue (`#1f66e1`),
  cool gray neutrals, Open Sans, tight 3–6px corners, uppercase status pills
  with leading dots, @mention-style assignee pills, and a focus ring matching
  the host. Dark theme overrides are kept in sync.
- **Context-driven Quick List.** A single list per channel: regular channels
  show that channel's tasks; DMs show the tasks shared between the two
  participants. The old "My Tasks / Channel Tasks" toggle and the `mine` /
  `all` scopes are gone — the list follows where the user is standing.
- **Filter tabs + grouping.** Six filter tabs (All · Today · To Do · In
  Progress · Done · Cancelled) replace the chip row; tasks are grouped under
  "Needs attention · N", "Upcoming · N" and "Completed · N" headers. Counts
  are computed client-side with an `N+` indicator when more pages exist.
- **Per-task priority.** New `priority` column on `task_tasks` with the
  Mattermost message-priority enum (`standard` / `important` / `urgent`,
  default `standard`). Shown as a colored dot in the list and a click-to-cycle
  pill in the Task Detail meta-table; selectable in the New Task form.
- **Redesigned Task Detail.** Click-to-edit summary, a `96px 1fr` meta-table
  (Status · Priority · Due · Assignee · Channel), click-to-edit description,
  and an activity feed with avatar bubbles.
- **Redesigned New Task.** Title + status/priority + hybrid due (quick-select
  + datetime-local) + assignee + description, in a footer-split primary form.
- **Relative due formatting.** Due dates render as "Today, 17:00" /
  "Tomorrow" / "3 days overdue" / "Fri, 26 Jun" via a shared, locale-aware
  helper with an ISO fallback.

### Breaking changes

- The `mine` and `all` list scopes are removed from `store.Scope` /
  `task.Scope`. The API now accepts only `scope=channel` (with `channel_id`)
  and `scope=direct` (with `partner_id`).
- The `/task` slash command and the mobile Interactive Dialogs (Quick List,
  Task Detail, New Task) are removed along with their HTTP endpoints
  (`/dialogs/*`). They will be reintroduced in a later release.
- `Task` gains a required `priority` field (default `standard`).

### Security

- `GET /tasks?scope=channel` now enforces channel membership: a caller who is
  not a member of the requested channel receives a 403 (prevents enumerating
  another channel's tasks by guessing the id).
- `GET /tasks/{id}` and other per-task reads still rely on the existing
  `CanUserViewTask` rule where it is wired; full wiring is tracked in a
  follow-up issue.

### Deferred (tracked as GitHub issues)

- Wire `CanUserViewTask` into `GET /tasks/{id}` and `SearchTasks`.
- Rebuild the slash-command layer on the 2-scope API.
- Rebuild the mobile Interactive Dialogs on the new design.

## v0.1.0 — first test release

The first usable build of the Task plugin: Lark-Suite-style task management inside Mattermost, covering task CRUD, single assignee, reminders, subtasks, comments, interactive cards, the desktop RHS, and cross-platform slash-command + Interactive Dialog flows.

> This is a **test** release, not a polished one. See [Known limitations](#known-limitations) for what is intentionally deferred and what is known to be rough.

### Highlights

- **Tasks over chat, everywhere.** Create, assign, set due dates and reminders, mark done/cancelled — from the `/task` slash command on desktop or mobile, from interactive task cards, or from the REST API.
- **Subtasks & comments** with parent-done rules and cancel cascade; comments notify participants.
- **Desktop RHS** with a Quick List (My Tasks / Channel Tasks, filters, cursor pagination) and a Task Detail panel (subtasks with `x/y done`, comments), plus a New Task popup dialog and real-time WebSocket updates.
- **Bilingual** — English and Vietnamese, following the user's Mattermost locale.

### What works (Phase 1–3)

Drawn from the closed Phase 1–3 issues (#5–#34) and PLAN.md §9:

- Task CRUD with a 4-status workflow: To Do · In Progress · Done · Cancelled.
- Single assignee per task; DM notification on assignment (skipped when assignee == creator).
- Due dates, descriptions, all-day flag; partial update via `PATCH /tasks/:id`.
- Reminders that fire once at `due − offset` via a cluster-scheduled job (single node); `/task remind <id> <15m|1h|1d|off>`.
- Interactive task cards posted to the channel and DM'd to the assignee, with `Done` / `Cancel` buttons.
- Subtasks (inherit parent's channel + default assignee); parent `done` only when all subtasks are terminal; parent `cancelled` cascade-cancels open subtasks.
- Comments notify creator + assignee (minus the commenter); listed in creation order.
- `/task add` opens a prefilled New Task Interactive Dialog; `/task list` / `/task show` open Quick List / Task Detail dialogs on mobile.
- Desktop webapp: channel-header button → RHS Quick List + Task Detail; `+ New Task` popup with `@username` assignee resolver; post-dropdown "Tạo task" creates a task from a message.
- Real-time updates across clients via WebSocket (`task_updated` events with seq-based stale-drop).
- Server-side permission model: assignee is co-owner (edit/assign/status/complete/subtask/reminder/comment); only the creator or a channel admin may delete.
- Bilingual UI (en/vi), server-side i18n for DM/card text.

### Known limitations

This is a test release. The following are **intentionally deferred or rough** — please don't file duplicates for items already tracked:

- **Kanban board (drag-and-drop, `OrderKey` fractional indexing) is not implemented.** Tracked in Phase 4 issues #35–#42.
- **Out-of-MVP, deferred:** multi-assignee + completion mode (#49), follower role (#44), tasklist/project entity (#43), custom fields (#46), file attachments (#47), repeat rules (#48), AI task agent (#50), advanced search (#51).
- **Storage scale:** KVStore-backed with a safe operating range of roughly ~2,000 tasks/user and ~10,000 tasks/channel. A dedicated-DB migration is roadmap (#52) if benchmarks exceed that.

### How to install / test

1. Build: `make dist` → `dist/com.mattermost.plugin-task-0.1.0.tar.gz`.
2. Upload via System Console → Plugins → Upload, or `mmctl plugin upload dist/com.mattermost.plugin-task-0.1.0.tar.gz`.
3. Enable the plugin. On activation it ensures a `task-bot` account.
4. Walk through the manual checklist in [`docs/E2E.md`](E2E.md) (Phase 1/2/3 sections).

### Server requirement

Mattermost server ≥ 10.7.0, with plugin uploads enabled (`EnableUploads: true`).
