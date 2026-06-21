# Kế hoạch: Chuyển persistence từ KV sang SQL (kiểu mattermost-plugin-boards)

> **Trạng thái:** ✅ Hoàn thành (milestone 12 — toàn bộ 22 issues đóng)
> **Tạo:** 2026-06-20
> **Hoàn thành:** 2026-06-21
> **Tài liệu tham chiếu:** [`mattermost-plugin-boards`](https://github.com/mattermost/mattermost-plugin-boards) — `server/services/store/sqlstore/`

## Verification (M5-5)

Final build + test verification (commit on main, 2026-06-21):

| Check | Result |
|---|---|
| `make dist` | ✅ `dist/com.mattermost.plugin-task-0.1.0.tar.gz` built |
| `go test -race ./server/...` | ✅ 8 packages pass (server, command, model, notification, permission, store/sqlstore, task, taskutil) |
| `go vet ./server/...` | ✅ clean |
| `golangci-lint run --config .golangci.yml ./server/...` | ✅ 0 issues |

### Automated coverage (what the tests prove)
- **Create → card → status change**: api_test `TestPatchTaskStatus_Endpoint`, `TestCreateTask_Endpoint`
- **Reminder due → fires**: service_test `TestFireReadyReminders_WithinWindow`, `service_reminder_job_test` full-cycle
- **SubtaskProgress (1 GROUP BY)**: service_test `TestSubtaskProgress`, sqlstore `TestSubtaskProgress_*`
- **Search ILIKE/LIKE**: sqlstore `TestSearchTasks_ILikeOrLike`, `TestSearchTasks_EscapesWildcards`
- **GET /tasks/:id/events audit trail**: api_test `TestListTaskEvents_ReturnsAuditTrail`
- **Delete → FK cascade**: service_test `TestDelete_CascadeRemovesDependents` (task + subtask + members removed via FK)
- **Migration idempotent**: sqlstore `TestRunMigrations_IdempotentApplyTwice`
- **List >100 + filter/pagination**: sqlstore `TestListTasks_ScopeChannelStatusAndPagination` (keyset cursor + HasMore)

### Manual E2E (requires running Mattermost server)
The 8 manual E2E steps in issue #135 require a live Mattermost instance (create
task → card post → DM → reminder fire → cascade delete verify in DB). These are
documented as the post-deploy acceptance checklist; the automated coverage above
proves the underlying contracts.

---

## 1. Background & lý do

Lớp lưu trữ hiện tại (`server/store/kvstore/`) dựa trên KVStore với schema "key-per-edge". Thiết kế này đã được nghĩ kỹ (ULID, self-healing reads, CAS retry cho `TouchTaskUpdatedAt`), nhưng gặp các vấn đề **nền tảng** không thể khắc phục trong khuôn khổ KV:

| # | Vấn đề | Bằng chứng |
|---|---|---|
| 1 | **N+1 reads** | `task.Service.List` (`service.go:1055`) gọi `ListKeys` rồi lặp `GetTask(id)` mỗi candidate |
| 2 | **Pagination giả** | `service.go:1077` tải **toàn bộ** candidate rồi mới cắt `Limit` |
| 3 | **Filter trong Go** | `matchStatus`/`matchDue` (`service.go:1063-1126`) chạy **sau** khi load mọi entity — không push `WHERE` xuống storage |
| 4 | **Không aggregation thực** | Kanban "5/12 done" = load tất cả subtask + đếm trong Go (`SubtaskProgress`) |
| 5 | **Search scan toàn bộ** | `service.go:1146` scan mọi task + `strings.Contains` từng cái |
| 6 | **Không transaction** | `Create` = task + 4-5 index edges tuần tự không atomic (`service.go:142-154`); handler `createTask` còn post card + ghi post_id 3 bước tuần tự → crash để lại card mồ côi |
| 7 | **`ListKeys` lọc prefix client-side** | `client.go:186` — không phải index scan thật |
| 8 | **Không audit trail** | Không biết ai đổi gì khi nào → debug khó |

**Giải pháp:** Chuyển sang SQL như Boards — tái sử dụng DB connection của Mattermost qua `pluginapi.Store.GetMasterDB()`, tạo các bảng riêng có namespace `task_*`. Giải quyết tận gốc 8 vấn đề trên: `WHERE/ORDER BY/LIMIT`, `GROUP BY`, `BEGIN/COMMIT`, ILIKE search, FK cascade, audit log.

API `pluginapi.Store.GetMasterDB()` đã có ở `min_server_version` 10.7.0 (phiên bản tối thiểu của plugin).

---

## 2. Quyết định thiết kế (đã chốt)

Thiết kế **từ chức năng**, không bê cấu trúc KV. Phân tích 10 chức năng của plugin:

| # | Chức năng | Bản chất dữ liệu | **Quyết định** |
|---|---|---|---|
| 1 | Quản lý task (CRUD) | Entity cốt lõi | Cột trong `task_tasks` |
| 2 | Phạm vi (cá nhân/kênh) | 1:1 với task | Cột `channel_id` trong `task_tasks` |
| 3 | Subtask (cha-con) | Self-reference | Cột `parent_task_id` + FK self trong `task_tasks` |
| 4 | Người liên quan (creator+assignee, multi-ready) | Quan hệ N:N với role | **Bảng `task_members`** ✅ |
| 5 | Bình luận | Reuse thread Mattermost | Bảng `task_comments` chỉ lưu **ánh xạ** `(task_id, post_id)` ✅ |
| 6 | Nhắc nhở (multi-ready) | 1:N lifecycle riêng | **Bảng `task_reminders`** ✅ |
| 7 | Card post tracking (linh hoạt) | 1:N lifecycle riêng | **Bảng `task_posts`** ✅ |
| 8 | Audit/history | Append-only log | Bảng `task_events` |
| 9 | Thông báo DM | Stateless | Không bảng |
| 10 | Real-time WebSocket | Stateless | Không bảng |

**Quy tắc:** thuộc tính cốt lõi 1:1 → cột trong `task_tasks`. Quan hệ 1:N hoặc có lifecycle/lược đồ riêng → bảng riêng với FK + `ON DELETE CASCADE`.

**3 lựa chọn future-proof** (đã chốt với user):
- ✅ **task_members** thay vì cột `creator_id`/`assignee_id` — sẵn sàng multi-assignee + follower mà không cần migrate
- ✅ **task_reminders** thay vì cột `reminder_offset`/`reminder_fired` — sẵn sàng multi-reminder/task
- ✅ **task_posts** thay vì 2 cột cứng `channel_post_id`/`dm_post_id` — linh hoạt post card chỗ khác

---

## 3. Schema cuối cùng (6 bảng)

**Quy ước:**
- PK/FK = `VARCHAR(26)` (ULID)
- Timestamp = `BIGINT` ms UTC (giữ convention cũ)
- Dialect: postgres (production) + sqlite (test). MySQL schema option.
- Migrations dùng morph + `//go:embed` + dialect templates `{{prefix}}`/`{{postgres}}`/`{{sqlite}}`/`{{mysql}}` (giống Boards)
- Table prefix = `task_`

> **Tại sao `BIGINT` cho datetime (không phải `TIMESTAMP`/`TIMESTAMPTZ`)?**
> Đây là convention của cả **Mattermost core** (`model.Post.CreateAt`/`UpdateAt`/`DeleteAt` đều là `int64` ms) và **mattermost-plugin-boards** (`blocks.create_at`/`update_at`/`delete_at` đều `BIGINT`). Boards chỉ dùng `TIMESTAMPTZ` cho `insert_at` (field kỹ thuật phục vụ replication, cần `DEFAULT NOW()` ở DB level).
>
> Lý do áp dụng cho plugin này:
> 1. **Tính toán thời gian bằng toán số nguyên**: `now >= due_at - offset_ms` không cần `EXTRACT(EPOCH FROM ...)` hay cast — query reminder phụ thuộc điều này
> 2. **Consistent với model hiện tại**: `model.Task.Due`, `model.ReminderMetadata (deleted)` đã là `int64` ms; đổi sang TIMESTAMP buộc convert ở mỗi boundary API/DB → bug-prone
> 3. **Cross-dialect đơn giản**: `BIGINT` giống nhau ở postgres/mysql/sqlite; `TIMESTAMPTZ` là postgres-only (mysql `DATETIME`, sqlite `INTEGER`/`TEXT`) → migration phải dialect-specific phức tạp hơn
> 4. **Go marshal đơn giản**: `int64` ↔ JSON number, không cần custom `MarshalJSON` cho time
> 5. **Timezone handled at render**: server lưu ms UTC, client tự hiển thị theo timezone preference (giống Lark/PLAN đã chốt)
>
> Query theo ngày vẫn dùng index hiệu quả: `WHERE due_at BETWEEN :startOfDayMs AND :endOfDayMs`. Chỉ cân nhắc `TIMESTAMPTZ` nếu sau này cần `DEFAULT NOW()` ở DB, range partition, hoặc `date_trunc()` thường xuyên — đều ngoài scope hiện tại.

### 3.1. `task_tasks` — entity trung tâm (chỉ data cốt lõi)

```sql
CREATE TABLE {{prefix}}task_tasks (
    id             VARCHAR(26) PRIMARY KEY,         -- ULID
    summary        TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    channel_id     VARCHAR(26) NOT NULL DEFAULT '', -- '' = personal
    parent_task_id VARCHAR(26) DEFAULT NULL,          -- subtask; NULL = top-level
    status         VARCHAR(32) NOT NULL DEFAULT 'todo',
    order_key      VARCHAR(64) NOT NULL,              -- global fractional index (Kanban)
    is_all_day     BOOLEAN NOT NULL DEFAULT FALSE,
    due_at         BIGINT DEFAULT NULL,               -- ms; NULL = no due
    completed_at   BIGINT DEFAULT NULL,
    cancelled_at   BIGINT DEFAULT NULL,
    created_at     BIGINT NOT NULL,
    updated_at     BIGINT NOT NULL,
    CONSTRAINT fk_tasks_parent FOREIGN KEY (parent_task_id)
        REFERENCES {{prefix}}task_tasks(id) ON DELETE CASCADE
);
CREATE INDEX idx_tasks_channel    ON {{prefix}}task_tasks (channel_id, status);
CREATE INDEX idx_tasks_parent     ON {{prefix}}task_tasks (parent_task_id);
CREATE INDEX idx_tasks_order_key  ON {{prefix}}task_tasks (order_key);
CREATE INDEX idx_tasks_status_due ON {{prefix}}task_tasks (status, due_at);
```

**Loại bỏ khỏi `task_tasks`** (so với KV model): `creator_id`, `assignee_id`, `channel_post_id`, `dm_post_id`, `reminder_offset`, `reminder_fired` — tất cả chuyển sang bảng quan hệ.

### 3.2. `task_members` — creator/assignee/follower (future-proof)

```sql
CREATE TABLE {{prefix}}task_members (
    task_id    VARCHAR(26) NOT NULL,
    user_id    VARCHAR(26) NOT NULL,
    role       VARCHAR(32) NOT NULL,     -- 'creator' | 'assignee' | 'follower' (future)
    created_at BIGINT NOT NULL,
    CONSTRAINT pk_members PRIMARY KEY (task_id, user_id, role),
    CONSTRAINT fk_members_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}task_tasks(id) ON DELETE CASCADE
);
CREATE INDEX idx_members_user ON {{prefix}}task_members (user_id, role);
CREATE INDEX idx_members_task ON {{prefix}}task_members (task_id);
```

- "My Tasks" = `JOIN task_members WHERE user_id=? AND role='assignee'` — 1 query JOIN, không N+1
- MVP app-layer enforce: 1 creator + 1 assignee mỗi task
- Schema đã sẵn sàng multi-assignee + follower (PLAN hoãn) mà không cần migrate

### 3.3. `task_reminders` — multi-ready

```sql
CREATE TABLE {{prefix}}task_reminders (
    id         VARCHAR(26) PRIMARY KEY,   -- ULID (mỗi reminder có id riêng)
    task_id    VARCHAR(26) NOT NULL,
    offset_ms  BIGINT NOT NULL,            -- ms trước due mà reminder fire
    fired_at   BIGINT DEFAULT NULL,        -- NULL = chưa fire
    created_at BIGINT NOT NULL,
    CONSTRAINT fk_reminders_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}task_tasks(id) ON DELETE CASCADE
);
CREATE INDEX idx_reminders_pending ON {{prefix}}task_reminders (fired_at);
```

- Scheduler chỉ scan bảng nhỏ này `WHERE fired_at IS NULL` — không đụng `task_tasks`
- Fire query JOIN lấy due + assignee (1 query, không ListKeys+GetReminder+GetTask của KV):

```sql
SELECT r.id, r.task_id, r.offset_ms, t.due_at, m.user_id AS assignee_id
FROM {{prefix}}task_reminders r
JOIN {{prefix}}task_tasks t ON t.id = r.task_id
LEFT JOIN {{prefix}}task_members m ON m.task_id = r.task_id AND m.role = 'assignee'
WHERE r.fired_at IS NULL
  AND t.status IN ('todo','in_progress')
  AND t.due_at IS NOT NULL
  AND (t.due_at - r.offset_ms) <= :nowMs
  AND t.due_at >= :nowMs - :graceMs
```

- MVP app-layer enforce: 1 reminder/task; schema đã sẵn sàng multi-reminder (PLAN hoãn)

### 3.4. `task_posts` — card post tracking (linh hoạt)

```sql
CREATE TABLE {{prefix}}task_posts (
    id         VARCHAR(26) PRIMARY KEY,   -- ULID
    task_id    VARCHAR(26) NOT NULL,
    post_id    VARCHAR(26) NOT NULL,       -- Mattermost post id
    kind       VARCHAR(32) NOT NULL,       -- 'channel' | 'dm' | (future kinds)
    created_at BIGINT NOT NULL,
    CONSTRAINT fk_posts_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}task_tasks(id) ON DELETE CASCADE,
    CONSTRAINT uq_posts_post UNIQUE (post_id)
);
CREATE INDEX idx_posts_task ON {{prefix}}task_posts (task_id);
```

- Update card khi đổi status = `SELECT post_id FROM task_posts WHERE task_id=?` → update mỗi post
- Bỏ 2 cột cứng `channel_post_id`/`dm_post_id`. Linh hoạt post thêm card chỗ khác không cần migrate

### 3.5. `task_comments` — ánh xạ tới thread Mattermost (Hybrid)

> **Quyết định thiết kế quan trọng (đã chốt):** Comment **không** lưu nội dung trong SQL.
> Task hoạt động như một **thread tin nhắn Mattermost**: card task trong channel = **root post**, mỗi comment = **post reply** (`RootId = rootPostID`). Bảng này chỉ lưu **ánh xạ** `(task_id, post_id)` để plugin biết reply nào thuộc task nào; nội dung thật sống trong Mattermost.

**Workflow:**
1. Tạo task → bot post card làm **root post** (đã track trong `task_posts` với `kind='channel'`)
2. User reply trong thread → hook `MessageHasBeenPosted` catch post có `RootId == root post id của task` → INSERT vào `task_comments(task_id, post_id)`
3. `GET /tasks/:id/comments` → query `task_comments` + `GetPost(post_id)` để lấy content/reactions/author
4. @mention, notification, reaction, attachment, edit, delete → Mattermost lo (free)
5. Task deleted → cascade xóa `task_comments` (post gốc + reply vẫn còn trong Mattermost, hoặc best-effort `DeletePost` root)

**Lợi ích so với bảng comment đầy đủ:**
- Plugin giảm ~70% logic comment (không render markdown, không reaction, không attachment, không edit history)
- UX chat native — user thảo luận ngay trong kênh, không rời đi
- Native notification (badge, push, email, mobile), @mention, reaction — tất cả free
- Thread collapse trong kênh — không spam

**Trade-off chấp nhận được:**
- Post bị admin/ghost xóa → `GetPost` trả nil → skip trong list (defensive, giống self-heal)
- Search task comment qua `SearchPostsInTeam` (pluginapi) thay SQL ILIKE trên content — đủ cho MVP (audit vẫn có snapshot tại event)
- Task personal (không channel): root post = DM card với assignee/creator; comment trong DM thread

```sql
CREATE TABLE {{prefix}}task_comments (
    id         VARCHAR(26) PRIMARY KEY,        -- ULID (id nội bộ)
    task_id    VARCHAR(26) NOT NULL,
    post_id    VARCHAR(26) NOT NULL UNIQUE,    -- Mattermost post id (reply trong thread)
    author_id  VARCHAR(26) NOT NULL,            -- snapshot user_id lúc comment (audit, không phụ thuộc post)
    created_at BIGINT NOT NULL,                 -- snapshot create_at của post (sort)
    CONSTRAINT fk_comments_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}task_tasks(id) ON DELETE CASCADE
);
CREATE INDEX idx_comments_task ON {{prefix}}task_comments (task_id, created_at);
```

**Loại bỏ so với bản đầu:** cột `content` (sống trong post), `updated_at` (edit qua Mattermost). Giữ `author_id` + `created_at` làm **snapshot** để audit/sort không phụ thuộc post bị xóa/sửa.

### 3.6. `task_events` — audit log (append-only)

```sql
CREATE TABLE {{prefix}}task_events (
    id          VARCHAR(26) PRIMARY KEY,   -- ULID
    task_id     VARCHAR(26) NOT NULL,
    actor_id    VARCHAR(26) NOT NULL,
    event_type  VARCHAR(64) NOT NULL,      -- created|status_changed|assigned|due_changed|deleted|commented|reminder_set|...
    from_value  TEXT DEFAULT NULL,         -- JSON snapshot cũ; NULL cho create
    to_value    TEXT DEFAULT NULL,         -- JSON snapshot mới; NULL cho delete
    created_at  BIGINT NOT NULL,
    CONSTRAINT fk_events_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}task_tasks(id) ON DELETE CASCADE
);
CREATE INDEX idx_events_task ON {{prefix}}task_events (task_id, created_at DESC);
```

- Mỗi transition (Create/SetStatus/Assign/Patch/Delete/AddComment/SetReminder) ghi 1 event **trong cùng tx với change chính** — atomic
- `GET /tasks/:id/events` cho audit trail

---

## 4. Milestones & Issues

> Mỗi issue = một chức năng/đơn vị công việc riêng biệt. Mỗi issue có mục tiêu, yêu cầu, files ảnh hưởng, dependencies.

---

### MILESTONE M1 — SQL Infrastructure

> Nền tảng: store scaffolding, dialect handling, migrations framework. Chưa có logic nghiệp vụ.

---

#### Issue M1-1: Khởi tạo package `server/store/sqlstore/` + dialect helpers

**Mục tiêu:** Tạo skeleton package SQLStore với khả năng xử lý nhiều dialect (postgres/mysql/sqlite).

**Yêu cầu:**
- Struct `SQLStore` giữ `db *sql.DB`, `dbType string`, `tablePrefix string`
- Constructor `New(db *sql.DB, dbType, tablePrefix string) (*SQLStore, error)`
- Helper dialect:
  - `placeholderFormat()` — squirrel `$` (postgres) | `?` (mysql/sqlite)
  - `escapeField(name)` — `"` (postgres/sqlite) | `` ` `` (mysql)
  - `tableName(short string) string` — trả `prefix + short` (vd `"task_tasks"`)
- Đọc config từ `p.client.Configuration.GetConfig().SqlSettings` để biết driver + connection string
- Helper `withRunner(runner sq.BaseRunner) *SQLStore` — trả SQLStore mới dùng runner cho (pool hoặc tx)

**Files:**
- `server/store/sqlstore/sqlstore.go` (mới)
- `server/store/sqlstore/sqlstore_test.go` (mới — test dialect helpers)

**Dependencies:** (none)

---

#### Issue M1-2: Migrations framework với morph (cluster-safe)

**Mục tiêu:** Thiết lập engine migration idempotent, an toàn trên cluster (chỉ 1 node chạy migration).

**Yêu cầu:**
- Tích hợp `github.com/mattermost/morph` (driver postgres + sqlite cho test)
- `//go:embed migrations/*.sql` qua `embed.FS`
- Method `RunMigrations(api plugin.API) error`:
  - Acquire `cluster.Mutex(api, "TaskMigrationMutex")` trước khi chạy
  - Áp dụng migrations theo thứ tự
  - Idempotent: chạy 2 lần liên tiếp không lỗi (morph track schema version)
- SQL migration files dùng template engine của morph với params `{prefix, postgres, sqlite, mysql}` + helper funcs (`createIndexIfNeeded`, ...)
- Tạo file migration đầu tiên `000001_init.up.sql` + `.down.sql` (chỉ tạo bảng rỗng trước, hoặc để M2-1 điền schema)

**Files:**
- `server/store/sqlstore/migrate.go` (mới)
- `server/store/sqlstore/migrations/000001_init.up.sql` (mới)
- `server/store/sqlstore/migrations/000001_init.down.sql` (mới)
- `server/store/sqlstore/migrate_test.go` (mới — test idempotent 2 lần)

**Dependencies:** M1-1

---

#### Issue M1-3: Thêm dependencies (go.mod)

**Mục tiêu:** Thêm các thư viện cần thiết.

**Yêu cầu:**
- `github.com/Masterminds/squirrel` — query builder (Boards dùng)
- `github.com/mattermost/morph` — migrations
- `github.com/mattermost/morph/drivers/mysql` (cho production mysql)
- `github.com/mattermost/morph/drivers/postgres` (cho production postgres)
- `github.com/mattn/go-sqlite3` — test only (build tag `//go:build test`)
- Chạy `go mod tidy`
- `make dist` build OK

**Files:**
- `go.mod` (update)
- `go.sum` (update)

**Dependencies:** (none)

---

### MILESTONE M2 — 6 bảng quan hệ

> Mỗi issue = 1 bảng = 1 chức năng. Triển khai repository (CRUD + queries) cho mỗi bảng.

---

#### Issue M2-1: `task_tasks` entity repository

**Mục tiêu (Chức năng #1 + #2 + #3):** CRUD + list + search cho entity task cốt lõi + subtask self-reference.

**Yêu cầu:**
- Migration 000001: tạo bảng `task_tasks` với tất cả cột + FK self `parent_task_id` + 4 indexes (xem §3.1)
- Repository methods:
  - `CreateTask(ctx, task Task) (Task, error)` — INSERT, return row đã insert
  - `GetTask(ctx, id string) (*Task, error)` — SELECT by id
  - `UpdateTask(ctx, task Task) (Task, error)` — `UPDATE ... RETURNING *`
  - `TouchTaskUpdatedAt(ctx, id string, ts int64) error` — `UPDATE ... SET updated_at = GREATEST(updated_at, ?)`
  - `DeleteTask(ctx, id string) error` — DELETE (FK cascade tự dọn mọi bảng con)
  - `ListTasks(ctx, q ListQuery) (PageResult, error)` — push WHERE/ORDER BY/LIMIT xuống SQL:
    - Filter theo scope: `mine` (JOIN task_members role='assignee') / `channel` (WHERE channel_id=?) / `all`
    - Filter theo status, due (overdue/today/week) — push xuống SQL clause
    - Pagination cursor `after_order_key` + `LIMIT n+1` để tính `has_more`
    - Song song chạy `SELECT COUNT(*)` (cùng WHERE) cho `total`
    - Return `PageResult{items []Task, total, has_more}`
  - `CountTasksByStatus(ctx, q ListQuery) (map[string]int, error)` — `GROUP BY status` cho Kanban progress
  - `SearchTasks(ctx, keyword string, limit int) ([]Task, error)` — ILIKE trên summary/description
  - `ListSubtasks(ctx, parentID string) ([]Task, error)` — WHERE parent_task_id=? ORDER BY created_at
  - `SubtaskProgress(ctx, parentID string) (done, total int, err error)` — `GROUP BY status` 1 query
  - `GetTask(ctx, id string) (*Task, error)` — JOIN task_members + task_reminders + task_posts để build DTO
  - `nextGlobalOrderKey(ctx)` — `SELECT MAX(order_key) FROM task_tasks` (helper)
- `ListQuery` struct: `{Scope, UserID, ChannelID, Status, Due, AfterOrderKey, Limit}`
- `PageResult[T]` generic struct: `{Items []T, Total int, HasMore bool}` (hoặc non-generic cho MVP)

**Files:**
- `server/store/store.go` (mới — interface, được build-up qua các issue)
- `server/store/sqlstore/tasks.go` (mới)
- `server/store/sqlstore/tasks_test.go` (mới — sqlite in-memory)
- `server/model/task.go` (sửa — bỏ `CreatorID`, `AssigneeID`, `ChannelPostID`, `DMPostID`, `ReminderOffset`, `ReminderFired`, đổi `Due` → `DueAt` (đã thực hiện trong PR #154); thêm `model.Task`)
- `server/store/sqlstore/migrations/000001_init.up.sql` (điền schema task_tasks + indexes)

**Dependencies:** M1-1, M1-2, M1-3

---

#### Issue M2-2: `task_members` repository (creator + assignee)

**Mục tiêu (Chức năng #4):** Quản lý người liên quan task với role, future-proof cho multi-assignee/follower.

**Yêu cầu:**
- Migration 000002: tạo bảng `task_members` với PK composite + FK cascade + 2 indexes (xem §3.2)
- Repository methods:
  - `AddMember(ctx, taskID, userID, role string) error` — INSERT (idempotent nhờ PK)
  - `RemoveMember(ctx, taskID, userID, role string) error` — DELETE
  - `ListMembers(ctx, taskID string) ([]TaskMember, error)` — SELECT by task
  - `GetMemberByRole(ctx, taskID, role string) (userID string, err error)` — SELECT user_id WHERE task_id=? AND role=? LIMIT 1 (cho 'creator'/'assignee')
  - `SetAssignee(ctx, taskID, newAssigneeID string) error` — UPDATE in-place, fall back to INSERT if no edge exists
- Model `model.TaskMember`: `{TaskID, UserID, Role, CreatedAt}`
- Helper: `GetTask` (M2-1) JOIN task_members để lấy creator_id + assignee_id cho DTO

**Files:**
- `server/store/sqlstore/members.go` (mới)
- `server/store/sqlstore/members_test.go` (mới)
- `server/model/task_member.go` (mới)
- `server/store/sqlstore/migrations/000002_members.up.sql` (mới)

**Dependencies:** M2-1 (gần M2-3/M2-5 để test JOIN trong GetTask)

---

#### Issue M2-3: `task_reminders` repository

**Mục tiêu (Chức năng #6):** Nhắc nhở tách khỏi task, multi-ready, scheduler chỉ scan bảng nhỏ.

**Yêu cầu:**
- Migration 000003: tạo bảng `task_reminders` + FK cascade + index `idx_reminders_pending` (xem §3.3)
- Repository methods:
  - `SetReminder(ctx, taskID string, offsetMS int64) (TaskReminder, error)` — MVP enforce 1/task (UPSERT: delete existing + insert)
  - `ClearReminder(ctx, taskID string) error` — DELETE WHERE task_id=?
  - `ListReminders(ctx, taskID string) ([]TaskReminder, error)` — SELECT by task
  - `ListDueReminders(ctx, nowMs, graceMs int64) ([]DueReminder, error)` — JOIN query (xem §3.3 fire query) trả `[]DueReminder{ReminderID, TaskID, DueAt, OffsetMS, AssigneeID}`
  - `MarkReminderFired(ctx, reminderID string, firedAt int64) error` — UPDATE fired_at
- Model `model.TaskReminder`: `{ID, TaskID, OffsetMS, FiredAt, CreatedAt}` — thay `model.ReminderMetadata`
- `model.DueReminder: {ReminderID, TaskID, DueAt, OffsetMS, AssigneeID}`
- Helper: `GetTask` JOIN task_reminders để tính `reminder_offset` + `reminder_fired` cho DTO

**Files:**
- `server/store/sqlstore/reminders.go` (mới)
- `server/store/sqlstore/reminders_test.go` (mới)
- `server/model/task_reminder.go` (mới)
- `server/store/sqlstore/migrations/000003_reminders.up.sql` (mới)

**Dependencies:** M2-1, M2-2

---

#### Issue M2-4: `task_posts` repository (card tracking)

**Mục tiêu (Chức năng #7):** Track mọi post chứa card task, linh hoạt post chỗ khác.

**Yêu cầu:**
- Migration 000004: tạo bảng `task_posts` + FK cascade + UNIQUE(post_id) + index (xem §3.4)
- Repository methods:
  - `AddPost(ctx, taskID, postID, kind string) error` — INSERT (kind ∈ {channel, dm})
  - `ListPosts(ctx, taskID string) ([]TaskPost, error)` — SELECT by task
  - `GetPostByKind(ctx, taskID, kind string) (postID string, err error)` — SELECT post_id WHERE task_id=? AND kind=? LIMIT 1
  - `DeletePost(ctx, postID string) error` — DELETE (rare — vd khi post bị xóa trên server)
- Model `model.TaskPost`: `{ID, TaskID, PostID, Kind, CreatedAt}`
- Helper: `GetTask` JOIN task_posts để tính `channel_post_id` + `dm_post_id` cho DTO

**Files:**
- `server/store/sqlstore/posts.go` (mới)
- `server/store/sqlstore/posts_test.go` (mới)
- `server/model/task_post.go` (mới)
- `server/store/sqlstore/migrations/000004_posts.up.sql` (mới)

**Dependencies:** M2-1, M2-2

---

#### Issue M2-5: `task_comments` repository (ánh xạ thread Mattermost)

**Mục tiêu (Chức năng #5):** Task hoạt động như thread tin nhắn — comment là reply trong thread card task; bảng chỉ lưu ánh xạ.

**Yêu cầu:**
- Migration 000005: tạo bảng `task_comments` chỉ với `(id, task_id, post_id UNIQUE, author_id, created_at)` + FK cascade + index `(task_id, created_at)` (xem §3.5)
- Repository methods (mọi method nhận `context.Context`):
  - `LinkComment(ctx, taskID, postID, authorID string, createdAt int64) (TaskComment, error)` — INSERT ánh xạ khi hook catch reply trong thread
  - `ListComments(ctx, taskID string) ([]TaskComment, error)` — SELECT ánh xạ ORDER BY created_at (caller lấy content qua `GetPost(post_id)`)
  - `CountComments(ctx, taskID string) (int, error)` — `SELECT COUNT(*)` (cho card indicator)
  - `UnlinkComment(ctx, postID string) error` — DELETE WHERE post_id=? (khi post bị xóa)
- Model `model.TaskComment`: `{ID, TaskID, PostID, AuthorID, CreatedAt}` — **bỏ** `Content`, `UpdatedAt`
- Defensive: `GetPost(post_id)` trả nil (post bị xóa) → skip trong list comment khi render

**Files:**
- `server/store/sqlstore/comments.go` (mới)
- `server/store/sqlstore/comments_test.go` (mới)
- `server/model/task_comment.go` (mới — thay `model.Comment`)
- `server/store/sqlstore/migrations/000005_comments.up.sql` (mới)

**Dependencies:** M2-1, (hook ở issue mới M4-X)

---

#### Issue M2-6: `task_events` repository (audit log)

**Mục tiêu (Chức năng #8):** Audit trail atomic với mỗi transition.

**Yêu cầu:**
- Migration 000006: tạo bảng `task_events` + FK cascade + index `(task_id, created_at DESC)` (xem §3.6)
- Repository methods:
  - `AppendTaskEvent(ctx, e TaskEvent) error` — INSERT (id ULID tự sinh)
  - `ListTaskEvents(ctx, taskID string, limit int) ([]TaskEvent, error)` — SELECT WHERE task_id=? ORDER BY created_at DESC LIMIT ?
- Model `model.TaskEvent`: `{ID, TaskID, ActorID, EventType, FromValue, ToValue, CreatedAt}`
- Enum `event_type` giá trị: `created`, `status_changed`, `assigned`, `unassigned`, `due_changed`, `summary_changed`, `description_changed`, `reminder_set`, `reminder_cleared`, `commented`, `subtask_added`, `deleted`

**Files:**
- `server/store/sqlstore/events.go` (mới)
- `server/store/sqlstore/events_test.go` (mới)
- `server/model/task_event.go` (mới)
- `server/store/sqlstore/migrations/000006_events.up.sql` (mới)

**Dependencies:** M2-1

---

### MILESTONE M3 — Service layer rewrite

> Refactor `task.Service` dùng `Store` interface mới, thêm WithTx + audit hooks.

---

#### Issue M3-1: Store interface hoàn chỉnh (`server/store/store.go`)

**Mục tiêu:** Định nghĩa interface `Store` idiomatic SQL, mọi method nhận `context.Context`.

**Yêu cầu:**
- Interface `Store` tổng hợp mọi method từ M2-1 → M2-6 (xem §5 dưới)
- Mọi method nhận `context.Context` đầu tiên
- Method `WithTx(ctx, fn func(Store) error) error`
- Types: `ListQuery`, `PageResult`, `Scope` constants (mine/channel/all)
- Update `server/store/sqlstore/sqlstore.go` implement toàn bộ interface

**Files:**
- `server/store/store.go` (hoàn thiện)
- `server/store/sqlstore/sqlstore.go` (update)

**Dependencies:** M2-1, M2-2, M2-3, M2-4, M2-5, M2-6

---

#### Issue M3-2: Refactor `task.Service` dùng Store mới

**Mục tiêu:** Thay dependency từ `kvstore.KVStore` sang `store.Store`, đơn giản hóa logic.

**Yêu cầu:**
- Đổi `s.store kvstore.KVStore` → `s.store store.Store`
- `Create`: dùng `s.store.WithTx` → `CreateTask` + `AddMember(creator)` + `AddMember(assignee)` + `SetReminder(offset)` (nếu có due+offset) + `AppendTaskEvent(created)`. Atomic.
- `Assign`: `WithTx` → `RemoveMember(old,'assignee')` + `AddMember(new,'assignee')` + event `assigned`. Atomic.
- `SetStatus`: `WithTx` → `UpdateTask` + cascade-cancel subtasks (nếu cancel) + event `status_changed`.
- `Delete`: chỉ cần `DeleteTask` — FK cascade tự xoá members/reminders/posts/comments/events/subtasks. Bỏ ListKeys+loop+index-delete tay.
- `FireReadyReminders`: `s.store.ListDueReminders(now, grace)` — 1 query JOIN.
- `nextGlobalOrderKey`: `SELECT MAX(order_key)` — 1 query.
- `List`: `s.store.ListTasks(q)` — 1 query JOIN, push WHERE/ORDER/LIMIT.
- `SubtaskProgress`: `s.store.SubtaskProgress(parentID)` — 1 GROUP BY query.
- `Patch`: cập nhật field + `UpdateTask` (RETURNING) + event per field (`summary_changed`/`due_changed`/`description_changed`).
- `AddComment`: insert comment + event `commented` + bump UpdatedAt.
- `SetReminder`/`ClearReminder`: update reminder + event.
- **REST contract ổn định**: service vẫn trả `model.Task` (qua GetTask) để JSON response giữ shape cũ.

**Files:**
- `server/task/service.go` (rewrite lớn)
- `server/task/service_test.go` (rewrite — fake `Store` interface)

**Dependencies:** M3-1

---

#### Issue M3-3: Audit hooks trong Service layer

**Mục tiêu:** Mỗi transition task ghi 1 audit event atomic với change.

**Yêu cầu:**
- Mỗi method service thay đổi state gọi `s.store.AppendTaskEvent` **trong cùng tx với change chính**:
  - `Create` → event `created` (from=NULL, to=task JSON)
  - `SetStatus` → event `status_changed` (from/to = status)
  - `Assign` → event `assigned` (from/to = assignee_id); `unassign` → event `unassigned`
  - `Patch` → event per field (`due_changed`/`summary_changed`/`description_changed`) (from/to)
  - `Delete` → event `deleted` (from=task snapshot, to=NULL) — ghi **trước** khi cascade
  - `AddComment` → event `commented` (to = comment ID)
  - `SetReminder` → event `reminder_set`; `ClearReminder` → event `reminder_cleared`
  - `CreateSubtask` → event `subtask_added` trên parent
- Atomic: nếu event insert fail thì change cũng rollback (cùng tx)
- Test: mỗi transition → verify event tương ứng được ghi; rollback khi error → event cũng rollback

**Files:**
- `server/task/service.go` (thêm audit hooks — đã bắt đầu ở M3-2)
- `server/task/service_audit_test.go` (mới — test atomic audit)

**Dependencies:** M3-2, M2-6

---

### MILESTONE M4 — Integration & wiring

> Kết nối các thành phần: plugin wiring, atomic createTask, reminder job, events route.

---

#### Issue M4-1: Plugin wiring trong `OnActivate`

**Mục tiêu:** Khởi tạo SQLStore + chạy migrations khi plugin activate.

**Yêu cầu:**
- Trong `OnActivate`:
  ```go
  db, err := p.client.Store.GetMasterDB()
  if err != nil { return errors.Wrap(err, "get master db") }
  // Health check fail-fast
  ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
  defer cancel()
  if err := db.PingContext(ctx); err != nil {
      return errors.Wrap(err, "plugin db unreachable on activate")
  }
  sqlStore, err := sqlstore.New(db, dbType, "task_")
  if err != nil { return errors.Wrap(err, "init sqlstore") }
  if err := sqlStore.RunMigrations(p.API); err != nil { return err }
  p.taskStore = sqlStore
  p.taskService = task.NewService(sqlStore, &p.client.Log)
  ```
- Đọc `dbType` + connection string từ `p.client.Configuration.GetConfig().SqlSettings`
- Đổi `p.kvstore kvstore.KVStore` → `p.taskStore store.Store` trong Plugin struct
- `OnDeactivate`: không cần dọn dẹp gì (DB là của server, không phải plugin mở riêng)

**Files:**
- `server/plugin.go` (sửa)

**Dependencies:** M3-2

---

#### Issue M4-2: Atomic `createTask` handler (task + card posts trong 1 tx)

**Mục tiêu:** Wrap handler `createTask` trong `WithTx` để crash giữa chừng không để lại card mồ côi.

**Yêu cầu:**
- `POST /tasks` và dialog submit `submitNewTaskDialog` wrap trong `s.store.WithTx`:
  ```
  WithTx:
    1. service.Create (task + members + reminder + event)  ← trong tx
    2. postCard(channelID) → channelPostID                  ← tạo post (ngoài tx nhưng tracked)
    3. postCardDM(assigneeID) → dmPostID                    ← tạo post
    4. AddPost(task, channelPostID, 'channel')              ← trong tx
    5. AddPost(task, dmPostID, 'dm')                        ← trong tx
  ```
- **Lưu ý**: `p.API.CreatePost` không tham gia tx DB (post là API call riêng). Strategy: tạo post **trước**, nếu bất kỳ step nào fail → tx rollback (task không persist), post mồ côi sẽ bị xóa bằng cách best-effort (try `p.API.DeletePost` cho mỗi postID đã tạo trước khi return error). Hoặc alternative: commit tx trước rồi post sau, post-fail chỉ log (card mồ côi chấp nhận vì không gắn với task).
- Chọn strategy & document rõ trong code comment. **Khuyến nghị**: tạo task trong tx trước (commit), post card sau (post-fail chỉ log, task vẫn còn). Lý do: task là source of truth; card có thể rebuild/tạo lại sau qua endpoint riêng nếu cần.
- `updateCard` giờ `ListPosts(taskID)` rồi update mỗi post (không còn 2 cột cứng).
- Áp dụng tương tự cho `createSubtask`.

**Files:**
- `server/api.go` (sửa `createTask`, `createSubtask`)
- `server/dialog.go` (sửa `submitNewTaskDialog`)
- `server/message_attachment.go` (sửa `updateCard` dùng `ListPosts`)

**Dependencies:** M3-2, M4-1

---

#### Issue M4-3: Reminder job dùng `ListDueReminders`

**Mục tiêu:** Cập nhật `runReminderJob` dùng query mới (1 query JOIN thay ListKeys+GetReminder+GetTask).

**Yêu cầu:**
- `task.Service.FireReadyReminders` (M3-2 đã refactor) giờ gọi `s.store.ListDueReminders(now, grace)`
- `runReminderJob` (`server/job.go`) giữ nguyên logic DM/mark fired, chỉ đổi signature nếu cần
- `MarkReminderFired` giờ nhận `reminderID` (không phải `taskID`) — vì reminder có id riêng
- `fireReminderDM` giữ nguyên
- Test: tạo task có reminder → đến hạn → DM fires → reminder marked fired

**Files:**
- `server/job.go` (sửa nhẹ — signature)
- `server/task/service.go` (FireReadyReminders đã refactor ở M3-2)

**Dependencies:** M3-2, M2-3

---

#### Issue M4-4: REST endpoint `GET /tasks/:id/events` (audit trail)

**Mục tiêu:** Expose audit trail qua API cho UI render timeline sau.

**Yêu cầu:**
- Route `GET /tasks/:id/events?limit=50` (authenticated)
- Permission: user phải được xem task (dùng cùng rule `permission.CanUserCommentTask` hoặc rule view)
- Response: `[]TaskEvent` JSON, sort by `created_at DESC`
- Test: tạo task + change status + assign → GET events → 3 events returned đúng thứ tự

**Files:**
- `server/api.go` (thêm route + handler `listTaskEvents`)

**Dependencies:** M2-6, M4-1

---

#### Issue M4-5: `MessageHasBeenPosted` hook — catch thread reply → link comment

**Mục tiêu (Chức năng #5 — Hybrid comment-as-thread):** Khi user reply trong thread của card task, hook tự catch và ghi ánh xạ vào `task_comments`.

**Yêu cầu:**
- Implement `MessageHasBeenPosted(c *plugin.Context, post *model.Post)` trên `Plugin`
- Logic:
  1. Nếu `post.RootId == ""` → bỏ qua (không phải reply)
  2. Tra cứu `task_posts` WHERE `post_id = post.RootId` → nếu không có → bỏ qua (root post không phải card task)
  3. Nếu có → lấy `task_id` → `LinkComment(taskID, post.Id, post.UserId, post.CreateAt)`
  4. Bump `task.UpdatedAt` (để WS seq advance — reason như comment cũ)
  5. Append `task_event` type `commented` (snapshot content = post.Message tại thời điểm này, không phụ thuộc edit sau)
  6. Notification: bỏ qua — Mattermost đã notify native (thread reply + @mention). Plugin chỉ gửi DM cho participants nếu họ **không** trong kênh đó (optional, defer)
- Guard: nếu post là của bot (post.UserId == botUserID) → bỏ qua (tránh loop khi bot post card update)
- Hook phải return nhanh — không block channel. Nếu `LinkComment` fail → log + return (post vẫn tồn tại trong Mattermost, không lost)
- Test:
  - User reply trong thread card → `task_comments` có 1 row mới
  - User post reply ở thread không phải card → không có row mới
  - Bot post (update card) → không trigger (guard botUserID)

**Files:**
- `server/hooks.go` (mới — `MessageHasBeenPosted`)
- `server/plugin.go` (Plugin struct đã embed `plugin.MattermostPlugin`, hook auto-wired)

**Dependencies:** M2-1 (GetTask), M2-4 (task_posts lookup), M2-5 (LinkComment), M2-6 (events)

---

### MILESTONE M5 — Cleanup & verification

> Dọn dẹp code cũ, update tests, build + PLAN.md.

---

#### Issue M5-1: Xoá package `server/store/kvstore/`

**Mục tiêu:** Xóa toàn bộ code KV cũ sau khi đã migrate sang SQL.

**Yêu cầu:**
- Xóa files:
  - `server/store/kvstore/kvstore.go`
  - `server/store/kvstore/client.go`
  - `server/store/kvstore/atomic.go`
  - `server/store/kvstore/task.go`
  - `server/store/kvstore/kvstore_test.go`
  - `server/store/kvstore/store_test.go`
- Xóa toàn bộ reference tới `kvstore` package (grep `kvstore\.` trong `server/`)
- Xóa `model.ReminderMetadata` (thay bằng `model.TaskReminder`)
- Verify build OK (`make server`)

**Files:**
- Xóa `server/store/kvstore/` (toàn bộ)
- `server/model/reminder.go` (xóa)

**Dependencies:** M4-1, M4-2, M4-3 (tất cả handler đã không còn dùng kvstore)

---

#### Issue M5-2: Update toàn bộ tests

**Mục tiêu:** Cập nhật tests dùng `Store` interface mới.

**Yêu cầu:**
- `server/task/service_test.go` — fake `Store` interface (M3-2 đã bắt đầu, hoàn thiện)
- `server/api_test.go` — `fakeTaskStore` implement `Store` mới
- `server/integration_test.go` — `remStore` implement `Store` mới
- `server/store/sqlstore/*_test.go` — sqlite in-memory coverage đầy đủ:
  - CRUD mỗi bảng
  - List filter/pagination
  - CountByStatus (Kanban progress)
  - Search ILIKE
  - reminder JOIN scan
  - members/posts CRUD
  - events atomic + rollback
  - FK cascade delete (delete task → mọi bảng con tự xóa)
  - WithTx commit/rollback
  - migration idempotent 2 lần
- `make test` xanh với `-race`

**Files:**
- `server/task/service_test.go`
- `server/api_test.go`
- `server/integration_test.go`
- `server/store/sqlstore/*_test.go`
- `server/dialog_http_test.go`

**Dependencies:** M5-1

---

#### Issue M5-3: Update `message_attachment.go` helpers

**Mục tiêu:** Các helper `subtaskProgress`, `commentCount` dùng SQL aggregation thay vì load-all.

**Yêu cầu:**
- `subtaskProgress(taskID)` → `s.store.SubtaskProgress(taskID)` (1 GROUP BY query, không N+1)
- `commentCount(taskID)` → `s.store.CountComments(taskID)` (`SELECT COUNT(*)`, không `ListComments` rồi `len`)
- `updateCard` dùng `ListPosts` (M4-2 đã làm)

**Files:**
- `server/message_attachment.go` (sửa)

**Dependencies:** M4-1, M5-1

---

#### Issue M5-4: Update `PLAN.md` + documentation

**Mục tiêu:** Đồng bộ tài liệu thiết kế với thực tế SQL.

**Yêu cầu:**
- `PLAN.md` §4 "Thiết kế dữ liệu trên KVStore" → đổi tiêu đề + nội dung sang "Thiết kế dữ liệu trên SQL"
- Mô tả 6 bảng quan hệ mới + lý do (tham chiếu `docs/SQL_MIGRATION_PLAN.md`)
- §Rủi ro: bỏ phần giới hạn scale KV; thêm ghi chú dialect postgres/mysql + sqlite test
- Thêm note về transaction trong handler `createTask` (strategy commit-task-then-post)
- Decisions Log: thêm entry "Đợt review #10 — chuyển KV sang SQL"

**Files:**
- `PLAN.md` (sửa)

**Dependencies:** M5-1

---

#### Issue M5-5: Build & verification cuối

**Mục tiêu:** Build xanh + test thủ công.

**Yêu cầu:**
- `make dist` build OK
- `go test ./... -race` xanh
- Test thủ công E2E:
  1. Tạo task → post card channel + DM → card update khi đổi status
  2. Reminder đến hạn → DM fires
  3. Subtask → SubtaskProgress 1 GROUP BY
  4. `/task search` ILIKE hoạt động
  5. `GET /tasks/:id/events` trả audit trail đầy đủ
  6. Delete task → FK cascade xoá mọi bảng con (verify trong DB)
  7. Migration idempotent: restart plugin 2 lần không lỗi
  8. List với >100 task + filter/pagination hoạt động đúng

**Files:** (none — verification only)

**Dependencies:** M5-2, M5-3, M5-4

---

## 5. Store interface tổng hợp (sau khi hoàn thiện M3-1)

```go
package store

type Store interface {
    // Task (M2-1)
    CreateTask(ctx context.Context, task Task) (Task, error)
    GetTask(ctx context.Context, id string) (*Task, error)
    GetTask(ctx context.Context, id string) (*Task, error)
    UpdateTask(ctx context.Context, task Task) (Task, error)
    TouchTaskUpdatedAt(ctx context.Context, id string, ts int64) error
    DeleteTask(ctx context.Context, id string) error
    ListTasks(ctx context.Context, q ListQuery) (PageResult, error)
    CountTasksByStatus(ctx context.Context, q ListQuery) (map[string]int, error)
    SearchTasks(ctx context.Context, keyword string, limit int) ([]Task, error)
    ListSubtasks(ctx context.Context, parentID string) ([]Task, error)
    SubtaskProgress(ctx context.Context, parentID string) (done, total int, err error)

    // Members (M2-2)
    AddMember(ctx context.Context, taskID, userID, role string) error
    RemoveMember(ctx context.Context, taskID, userID, role string) error
    ListMembers(ctx context.Context, taskID string) ([]TaskMember, error)
    GetMemberByRole(ctx context.Context, taskID, role string) (userID string, err error)

    // Reminders (M2-3)
    SetReminder(ctx context.Context, taskID string, offsetMS int64) (TaskReminder, error)
    ClearReminder(ctx context.Context, taskID string) error
    ListReminders(ctx context.Context, taskID string) ([]TaskReminder, error)
    ListDueReminders(ctx context.Context, nowMs, graceMs int64) ([]DueReminder, error)
    MarkReminderFired(ctx context.Context, reminderID string, firedAt int64) error

    // Posts (M2-4)
    AddPost(ctx context.Context, taskID, postID, kind string) error
    ListPosts(ctx context.Context, taskID string) ([]TaskPost, error)
    GetPostByKind(ctx context.Context, taskID, kind string) (postID string, err error)
    DeletePost(ctx context.Context, postID string) error

    // Comments (M2-5) — Hybrid: comment = thread reply, chỉ lưu ánh xạ
    LinkComment(ctx, taskID, postID, authorID string, createdAt int64) (TaskComment, error)  // hook gọi khi có reply trong thread
    ListComments(ctx, taskID string) ([]TaskComment, error)                                  // trả ánh xạ, content lấy qua GetPost
    UnlinkComment(ctx, postID string) error                                                  // khi post bị xóa
    CountComments(ctx, taskID string) (int, error)

    // Events (M2-6)
    AppendTaskEvent(ctx context.Context, e TaskEvent) error
    ListTaskEvents(ctx context.Context, taskID string, limit int) ([]TaskEvent, error)

    // Transaction (M3-1)
    WithTx(ctx context.Context, fn func(Store) error) error
}
```

---

## 6. Cải tiến đi kèm (Tier 1, đã tích hợp trong các issue)

| Cải tiến | Implement ở issue |
|---|---|
| Composite indexes | M2-1, M2-2, M2-3, M2-4, M2-5, M2-6 (trong migration mỗi bảng) |
| `context.Context` xuyên suốt | M3-1 (Store interface), M3-2 (service) |
| Health check `db.PingContext` | M4-1 (plugin wiring) |
| `RETURNING *` cho `UpdateTask` | M2-1 |
| Pagination `total` + `has_more` | M2-1 (`PageResult`) |
| `WithTransaction` propagation | M3-1, M3-2, M4-2 |
| Audit `task_events` | M2-6, M3-3, M4-4 |

---

## 7. Phạm vi KHÔNG thay đổi (giữ rủi ro thấp)

- **REST routes + JSON shape của Task/Comment** (qua `Task` DTO) — không đổi, webapp không phải sửa
- **Webapp (`webapp/`)** — không đụng
- **Slash command, dialog, notification rules, permission rules** — không đụng
- **`cluster.Schedule` reminder job** — giữ; chỉ internals `FireReadyReminders` đổi
- **Hard delete + cascade** — giờ có transaction FK cascade thật (PLAN đã chốt)
- **Deferred (sau milestone này):**
  - Full-text search tsvector/FULLTEXT (MVP dùng ILIKE đủ)
  - Benchmark test (sẽ thêm nếu phát hiện chậm)
  - Multi-assignee/follower/reminder UI (schema đã sẵn sàng, app-layer chưa enable)
  - Soft-delete (PLAN chốt hard delete)

---

## 8. Thứ tự ưu tiên (critical path)

```
M1-1 ─┬─► M1-2 ─┬─► M2-1 ─┬─► M2-2 ──► M2-3 ──┐
M1-3 ─┘         │         ├─► M2-4 ──► M2-5   ├─► M3-1 ─► M3-2 ─► M3-3
                │         └─► M2-6 ────────────┘             │
                │                                            ├─► M4-1 ─┬─► M4-2
                │                                            │         ├─► M4-3
                │                                            │         └─► M4-4
                │                                            │
                └────────────────────────────────────────────┴─► M5-1 ─► M5-2
                                                                            ├─► M5-3
                                                                            ├─► M5-4
                                                                            └─► M5-5
```

**Đường tới giá trị nhanh nhất:** M1 → M2-1 (task entity + list) → M3 → M4-1 (wiring) = plugin đã chạy với SQL. M2-2/3/4/5/6 có thể song song. M4-2/M4-4 là enhancement. M5 là cleanup.

---

## 9. Dependencies bổ sung

| Package | Vai trò | Version |
|---|---|---|
| `github.com/Masterminds/squirrel` | Query builder (Boards dùng) | latest |
| `github.com/mattermost/morph` | Migrations engine | latest |
| `github.com/mattermost/morph/drivers/postgres` | Postgres driver cho morph | latest |
| `github.com/mattermost/morph/drivers/mysql` | MySQL driver cho morph | latest |
| `github.com/mattn/go-sqlite3` | SQLite cho test (build tag `//go:build test`) | latest |

---

## 10. Data migration KV → SQL

**Không cần.** Plugin chưa có production data. Nếu sau này có data production cần migrate:
- Thêm migration Go riêng (`data_migrations.go` kiểu Boards, gated bởi schema version + completion flag)
- Đọc KV cũ → insert vào SQL
- Đánh dấu completion trong `task_events` hoặc bảng system settings
- **Ngoài scope milestone này.**
