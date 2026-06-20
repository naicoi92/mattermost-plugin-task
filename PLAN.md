# Kế hoạch triển khai Mattermost Plugin "Task" (mô phỏng Lark Suite Tasks)

> Trạng thái: **Hoàn thiện** — chốt phạm vi & cách tiếp cận với người dùng. Sẵn sàng review.

## Context (Bối cảnh)

Người dùng đang dùng Mattermost và muốn xây dựng plugin "Task" tái hiện trải nghiệm quản lý công việc của **Lark Suite (Feishu) Tasks** — đưa năng lực quản lý task hợp tác cao (task, subtask, assignee, comment, reminder...) vào ngay trong luồng kênh chat, không cần rời khỏi Mattermost.

Dự án bắt đầu từ thư mục trống `mattermost-plugin-task`, dùng `mattermost-plugin-starter-template` làm khung sườn.

## Quyết định đã chốt (từ người dùng)

| Quyết định | Lựa chọn |
|---|---|
| **Phạm vi MVP** | ✅ Task + Assignee cơ bản · ✅ Subtask & Comment · ✅ Reminder & Thông báo · ✅ **Kanban board** (theo status). ⏸ Hoãn: Tasklist, Follower vai trò, Section, Custom Field, Attachment, Repeat rule, AI agent. |
| **Cách tiếp cận UI** | **Hybrid** — Slash command `/task` (+ `/task new` mở dialog) + nút channel header (mở RHS) + **nút "New Task" trong composer (desktop, #107)** + interactive message card + sidebar React + bảng Kanban. Bố cục: **Quick List → RHS**, **New Task → popup dialog**, **Task Detail → RHS**, **Kanban → modal gần full màn**. |
| **Status / cột Kanban** | Cố định **4 cột**: To Do · In Progress · Done · Cancelled. |
| **Subtask trên Kanban** | Hiện thành **thẻ riêng** (status độc lập) + vẫn lồng trong Task Detail của task cha. |
| **Ngôn ngữ UI** | **i18n** (Tiếng Việt + Tiếng Anh) qua `registerTranslations`. |
| **Hỗ trợ Mobile** | **Graceful degradation** — mobile dùng slash command + task card + **Interactive Dialog**; **Task Detail & Quick List trên mobile dùng Interactive Dialog** (`OpenInteractiveDialog`, cross-platform); RHS & Kanban modal là tăng cường desktop. Nguyên tắc: mọi thao tác cốt lõi làm được qua chat. |
| **Phạm vi task** | **Hybrid** — task lưu `channel_id` (nơi khởi tạo, tuỳ chọn) NHƯNG vẫn xuất hiện toàn cục trong "My Tasks" của assignee. |

## Thuật ngữ (làm rõ “List” vs “Tasklist”)

Rất dễ nhầm vì cả hai đều dùng chữ “list”. Đây là 2 thứ hoàn toàn khác nhau:

| Khái niệm | Bản chất | Ví dụ | Có trong MVP? |
|---|---|---|---|
| **Quick List / List (Kanban)** | **VIEW — cách HIỂN THỊ** task. Không phải dữ liệu, chỉ là góc nhìn. | “My Tasks” (việc của tôi), “Channel Tasks”, cột To Do/In Progress/Done | ✅ Có |
| **Tasklist** (khái niệm Lark) | **ENTITY — một “dự án/nhóm” chứa task**. Là dữ liệu: có tên, thành viên, vai trò owner/editor/viewer. Task *thuộc về* tasklist. | Dự án "Sprint 42", "Marketing Q3", "Onboarding" | ⏸ Hoãn |

**So sánh thực tế:**
- **List (Quick List/Kanban)** giống *chế độ xem* trong file explorer (xem theo “của tôi”, “kênh”, “theo status”) — chỉ là cách sắp xếp hiển thị.
- **Tasklist** giống một *thư mục/dự án* bạn tự tạo để gom task: vd bạn lập tasklist **“Ra mắt web mới”**, thêm 10 task vào đó; tasklist có 3 thành viên với vai trò khác nhau. Nó giống *Board* của Trello, *Project* của Asana, *Danh sách* của Microsoft To Do.

**Trong MVP hiện tại**, task được tổ chức theo **scope (Cá nhân/Kênh)** và **status (4 cột Kanban)** — chưa có “dự án” để gom task theo chủ đề. Nếu thêm **Tasklist**, bạn sẽ có thêm chiều tổ chức theo dự án (task thuộc dự án nào), độc lập với channel. Vì vậy Tasklist bị **hoãn** — nó là tính năng riêng, không trùng với Quick List/Kanban.

---

## Phần 1 — Tổng quan chức năng Lark Suite Tasks (đã nghiên cứu)

Nguồn: `open.feishu.cn/document/task-v2/overview`, skill `lark-task` của `larksuite/cli`.

**Các thực thể cốt lõi**: Task (summary, description, due, start, completed_at, completion_mode or-sign/countersign), Subtask (task con có `parent`), Assignee (nhiều người, quyền sửa/hoàn thành), Follower (nhận thông báo), Tasklist (dự án, owner/editor/viewer), Comment, Section (nhóm tùy chỉnh), Custom Field (number/member/datetime/select), Attachment, Reminder (tối đa 1/task, cần due), Repeat Rule (cần due).

**Khái niệm then chốt**: *My Tasks* (task mình là assignee), *Get Related Tasks*, *Completion Mode* (or-sign vs countersign), *Idempotent invocation* (`client_token`), *Partial update* (PATCH + `update_fields`), pagination (`page_size` ≤100 + `page_token` cursor), time = timestamp-ms dạng string.

**Toàn bộ thao tác**: `+create/+update/+complete/+reopen/+assign/+followers/+reminder/+comment/+get-my-tasks/+get-related-tasks/+search/+upload-attachment` (Task); `+tasklist-create/+tasklist-task-add/+tasklist-members` (Tasklist); Section/Custom Field/Agent CRUD.

---

## Phần 2 — Kiến trúc & Tận dụng starter template

Plugin có 2 thành phần: **Server (Go)** chạy subprocess qua RPC + hooks, và **Webapp (React/Redux)**. Lưu trữ: **chỉ KVStore** (bảng `PluginKeyValueStore`, key ≤150 ký tự, giá trị JSON) — không tạo bảng DB riêng.

Template `mattermost-plugin-starter-template` đã cung cấp sẵn các pattern ta sẽ tái sử dụng trực tiếp:

| Pattern có sẵn | File template | Tái dùng cho |
|---|---|---|
| `Plugin` struct + `OnActivate`/`OnDeactivate` + router | `server/plugin.go` | Đăng ký command, khởi tạo store, router API |
| `cluster.Schedule` background job (cluster-aware, chạy trên 1 node) | `server/plugin.go` + `server/job.go` | **Scheduler reminder** — quét task đến hạn định kỳ |
| `ServeHTTP` + router `/api/v1` + middleware `MattermostAuthorizationRequired` | `server/api.go` | REST API + callback từ interactive buttons |
| Package `command` với `Register`/`Handle` + autocomplete | `server/command/command.go` | Slash command `/task` và các subcommand |
| Interface `KVStore` + `Client` qua `pluginapi.Client.KV` | `server/store/kvstore/` | Lớp lưu trữ task/comment/index |
| `configuration` + `settings_schema` | `server/configuration.go` + `plugin.json` | Cài đặt: mặc định reminder offset, bot... |
| `Plugin.initialize(registry, store)` | `webapp/src/index.tsx` | Channel header button, RHS sidebar |

> ✅ Quan trọng: **reminder scheduler sẽ dùng `cluster.Schedule`** của template (đã cluster-aware, không cần tự viết goroutine/ticker) — thay thế ý tưởng ban đầu.

### 2.1 Phụ thuộc & phiên bản mới nhất (theo phản hồi)

| Thành phần | Phiên bản / nguồn mới nhất | Ghi chú |
|---|---|---|
| **Server plugin SDK** | `github.com/mattermost/mattermost/server/public/plugin` + `.../pluginapi` | `mattermost-plugin-api` (repo riêng) **đã archived** → dùng trong monorepo. Template đã import đúng. |
| **min_server_version** | `"10.7.0"` (trở lên) | Đảm bảo tương thích Go 1.23+ serialization; dùng các API mới (tooltip button, custom error). |
| **Go** | 1.23+ | |
| **Webapp React** | React 18.2 (host cung cấp qua webpack externals; các plugin đang dần nâng cấp React 19) | KHÔNG bundle react/redux/react-redux/react-router-dom — khai báo `externals` trong `webpack.config.js`. |
| **Node / npm (dev)** | Node 24 / npm 11 | |
| **Drag-and-drop (Kanban)** | `@dnd-kit/core` + `@dnd-kit/sortable` (bản mới nhất) | Thay `react-beautiful-dnd` (đã ngừng phát triển). |
| **HTTP router** | `github.com/gorilla/mux` (như template) | |

### 2.2 Registry method webapp sẽ dùng (tên chính xác, ref: webapp-reference)

| Chức năng | Registry method |
|---|---|
| RHS (Quick List + Task Detail) | `registerRightHandSidebarComponent(component, title)` |
| Nút channel header mở RHS | `registerChannelHeaderButtonAction(icon, action, dropdownText, tooltipText)` |
| **Nút "New Task" trong composer (desktop)** | `registerPostEditorActionComponent(component)` — render trong FormattingBar của message composer (#107); dùng class `AdvancedTextEditor__action-button` của host + tooltip `react-bootstrap` `{OverlayTrigger, Tooltip}`. Mobile không render slot này → dùng `/task new`. |
| **Kanban (modal gần full màn)** | `registerRootComponent(component)` + mount modal lớn (gần full-screen) qua redux/modal; nút `📊 Kanban` mở modal. KHÔNG dùng custom route. |
| Task card (render React cho post task) | `registerPostCardTypeComponent("custom_task", component)` (tuỳ chọn nâng cao; mặc định dùng message attachment) |
| Real-time Kanban ↔ RHS | `registerWebSocketEventHandler(event, handler)` + `registerReducer` nhận sự kiện `p.API.PublishWebSocketEvent(...)` từ server |
| Modal New Task / Kanban | `registerRootComponent(component)` + redux modal (hoặc server `OpenInteractiveDialog`) |
| Đa ngôn ngữ | `registerTranslations(getTranslationsForLocale)` + `en.json`/`vi.json` |

### 2.3 API server then chốt (ref: server-reference)
`p.API` / `pluginapi.Client`: `CreateBot`/`EnsureBot` (tạo bot user cho DM — trong `OnActivate`, khai báo trong `plugin.json` `server.runtime_config`/bot), `CreatePost`, `SendEphemeralPost`, `UpdatePost` (dùng `ChannelPostID`/`DMPostID` để update card), `OpenInteractiveDialog`, `GetDirectChannel`/`CreateDirectChannel` (DM với bot), `KV.Set`(+`SetAtomic`/`SetAtomicWithRetries`)/`Get`/`Delete`/`ListKeys`, `RegisterCommand`, `PublishWebSocketEvent`, `UploadFile`. Reminder qua `cluster.Schedule`.

> **Bot account (chốt — theo doc bot-accounts)**: plugin tạo bot qua **`p.API.EnsureBot(&model.Bot{...})` trong `OnActivate`** (đây là cách chính thức cho plugin, không phụ thuộc manifest). Lưu `botUserID`; mọi DM/card/task post **dùng bot làm tác giả**. Bots do plugin tạo dùng plugin ID làm owner.

---

## Phần 3 — Ánh xạ khái niệm Lark → Mattermost

| Lark | Mattermost |
|---|---|
| User (`ou_xxx`) | Mattermost `userId` |
| Chat (group) | Mattermost `channelId` |
| Assignee | User — gán qua interactive dialog user picker hoặc `@mention` |
| Follower *(hoãn)* | — |
| Reminder notification | `CreatePost` / `SendEphemeralPost` / DM bot |
| Task link (`url`) | Deep link tới task detail trong sidebar/modal |
| My Tasks | Truy vấn `idx:u:{uid}:assigned` |
| Permission | **Tách vai**: Creator (edit/delete/assign/đặt due) · Assignee (đổi status/comment/complete/cancel). Xem mục **5.4 Phân quyền**. |

---

## Phần 4 — Thiết kế dữ liệu trên KVStore

KVStore không có query → thiết kế **entity store + inverted index** (slice taskID đã sắp xếp theo `created_at`).

### 4.1 Lược đồ key (MVP — tối ưu KVStore theo review)

Nguyên tắc: **key-per-edge** (mỗi quan hệ = 1 key riêng) → **ghi rẻ, không tranh chấp CAS**; truy vấn dùng `KV.ListKeys(prefix)`. **KHÔNG** lưu `[]taskID` lớn trong 1 key (tránh O(n) marshal + CAS retry khi đông user). TaskID = **ULID** (sortable, không dùng `seq` counter → không hotspot).

```
# Entity
  t:{taskID}                       -> Task JSON

# Comment: 1 key/comment (key-per-comment; ULID để sắp xếp theo thời gian)
  t:{taskID}:c:{commentULID}      -> Comment JSON        (thêm comment = 1 Set, không CAS)

# Subtask membership (key-per-edge)
  idx:t:{parentTaskID}:sub:{taskID}  -> marker

# Membership indexes (key-per-edge) — query bằng ListKeys(prefix)
  idx:u:{userId}:assigned:{taskID}  -> marker        (user được gán)
  idx:u:{userId}:created:{taskID}   -> marker        (user tạo)
  idx:ch:{channelId}:task:{taskID}  -> marker        (task thuộc channel)
  idx:all:task:{taskID}             -> marker        (index toàn cục cho /task list all)

# Reminder index (riêng — scheduler KHÔNG quét toàn bộ task)
  idx:reminder:{taskID}  -> {due_ms, offset_ms, assignee_id}   (due!=nil && status∈{todo,in_progress} && !fired)

# Kanban ordering: fractional index (OrderKey string trong entity)
```

### 4.2 Model (Go DTO)
```go
type Task struct {
    ID             string   // ULID (toàn cục, sortable, không dùng seq counter)
    Summary        string
    Description    string
    ChannelID      string   // nơi khởi tạo (hybrid scope); **subtask kế thừa ChannelID từ task cha**
    CreatorID      string
    AssigneeID     string   // MVP: **chỉ 1 assignee**; multi-assignee + completion_mode hoãn
    ChannelPostID  string   // post_id của task card trong channel (update card khi đổi status)
    DMPostID       string   // post_id của card DM gửi assignee (nếu có)
    Due            *int64   // timestamp ms, nil = không có hạn
    IsAllDay       bool
    Status         string   // "todo" | "in_progress" | "done" | "cancelled"; done ⇔ CompletedAt != nil
    OrderKey       string   // fractional index (midpoint string) — **toàn cục** (rank chung, không theo cột); sắp xếp Kanban theo OrderKey
    CompletedAt    *int64
    CancelledAt    *int64
    ParentTaskID   string   // subtask: id cha, rỗng nếu task gốc
    ReminderOffset *int64   // ms trước due (nil = không nhắc)
    ReminderFired  bool
    CreatedAt      int64
    UpdatedAt      int64
}

type Comment struct {
    ID        string   // ULID
    UserID    string
    Content   string
    CreatedAt int64
    UpdatedAt int64
}
```

### 4.3 Concurrency, atomic & truy vấn (sửa theo review)
- **KHÔNG dùng `KV.CompareAndSet` (đã deprecated).** Dùng `client.KV.Set(key, value, pluginapi.SetAtomic(oldValue))` cho ghi có điều kiện; hoặc helper `SetAtomicWithRetries` cho read-modify-write tự retry khi conflict.
- **Index = key-per-edge**: thêm/xoá quan hệ = 1 `Set`/`Delete` (không CAS, không contention) → phù hợp đông user. Truy vấn “tasks của user” = `ListKeys("idx:u:{uid}:assigned:")` → load entity.
- **Reminder**: scheduler chỉ `ListKeys("idx:reminder:")` + đọc value nhỏ `{due, offset}` → fire khi đến hạn → xoá key + set `ReminderFired`. **Không load toàn bộ task entity** → scalable tới hàng chục nghìn task.
- **Comment**: thêm = 1 key mới (không conflict); list = `ListKeys("t:{taskID}:c:")` (sắp xếp theo ULID).
- **Kanban reorder**: `OrderKey` fractional index — kéo thẻ chỉ cần cập nhật **1 thẻ** (midpoint giữa 2 key lân cận); rebalance cục bộ khi hiếm khi hết khoảng trống.
- **Phân trang**: sau ListKeys → load entity → lọc trong memory (status/due/assignee) → cắt trang (cursor).
- **Xoá = HARD DELETE cascade** (sửa theo review — tránh quét tìm subtask mồ côi): **thứ tự xoá chặt chẽ**: (1) subtask (ListKeys `idx:t:{id}:sub:` → xoá đệ quy) → (2) comment (ListKeys `t:{id}:c:`) → (3) **xoá index theo key đầy đủ đã biết từ entity** (KHÔNG ListKeys — đã biết `AssigneeID`/`CreatorID`/`ChannelID`): `idx:u:{AssigneeID}:assigned:{id}`, `idx:u:{CreatorID}:created:{id}`, `idx:ch:{ChannelID}:task:{id}` (nếu ChannelID != ""), `idx:all:task:{id}`, `idx:reminder:{id}` → (4) xoá entity `t:{id}`. Nếu crash giữa chừng chỉ thừa vài key rác (chấp nhận, read defensive).
- **Giảm N+1 (review #4)**: `ListKeys` + Get từng entity là N+1; paging theo **ULID time-range** (ListKeys từ taskID start→end) giảm số key duyệt. **KHÔNG dùng in-memory cache trong MVP** (review #5: cluster nhiều node → stale, chưa có invalidation; N+1 ở ~2.000 task/user chưa đáng kể). Nếu cần, chỉ thêm sau khi có benchmark + invalidation qua WebSocket.
- **Self-healing read (review #6/#8)**: mọi read path **defensive** — nếu `Get(entity)` trả về not-found (do crash giữa chừng hard-delete để lại vài marker rác) thì **bỏ qua thẻ đó** (không crash). Rác hiếm và vô hại; **không tự dọn dẹp phức tạp / không GC** (chấp nhận tolerate; đã chốt bỏ GC).
- **Verify API (đã check source `server/public/pluginapi/kv.go`)**: `client.KV.Set(key, value, pluginapi.SetAtomic(oldValue))` và `SetAtomicWithRetries(key, func(old []byte)(any,error))` **đều tồn tại**. `ListKeys(page, count int, pluginapi.WithPrefix(prefix))` — cần phân trang (KVList trả theo page). Lưu ý `ListKeys` lọc prefix **client-side** nên với dataset lớn sẽ chậm → ghi nhận ở mục *Rủi ro*.

---

## Phần 5 — Chức năng người dùng (User Operations & Features)

Phần này mô tả **từng thao tác người dùng** và **tính năng chi tiết** cho thao tác đó (góc nhìn user). Mỗi thao tác có: cách gọi (UI trigger), đầu vào, hành vi, kết quả/thông báo, và phân quyền. Bảng tổng quan ở đầu, chi tiết theo nhóm ở dưới.

### 5.0 Bảng tổng quan các thao tác người dùng (MVP)

| # | Thao tác | Cách gọi (UI) | Tóm tắt chức năng |
|---|---|---|---|
| 1 | **Tạo task** | `/task add`, nút ➕ New Task, action *"Tạo task từ tin nhắn"* | Tạo task mới có assignee, due, mô tả; thông báo người được gán |
| 2 | **Liệt kê/Lọc task** | `/task list`, tab sidebar My Tasks / Channel Tasks | Xem & lọc theo scope, trạng thái, assignee, hạn |
| 3 | **Xem chi tiết task** | `/task show`, click task, nút Open | Toàn bộ thông tin + subtask + comment + reminder |
| 4 | **Sửa task** | `/task edit`, nút sửa trong chi tiết | Cập nhật từng trường (partial) |
| 5 | **Đổi status (Kanban)** | kéo-thả thẻ, `/task status` | Di chuyển task giữa các cột trạng thái (todo/in_progress/done) |
| 6 | **Hoàn thành / Hủy** | nút `[✓ Done]`/`[🚫 Cancel]`, `/task done`/`/task cancel` | Đánh dấu done (thông báo) hoặc cancelled |
| 7 | **Xoá task** | `/task delete`, menu chi tiết | Xoá task + subtask + comment (cascade) |
| 8 | **Gán / Bỏ gán** | nút `[👤 Assign]`, `/task assign` | Thêm/bớt người làm; DM khi mới gán |
| 9 | **Tạo / xem subtask** | nút `[➕ Subtask]`, `/task subtask` | Chia nhỏ việc; hiển thị tiến độ con |
| 10 | **Bình luận** | nút `[💬 Comment]`, `/task comment` | Thảo luận trên task; thông báo người liên quan |
| 11 | **Đặt / tắt nhắc** | `/task remind`, nút trong chi tiết | Nhắc trước hạn qua DM (yêu cầu có due) |
| 12 | **Nhận thông báo** (tự động) | — (hệ thống) | DM khi được gán / task xong / có comment / đến hạn |
| 13 | **Bảng Kanban** | nút `📊 Kanban` (trong RHS/channel header) → modal gần full màn | Bảng cột theo status: cá nhân / kênh; kéo-thả, theo dõi tiến độ |

---

### 5.1 Chi tiết chức năng theo nhóm thao tác

#### A. Tạo task
- **Cách gọi**: `/task add "<tiêu đề>"` (chỉ tiêu đề) → mở **New Task dialog pre-filled tiêu đề**; HOẶC **`/task new`** mở dialog trống (hoặc `/task new "<tiêu đề>"` pre-fill) — điểm vào chính cho **mobile** (#107); HOẶC nút **➕ New Task** trong **composer** (desktop, `registerPostEditorActionComponent`, #107) hoặc trong RHS; HOẶC message action **"Tạo task từ tin nhắn"** (nội dung message → `summary` + `description`). KHÔNG parse inline `@assignee`/`due`/`desc` — mọi trường khác nhập trong dialog.
- **Đầu vào** (trong dialog): `summary` (bắt buộc), `assignee` (**1** user), `due`, `description`.
- **Scope / trigger**: mặc định **channel task** (`channel_id` = kênh hiện tại). **Task cá nhân** (`ChannelID == ""`): tạo qua **New Task dialog** chọn scope *Personal*, HOẶC `/task add` trong **DM với bot**.
- **Hành vi**: sinh taskID = **ULID**; ghi index `assigned`/`created`/`channel`/`all` (key-per-edge); post **interactive card** vào kênh (nếu channel task); lưu `ChannelPostID`/`DMPostID`. **Rule notification**: **gửi DM thông báo tới assignee** khi được gán — **trừ khi `assignee == creator`** (không tự DM). KHÔNG dùng @mention trong kênh (ngoài scope).
- **Kết quả**: ephemeral xác nhận ✅ + card tương tác (có buttons Complete/Assign/Subtask/Comment).
- **Phân quyền**: mọi thành viên kênh đều tạo được.

#### B. Liệt kê & lọc task
- **Cách gọi**: `/task list [mine|channel|all] [status todo|in_progress|done|cancelled] [due overdue|today|week]` HOẶC tab sidebar **My Tasks** / **Channel Tasks** (desktop) HOẶC **Interactive Dialog** (mobile).
- **Chức năng lọc**: theo **scope** (việc của tôi / kênh này / tất cả), **trạng thái** (todo/in_progress/done/cancelled), **assignee**, **hạn** (quá hạn 🔴 / hôm nay / tuần này). Phân trang + sắp xếp theo due/created.
- **Hành vi**: desktop → RHS `QuickList`; mobile → **Quick List dialog** (`select` lọc + `select` chọn task → mở Task Detail dialog). "My Tasks" = index `idx:u:{uid}:assigned`; task quá hạn tô đỏ.
- **Kết quả**: danh sách (RHS desktop / dialog mobile / ephemeral khi dùng slash command).
- **Phân quyền**: My Tasks tự xem; Channel Tasks cho thành viên kênh.

#### C. Xem chi tiết task
- **Cách gọi**: `/task show <id>`, click 1 task trong Quick List/Kanban, bấm **“Chi tiết”** trên task card, hoặc chọn từ Quick List dialog.
- **Chức năng**: hiển thị summary, description, due (theo timezone user), assignees, danh sách **subtask kèm tiến độ** (vd: 2/3 xong), **comment** theo thời gian, trạng thái **reminder**.
- **Render**: **desktop RHS** → `TaskDetailPanel`; **desktop Kanban modal** → panel detail **bên trong modal** (panel phải, không mở RHS phía sau); **mobile (hoặc bất kỳ đâu)** → **Interactive Dialog** xem + sửa các trường (status, assignee, due...) rồi submit lưu. → **Mobile hỗ trợ đầy đủ Task Detail**.

#### D. Sửa task
- **Cách gọi**: `/task edit <id> [summary|due|desc]...` HOẶC nút **sửa** trong chi tiết.
- **Chức năng**: **partial update** — chỉ trường được chỉ định mới đổi (kiểu `update_fields` của Lark), rõ ràng giữa "không đổi" và "xoá". Cập nhật `UpdatedAt`.
- **Phân quyền**: **creator HOẶC assignee** (assignee được sửa để điều chỉnh cho phù hợp — mục 5.4).

#### E. Đổi status (Kanban workflow)
- **Cách gọi**: kéo-thả thẻ giữa các cột trên bảng Kanban HOẶC `/task status <id> <todo|in_progress|done|cancelled>`.
- **Chức năng**: đổi `Status` theo workflow cột. **done** ⇔ set `CompletedAt` + thông báo; **cancelled** = hủy task (không làm nữa, set `CancelledAt`, ngừng reminder, khác done). **Cột cố định 4 cột** `todo`/`in_progress`/`done`/`cancelled` (KHÔNG cấu hình).
- **Phân quyền**: creator hoặc assignee.

#### F. Hoàn thành / Hủy
- **Cách gọi**: nút **`[✓ Done]`** / **`[🚫 Cancel]`** trên card, `/task done <id>` / `/task cancel <id>`, kéo vào cột done/cancelled.
- **Chức năng**: tương đương `/task status <id> done|cancelled`. `done` ⇔ set `CompletedAt`; `cancelled` ⇔ set `CancelledAt` (ngừng reminder). Card gạch ngang; **thông báo creator + assignee**. **Không có trạng thái “reopen”** — muốn làm lại thì `/task status <id> todo|in_progress` (xoá CompletedAt/CancelledAt).
- **Phân quyền**: creator HOẶC assignee (co-owner).

#### G. Xoá task
- **Cách gọi**: `/task delete <id>` hoặc menu trong chi tiết (có dialog xác nhận).
- **Chức năng**: **hard delete cascade** (theo review) — xoá đệ quy subtask + comment (liệt kê qua `ListKeys` prefix) + gỡ mọi index marker + xoá entity; không để mồ côi.
- **Phân quyền**: creator (hoặc admin kênh).

#### H. Gán / Bỏ gán (Assign)
- **Cách gọi**: nút **`[👤 Assign]`** (mở user picker), `/task assign <id> @user`, `/task unassign <id>`.
- **Chức năng**: **gán 1 assignee** (thay assignee hiện tại); **DM người được gán**; cập nhật index `assigned` + `idx:reminder` (nếu cần).
- **Phân quyền**: creator hoặc assignee.

#### I. Subtask (tạo / xem)
- **Cách gọi**: nút **`[➕ Subtask]`**, `/task subtask <idCha> <tóm tắt>`.
- **Chức năng**: tạo task con (có `parent_task_id`); **ChannelID kế thừa từ task cha**; **assignee mặc định = assignee task cha** (creator/assignee gán lại sau nếu cần); permission check dùng **ChannelID của chính subtask** (kế thừa). Lồng trong chi tiết task cha; task cha hiển thị **tiến độ subtask** (vd `2/3`); subtask có đầy đủ thuộc tính của task (do, assign, due).
- **Quy tắc cha-con**: subtask = task độc lập; parent done chỉ khi mọi subtask done/cancelled; parent cancelled → cascade cancel subtask todo/in_progress (xem 5.5).
- **Phân quyền**: creator hoặc assignee của task cha.

#### J. Comment (thêm / xem)
- **Cách gọi**: nút **`[💬 Comment]`**, `/task comment <id> <nội dung>`.
- **Chức năng**: thêm comment (nội dung + người + thời gian); list theo thời gian; **thông báo participants** (assignees + creator).
- **Phân quyền**: thành viên kênh / participant.

#### K. Reminder (đặt / tắt)
- **Cách gọi**: `/task remind <id> <15m|1h|1d|off>`, nút trong chi tiết.
- **Chức năng**: đặt khoảng nhắc trước `due` (mặc định theo config); scheduler DM assignee **đúng lúc** `due − offset` và **chỉ 1 lần** (đánh dấu `ReminderFired`); `off` để tắt. **Yêu cầu task có due.**
- **Phân quyền**: creator hoặc assignee.

#### L. Nhận thông báo (tự động)
- **Sự kiện kích hoạt**: được gán task · task được đánh dấu xong · có comment mới · reminder đến hạn · task sắp quá hạn. (KHÔNG thông báo khi bị `unassign`.)
- **Kênh**: DM bot (mặc định) hoặc ephemeral; bật/tắt qua config.

#### L. Quick List (RHS)
- **Cách gọi**: nút channel header **`📋 Tasks`**.
- **Chức năng**: mở **RHS** — `QuickList` với tab **My Tasks** / **Channel Tasks**, bộ lọc, nút **➕ New Task** (mở popup dialog); click task → **TaskDetailPanel** (cùng RHS).

#### M. Bảng Kanban (theo dõi task theo status — full page)
- **Cách gọi**: nút **`📊 Kanban`** (trong RHS / channel header) mở **full-page modal overlay**, hoặc `/task board [mine|channel]`.
- **Chức năng**: hiển thị task dạng **bảng cột theo status** (To Do · In Progress · Done · Cancelled). Bộ chọn scope: **Cá nhân (My Tasks)** / **Kênh (Channel)**. Mỗi cột là 1 status; thẻ hiển thị summary, assignee (avatar), due (tô đỏ nếu quá hạn), số subtask.
- **Tương tác**: **kéo-thả thẻ** giữa các cột → đổi status (→ done thì đánh dấu hoàn thành + thông báo); kéo để **sắp xếp lại thứ tự** trong cột (`OrderKey` fractional index); click thẻ → mở **TaskDetailPanel**.
- **Theo dõi tiến độ**: header bảng hiển thị tiến độ tổng quan (vd: `5/12 done`) cho scope đang chọn; cập nhật tức thì khi status đổi (WebSocket / refetch).
- **Phân quyền**: cá nhân tự xem/sửa task của mình; Kanban kênh cho thành viên kênh.

---

### 5.2 Slash command đầy đủ (tham chiếu nhanh)
```
/task new ["<summary>"]                                                mở New Task dialog (trống hoặc pre-fill) — điểm vào chính mobile (#107)
/task add "<summary>"                                                  tạo task + mở dialog điền assignee/due/desc
/task list [mine|channel|all] [status todo|in_progress|done|cancelled] [due ...]   liệt kê + lọc
/task show <id>                                                       xem chi tiết
/task status <id> <todo|in_progress|done|cancelled>                  đổi status (đặt lại todo/in_progress = “mở lại”)
/task done <id> | cancel <id>                                         hoàn thành / hủy
/task edit <id> [summary|due|desc]...                                 sửa (partial)
/task delete <id>                                                     xoá (hard delete cascade)
/task assign <id> @user | unassign <id>                               gán / bỏ gán (single assignee)
/task subtask <parentId> <summary>                                    thêm subtask
/task comment <id> <text>                                             bình luận
/task remind <id> <15m|1h|1d|off>                                     đặt / tắt nhắc
/task board [mine|channel]                                            Kanban: desktop mở modal; **mobile mở Quick List dialog** (Kanban không hỗ trợ mobile)
/task search <keyword>                                              tìm task (escape hatch cho mobile/dialog)
/task help
```
Parser: `@mention` → `userId`; `due` → timestamp theo timezone user (`preference`); autocomplete cho mọi subcommand. **Ưu tiên Interactive Dialog**: `/task new` (không cần tiêu đề, mở dialog trống — điểm vào mobile #107) và `/task add "<tiêu đề>"` (chỉ cần tiêu đề, có/không quote) → mở **dialog tạo task** điền assignee/due/desc; `/task edit`/`/task remind` cũng mở dialog. Parser chỉ parse tiêu đề + mở dialog → đơn giản, cần unit test kỹ.

### 5.3 REST API (prefix `/plugins/com.mattermost.plugin-task/api/v1/`, middleware auth)
```
POST   /tasks                          tạo
GET    /tasks?scope=mine|channel&channel_id=...&status=...&due=...&after_order_key=...&limit=50  list (phân trang cursor)
GET    /tasks/:id                      chi tiết
PATCH  /tasks/:id                      sửa (partial, update_fields)
DELETE /tasks/:id                      xoá (hard delete cascade)
PATCH  /tasks/:id/status               đổi status     body: {status}   (status∈{todo,in_progress,done,cancelled}; đặt todo/in_progress ⇔ xoá CompletedAt/CancelledAt)
PATCH  /tasks/:id/order                reorder/sang cột   body: {order_key, status}   (kéo-thả Kanban)
POST   /tasks/:id/assignee             gán assignee (single)  body: {user_id}
DELETE /tasks/:id/assignee             bỏ assignee (xoá `AssigneeID`)
POST   /tasks/:id/subtasks             tạo subtask
GET    /tasks/:id/subtasks             list subtask
POST   /tasks/:id/comments             thêm comment
GET    /tasks/:id/comments             list comment
POST   /tasks/:id/reminder             đặt reminder         body: {offset_ms}
DELETE /tasks/:id/reminder            tắt reminder (xoá ReminderOffset + idx:reminder)
POST   /actions                        callback từ interactive buttons (Done/Cancel/Assign/Subtask/Comment) → mở dialog / cập nhật card
GET    /me/tasks                       My Tasks (alias /tasks?scope=mine)
```
Định dạng JSON + `update_fields` (partial update kiểu Lark). **Không có `client_token` idempotency trong MVP** (bỏ).

### 5.4 Phân quyền (co-owner model)
**Assignee = co-owner** để chủ động điều chỉnh công việc (sửa, chuyển giao, đổi status...); **chỉ creator mới xoá** để tránh mất kiểm soát hoàn toàn. MVP chia:

| Hành động | Creator | Assignee | Người khác |
|---|---|---|---|
| Xem task | ✅ | ✅ | ✅ nếu `ChannelID != ""` (kênh) · ❌ nếu personal (`ChannelID == ""`) |
| Sửa summary/description/due | ✅ | ✅ | ❌ |
| Đổi status (todo/in_progress/done/cancelled) | ✅ | ✅ | ❌ |
| Complete / Cancel | ✅ | ✅ | ❌ |
| Assign / Unassign (chuyển giao) | ✅ | ✅ | ❌ |
| Tạo subtask / đặt reminder | ✅ | ✅ | ❌ |
| Comment | ✅ | ✅ | ✅ (nếu xem được theo rule trên) |
| Xoá task (hard delete) | ✅ | ❌ | ✅ **admin kênh** (nếu `ChannelID != ""`) |

> Triết lý: Assignee gần như **co-owner** (sửa, chuyển giao, đổi status, subtask, reminder, comment). **Chỉ creator HOẶC admin kênh (với channel task) mới xoá được**; assignee không xoá được. **Không thông báo creator** khi assignee thay đổi critical field.

**Multi-assignee (MVP):** ❌ KHÔNG dùng — **chỉ 1 assignee/task** (đơn giản hóa, theo review). Multi-assignee + `completion_mode` (or-sign/countersign của Lark) **hoãn** (Vượt MVP). Khi hoãn, `done` rõ ràng = assignee duy nhất đánh dấu xong.

**Quy tắc visibility (theo ChannelID):**
- `ChannelID != ""` → **mọi thành viên kênh** xem được task.
- `ChannelID == ""` (personal) → **chỉ creator + assignee** xem được (`/task show` của người khác sẽ bị từ chối).

### 5.5 Subtask — ngữ nghĩa & hiển thị trong “My Tasks”
- **Subtask là task độc lập**: có đầy đủ thuộc tính, hoàn thành/hủy như task thường.
- **Quy tắc hoàn thành cha-con**:
  - Parent chỉ được **done** khi **tất cả subtask** đã `done` HOẶC `cancelled` (chặn done nếu còn subtask todo/in_progress).
  - Parent **cancelled** → **cascade**: mọi subtask đang `todo`/`in_progress` tự động chuyển `cancelled`.
- **Hiển thị Quick List (My Tasks)**: danh sách **phẳng các task độc lập** (gồm cả task gốc và subtask được gán) — **không group Parent-Subtask** (review #5); mỗi dòng là 1 task; subtask hiện như task thường (có thể thêm nhãn nhỏ “sub của: <parent>” để định danh).
- **Kanban**: subtask là **thẻ riêng** + toggle “Hiện/ẩn subtask”.
- **Task Detail**: subtask lồng trong detail task cha (tiến độ x/y).

---

## Phần 6 — Webapp (React) — tầng Hybrid & Bố cục UI

Tầng webapp chỉ tiêu thụ REST API đã xây ở server (phase 1–3).

### 6.1 Bố cục UI (đã chốt)

| View | Vị trí hiển thị | Cách mở |
|---|---|---|
| **Quick List** (My Tasks / Channel Tasks + lọc) | **Right Sidebar (RHS)** | nút channel header `📋 Tasks` |
| **New Task** | **Popup dialog (modal)** | nút ➕ trong **composer** (desktop #107) hoặc RHS, `/task new` (dialog trống/pre-fill, điểm vào mobile #107), `/task add`, message action |
| **Task Detail** | **RHS** (từ Quick List) HOẶC **panel nội bộ Kanban modal** (từ Kanban) HOẶC **Interactive Dialog** (mobile) | click 1 task trong Quick List / Kanban / `/task show` |
| **Kanban board** | **Modal gần full màn** (overlay lớn phủ gần kín center channel) | nút `📊 Kanban` trong RHS/channel header, hoặc `/task board` |

> Quy ước giao tiếp: Quick List & Task Detail **cùng chia sẻ RHS**; **khi Kanban modal đang mở, click thẻ mở Task Detail ngay bên trong modal** (panel phải) — không mở RHS phía sau; New Task là dialog nổi; toàn bộ UI **đa ngôn ngữ** (Việt/Anh).

### 6.2 Component & đăng ký (dùng registry method chính xác)
- `webapp/src/index.tsx`:
  - `registry.registerChannelHeaderButtonAction(icon, () => openRHS(), "Tasks", "Mở danh sách task")` — mở RHS.
  - `registry.registerPostEditorActionComponent(NewTaskComposerButton)` — nút **➕ New Task** trong **FormattingBar của composer** (desktop, #107); dùng class `AdvancedTextEditor__action-button` của host + tooltip `react-bootstrap`. Click → dispatch `OPEN_NEW_TASK_DIALOG` với `draft.channelId`. Mobile không render slot này → dùng `/task new`.
  - `registry.registerRightHandSidebarComponent(TaskSidebar, "Tasks")` — RHS chứa `QuickList` + `TaskDetailPanel`.
  - `registry.registerRootComponent(KanbanModal)` — nút `📊 Kanban` mở **modal gần full màn** chứa `KanbanBoard`.
  - `registry.registerRootComponent(NewTaskDialog)` — popup tạo task (**desktop**); **mobile/fallback dùng Interactive Dialog** (`server/dialog.go`). Cả 2 submit cùng `POST /tasks` (không giảm duplication).
  - `registry.registerWebSocketEventHandler("task_updated", ...)` + `registerReducer` — real-time khi status đổi (nhận `p.API.PublishWebSocketEvent`).
  - `registry.registerTranslations((locale) => messages[locale])` — i18n Việt/Anh.
  - `registry.registerPostDropdownMenuAction("Tạo task", openNewTaskFromMessage)` — **“Tạo task từ tin nhắn”** (nội dung message → summary+description).
  - (tuỳ chọn) `registry.registerPostCardTypeComponent("custom_task", TaskCard)` — render task card bằng React.
- `webapp/src/components/`:
  - `TaskSidebar` (RHS) — chứa `QuickList` + `TaskDetailPanel`.
  - `QuickList` — tab My Tasks / Channel Tasks, bộ lọc status/due/assignee.
  - `TaskDetailPanel` — summary, due, assignees, subtasks (lồng + tiến độ), comments, reminder.
  - `NewTaskDialog` — popup modal tạo task.
  - `KanbanModal` + `KanbanBoard` — **modal gần full màn**; bảng **4 cột cố định** theo status (**To Do · In Progress · Done · Cancelled**); bộ chọn scope Cá nhân/Kênh; **drag-and-drop** (`@dnd-kit/core` + `@dnd-kit/sortable`) đổi status & sắp xếp. **Sort = `OrderKey` (toàn cục)** — không sort theo Due (tránh xung đột ghi đè khi kéo-thả); `OrderKey` chỉ tính theo due→created **lúc tạo** để có thứ tự khởi tạo hợp lý, sau đó do kéo-thả quyết định. **Phân trang cursor đơn giản**: `GET /tasks?status=...&after_order_key=<lastKey>&limit=50` (“load more”); header tiến độ tổng quan (`done/total`); **subtask hiện thành thẻ riêng** (status độc lập) + vẫn lồng trong detail task cha; **click thẻ → mở `TaskDetailPanel` bên trong modal** (panel phải, không động đến RHS).
- `webapp/src/client.ts` — wrapper gọi REST API; `webpack.config.js` khai báo `externals` (react, redux, react-redux, react-router-dom).
- `webapp/i18n/{en,vi}.json` — chuỗi hiển thị đa ngôn ngữ.

### 6.4 Hỗ trợ Mobile (ref: Mobile plugins)

Mattermost mobile (React Native) **KHÔNG render webapp plugin** (`registerRightHandSidebarComponent`, `registerRootComponent`/modal, `registerPostCardTypeComponent` đều bị bỏ qua trên mobile). Nhưng **slash command, interactive message card, interactive dialog là cross-platform** → chạy được trên mobile. Chiến lược:

| Tính năng | Mobile | Desktop/Web |
|---|---|---|
| Slash command `/task *` | ✅ | ✅ |
| Task card (message attachment + buttons Done/Cancel/Assign/Subtask/Comment) | ✅ | ✅ |
| **Quick List** | ✅ **qua Interactive Dialog** (`/task list` → dialog có select lọc + chọn task) | ✅ (RHS `QuickList`) |
| **Task Detail** | ✅ **qua Interactive Dialog** (`/task show` / chọn từ Quick List → dialog xem+sửa) | ✅ (RHS `TaskDetailPanel` + card) |
| Interactive Dialog (New Task, Assign...) | ✅ (qua `trigger_id`) | ✅ |
| Reminder/Notification (post/DM) | ✅ | ✅ |
| **Kanban modal** (drag-and-drop) | ❌ | ✅ |

**Nghiên cứu plugin Calls (theo phản hồi):** Calls hiển thị call screen trên mobile nhờ **code native được build sẵn trong app Mattermost mobile** (chia sẻ types/RTC qua shared lib web↔mobile). Đây **KHÔNG phải cơ chế mở** cho plugin bên thứ 3 — muốn UI native riêng phải **fork app mobile** + viết native module (iOS `RCT_EXPORT_MODULE` / Android `ReactContextBaseJavaModule`) rồi tự build/phát hành. Quá nặng cho plugin phân phối → **không áp dụng**.

**Giải pháp mobile (dialog-first) — ref: Mobile plugins:** Doc xác nhận *“Server plugins may use interactive dialogs and interactive messages to build cross-platform interactions compatible with mobile.”* Nên trên mobile, **Task Detail và Quick List dùng Interactive Dialog** (`p.API.OpenInteractiveDialog`):
- **Quick List dialog**: dialog có các phần tử `select` (load sẵn `options` khi mở dialog) để lọc scope (Cá nhân/Kênh) + status, và một `select` liệt kê **top N task gần nhất / cận hạn nhất** (do Interactive Dialog `select` không có search/pagination/`perform_lookup`). **N cấu hình trong System Console, default 20**. **Escape hatch**: `/task search <keyword>` để tìm task cũ/xa. **Desktop dùng RHS làm chính**; dialog chỉ là fallback khi ở mobile.
- **Task Detail dialog**: dialog hiển thị các trường task dạng **element sửa được**: `text` (summary), `textarea` (description), `select` (status: todo/in_progress/done/cancelled), `select` với `data_source:"users"` cho **1 assignee**, `text` (due); thêm element read-only tóm tắt **subtask (tiến độ)** & **comment gần đây**. Submit → lưu (partial update) + refresh. Nút phụ để thêm subtask/comment (mở dialog con).
- **Lưu ý API**: Interactive Dialog hỗ trợ phần tử `text`, `textarea`, `select` (với `data_source: "users"|"channels"` HOẶC `options` tĩnh), `radio`, `checkbox`, `bool` (+ multiselect mới). **KHÔNG có** kiểu `users` riêng, **không có** `static_select`/`perform_lookup` (đó là Apps Framework).
- **Task card (message attachment)** vẫn dùng để **thông báo** khi tạo trong channel/DM (có buttons hành động nhanh) — không thay thế dialog.
- Desktop: RHS `QuickList`/`TaskDetailPanel` + Kanban modal (React) vẫn là UI chính; dialog là phương án mobile/fallback.

**Nguyên tắc tổng (mobile-first server layer):** mọi thao tác cốt lõi phải **đạt được qua slash command → Interactive Dialog hoặc task card** (không phụ thuộc RHS/Kanban React). Mobile có trải nghiệm đầy đủ: tạo/xem/list/**xem+sửa detail**/đổi status/gán/subtask/comment/remind. **RHS & Kanban modal là tăng cường cho desktop.**

- `server/dialog.go` (mới) — dựng `OpenInteractiveDialog` cho Quick List & Task Detail (phần tử: `text`/`textarea`/`select` + `data_source:"users"`/`options`/`radio`/`bool`; **không** `static_select`/`perform_lookup`).
- `server/message_attachment.go` — task card thông báo (channel/DM) + buttons hành động nhanh.
- **Tương lai (tuỳ chọn):** muốn modal/Kanban thật trên mobile → **Mattermost Apps Framework** (cross-platform, data-driven bindings) hoặc fork app mobile. Đưa vào "Vượt MVP".

### 6.3 Task card message (channel & DM) — ref: interactive-messages
Khi tạo task (`/task add`, New Task dialog, hoặc "tạo task từ tin nhắn"), plugin post một **interactive message attachment** làm task card. **Hoạt động trong cả channel và Direct Message** (vì chỉ là post có attachment + buttons):
- Dùng `model.SlackAttachment` với `Actions[]`: `[✓ Done] [🚫 Cancel] [👤 Assign] [➕ Subtask] [💬 Comment]`, mỗi action có `integration.url` = endpoint plugin (URL tương đối) + `context: {action, task_id}`; dùng `style` (primary/danger/default) + `tooltip` (v10.5+).
- Khi click button → server `ServeHTTP` nhận `{user_id, post_id, channel_id, context}` → xử lý → phản hồi `{update, ephemeral_text}` (cập nhật card) hoặc `{error}`.
- Card hiển thị: summary, assignee (mention), due (đỏ nếu quá hạn), status, tiến độ subtask. Khi done/cancel → card cập nhật gạch ngang.
- Hỗ trợ cả **ephemeral message** có button (5.10+) cho xác nhận nhanh.
- DM: assignee nhận DM card thông báo được gán; người tạo có thể `/task add` trong DM với chính mình (kanban scope Cá nhân).

> Quy ước giao tiếp giữa các view: Quick List & Task Detail **cùng chia sẻ RHS**; Kanban mở ở **modal gần full màn** (Task Detail mở bên trong modal); New Task là dialog nổi.

---

## Phần 7 — Reminder & Notification (scheduler)

- **Reminder**: mỗi task có `ReminderOffset` (ms trước due) + `ReminderFired`. Khi tạo qua `/task remind <id> <offset>`.
- **Scheduler**: `cluster.Schedule(p.API, "TaskReminderJob", cluster.MakeWaitForRoundedInterval(1*time.Minute), p.runReminderJob)` (chạy 1 node/cluster nhờ `cluster`). `runReminderJob` **KHÔNG quét toàn bộ task** — chỉ `client.KV.ListKeys(page, count, pluginapi.WithPrefix("idx:reminder:"))` (phân trang; key-per-edge `idx:reminder:{taskID}` chứa `{due, offset}`), đọc value nhỏ, fire khi `now >= due-offset` **VÀ** `now <= due + grace` (grace vd 1 phút). Nếu `due` đã ở quá khứ (ngay khi set) → fire **một lần ngay lập tức**. Fire → DM assignee (`GetDirectChannel` + `CreatePost`) → xoá key + set `ReminderFired=true`. Index được duy trì mỗi khi due/status/reminder đổi → **scalable tới hàng chục nghìn task**.
- **Notification events**: 
  - Gán assignee khi tạo/`assign` → **DM người được gán** (trừ `assignee==creator`). **KHÔNG DM** người bị `unassign` (đã chốt).
  - Hoàn thành / **hủy (cancel)** → thông báo creator + assignee.
  - Thêm comment → thông báo participants (assignee + creator).
- **Server-side i18n**: bot DM/ephemeral cũng cần đa ngôn ngữ → nhúng `assets/i18n/{en,vi}.json` (Go `embed`), load vào bundle, chọn locale theo `user.Locale` (`p.API.GetUser`) khi dựng nội dung card/DM. (Webapp đã dùng `registerTranslations`; server dùng cùng file JSON.)
- Cấu hình qua `settings_schema`: reminder interval, bật/tắt DM, bot hiển thị.
- **`rebuildReminderIndex(task)`** (gọi trong **mọi** code path update task để không miss case): tạo `idx:reminder:{id}` khi `due!=nil && status∈{todo,in_progress} && offset!=nil && !fired`; xoá khi `done`/`cancelled`/`off`/`fired`. **Trigger đầy đủ**: tạo task có due+offset · đổi due · đổi offset · đổi status (done/cancelled→xoá; chuyển về todo/in_progress→tạo lại nếu due chưa quá) · đổi assignee (cập nhật `assignee_id` trong value). **Reset `ReminderFired=false`** khi **status→todo/in_progress** hoặc **đổi due** (cho phép nhắc lại); mỗi chu kỳ fire đúng 1 lần theo Lark.

---

## Phần 8 — Files to modify / tạo mới

Cấu trúc dựa trên starter template (đổi module path `github.com/<user>/mattermost-plugin-task`, `id` = `com.mattermost.plugin-task`):

**Cấu hình khung**
- [ ] `plugin.json` — `id`, `name`, `description`, `min_server_version`, `settings_schema` (reminder default, DM on/off), `server`. **KHÔNG khai báo bot metadata** (bot tạo hoàn toàn qua `EnsureBot` trong OnActivate)
- [ ] `go.mod` — module path mới
- [ ] `.golangci.yml` — `local-prefixes` mới

**Server — model & store**
- [ ] `server/model/task.go`, `server/model/comment.go` — DTO (mục 4.2)
- [ ] `server/store/kvstore/kvstore.go` — mở rộng interface `KVStore` (GetTask/SaveTask/DeleteTask, ListByIndex, GetComments/SaveComment, GetSubtasks...)
- [ ] `server/store/kvstore/task.go`, `.../comment.go`, `.../index.go`, `.../reminder.go` — key-per-edge entity/index + ghi qua `SetAtomic`/`SetAtomicWithRetries`; ListKeys cho truy vấn
- [ ] `server/store/kvstore/store_test.go` — test concurrency/pagination

**Server — logic**
- [ ] `server/api.go` — router `/api/v1` + endpoint tasks/comments/actions (REST dùng **PATCH** cho partial update — mục 5.3)
- [ ] `server/command/command.go` — `/task` + subcommands + autocomplete; `add/edit/remind` **ưu tiên mở Interactive Dialog** (mục 5.2)
- [ ] `server/plugin.go` — `OnActivate`: khởi tạo store, đăng ký command, `cluster.Schedule` reminder job
- [ ] `server/job.go` — `runReminderJob` (mục 7)
- [ ] `server/notification/notification.go` — DM/ephemeral helpers + **server-side i18n** (chọn locale theo `user.Locale`)
- [ ] `assets/i18n/{en,vi}.json` — bảng dịch dùng chung cho server (Go `embed`) và webapp
- [ ] `server/i18n.go` — load bundle i18n (embed `assets/i18n/*.json`), `T(locale, key, args...)`
- [ ] `server/configuration.go` — thêm trường config (reminder interval, DM on/off) — **cột status cố định 4 cột** (không cấu hình)
- [ ] `server/message_attachment.go` — dựng **interactive task card** (compact, khi tạo trong channel/DM) dùng `model.SlackAttachment` + `Actions` buttons
- [ ] `server/message_action.go` — **“Tạo task từ tin nhắn”** (post dropdown action `registerPostDropdownMenuAction`): nội dung tin nhắn → `summary` + `description` của task, mở New Task dialog
- [ ] `server/dialog.go` — dựng **Interactive Dialog** cho Quick List & Task Detail (xem+sửa) + New Task/Assign — cross-platform (mobile) qua `OpenInteractiveDialog`

**Webapp**
- [ ] `webapp/src/index.tsx` — đăng ký: `registerChannelHeaderButtonAction`, `registerPostEditorActionComponent` (nút New Task trong composer #107), `registerRightHandSidebarComponent` (RHS), `registerRootComponent` (Kanban modal gần full màn + NewTask dialog), `registerWebSocketEventHandler`, `registerReducer`, `registerTranslations` (i18n)
- [ ] `webapp/src/components/{TaskSidebar,QuickList,TaskDetailPanel,NewTaskDialog,KanbanModal,KanbanBoard,TaskCard}.tsx`
- [ ] `webapp/src/client.ts` — wrapper gọi REST API
- [ ] `webapp/webpack.config.js` — khai báo `externals` (react/redux/react-redux/react-router-dom)
- [ ] `webapp/i18n/{en,vi}.json` — chuỗi đa ngôn ngữ; **format flat key-value** (`{"task.list.empty":"Không có task"}`) dùng chung cho server & webapp; server tự viết helper `T(locale, key, args)` load map; Makefile copy `assets/i18n/*.json` → `webapp/i18n/` mỗi build (**single source**)

**CI / build**
- [ ] `.github/workflows/ci.yml` — giữ nguyên; đảm bảo `make dist` xanh.

## Reuse (tận dụng code có sẵn)
- `cluster.Schedule` / `cluster.Job` (`server/plugin.go`) → scheduler reminder.
- Pattern `KVStore` interface + `pluginapi.Client.KV` (`server/store/kvstore/`) → lớp lưu trữ.
- Pattern `command.Handler`/`Register`/`Handle` + `model.NewAutocompleteData` (`server/command/command.go`) → slash command.
- `MattermostAuthorizationRequired` middleware + `mux` subrouter (`server/api.go`) → REST API.
- `configuration` clone-under-lock (`server/configuration.go`) → cài đặt.
- `mattermost-community/mattermost-plugin-todo` (tham khảo) → pattern reminder hàng ngày & message card, KHÔNG sao chép (thiếu subtask/index).

---

## Phần 9 — Kế hoạch thực hiện (theo giai đoạn)

- [ ] **Giai đoạn 0 — Khung sườn**: clone template, đổi id/module, tạo cấu trúc `model/store/command/notification/dialog`, model `Task` (ULID qua `github.com/oklog/ulid`, key-per-edge store), KVStore wrapper (`Set`+`SetAtomic`/`SetAtomicWithRetries`), `/task help`, `make dist` chạy được. **Khuyến nghị triển khai sớm**: (a) **test framework** — `go test ./...` + 1 test CAS validate `SetAtomicWithRetries`; (b) **i18n wrapper server** (`T(locale,key)`) ngay Phase 0/1 tránh refactor notification; (c) **chuẩn hóa status transition**: `todo`/`in_progress`→xoá CompletedAt+CancelledAt; `done`→set CompletedAt, xoá CancelledAt; `cancelled`→set CancelledAt, xoá CompletedAt, ngừng reminder; (d) log rõ crash/atomic conflict để debug reminder job trên cluster. **Quy ước error handling**: `SetAtomicWithRetries` = **5 lần retry, backoff 10ms**; lỗi ghi log + **trả về user** (ephemeral) cho thao tác người dùng; **silent fail + log** cho job nền.
- [ ] **Giai đoạn 1 — Task + Assignee + Reminder (giá trị cốt lõi)**: CRUD task, assignee (single), due, status workflow (todo/in_progress/done/cancelled) + complete/cancel; **reminder + notification DM** (scheduler `cluster.Schedule` dùng `idx:reminder`); **“tạo task từ tin nhắn”** (post action); commands `add/list mine/show/status/done/cancel/assign/unassign/remind/search`; **interactive message card** + **Interactive Dialog** (Task Detail/Quick List/New Task — mobile-first); REST `/tasks*` + `/tasks/:id/status`; key-per-edge indexes; phân quyền co-owner (delete = creator/admin kênh); unit test. → **Giá trị sử dụng đầy đủ trên desktop & mobile**.
- [ ] **Giai đoạn 2 — Subtask & Comment**: subtask (`idx:t:{parent}:sub:{id}` + tránh double-count trong My Tasks); comment 1-key/comment (`t:{id}:c:{ulid}`); commands `subtask/comment`; card/dialog thêm Subtask/Comment; chi tiết hiển thị subtask + comments.
- [ ] **Giai đoạn 3 — Webapp React RHS (desktop enhancement)**: channel header button mở **RHS** (`QuickList` danh sách phẳng task độc lập + `TaskDetailPanel`); `NewTaskDialog`; `registerTranslations` i18n Việt/Anh; real-time qua WebSocket.
- [ ] **Giai đoạn 4 — Kanban modal (desktop, tốn UI nhất)**: `KanbanModal` gần full màn + `KanbanBoard` drag-and-drop 4 cột cố định (`OrderKey` fractional index), scope Cá nhân/Kênh, header tiến độ, toggle hiện/ẩn subtask. (Đặt cuối vì drag-and-drop tốn thời gian nhất; giá trị chính đã có ở Giai đoạn 1.)

**Vượt MVP (sau này)**: Tasklist (dự án) + member/role owner/editor/viewer; Follower; Section (nhóm tùy chỉnh); Custom Field; Attachment; Repeat rule; Completion mode (or-sign/countersign); AI task agent; search nâng cao.

---

## Phần 10 — Verification (Kiểm thử)

- **Build**: `make dist` → file `dist/com.mattermost.plugin-task.tar.gz`; cài qua System Console hoặc `mmctl plugin upload` (server dev, `EnableUploads: true`).
- **Unit test**: `cd server && go test ./...` (store concurrency với CAS, command parser, reminder fire logic).
- **E2E thủ công**:
  1. `/task add Review PR @bob due 2026-07-01` → card xuất hiện + DM tới @bob.
  2. Click `[✓ Done]` → card cập nhật trạng thái + thông báo creator.
  3. `/task subtask T1 Viết test` rồi `/task comment T1 Đã xong` → hiển thị trong detail.
  4. `/task remind T1 15m` → đúng `due-15m` bot DM @bob đúng 1 lần (kiểm `ReminderFired`).
  5. `/task list mine` và mở RHS sidebar → thấy task; lọc theo status/due.
  6. Tạo nhiều task đồng thời (concurrency) → không mất/sai index.
  7. Nút `📋 Tasks` → RHS hiện **Quick List**; nút ➕ trong **composer** (desktop) hoặc `/task new` (mobile) → **popup dialog** New Task; click task → **Task Detail** trong RHS.
  8. Mở **Kanban full-page** (scope Cá nhân/Kênh): kéo thẻ từ To Do → In Progress → Done → status & thứ tự cập nhật, `CompletedAt` set khi vào Done, header tiến độ cập nhật. Đổi scope cá nhân ↔ kênh → bảng đổi nội dung.
- **Kiểm pagination**: tạo >100 task, duyệt `list` qua các trang.

## Rủi ro & lưu ý
- **KVStore key ≤150 ký tự**: lược đồ key hiện an toàn; nếu cần index phức tạp hơn (sau này) phải rút gọn tiền tố.
- **Giới hạn scale KV (theo review)**: `KV.ListKeys(page,count,WithPrefix)` lọc prefix **client-side** và phân trang → vùng an toàn thực tế: **~2.000 task/user**, **~10.000 task/channel**, **~vài trăm comment/task**, **~vài nghìn reminder đang chờ**. **DB riêng là roadmap, hoãn** (chỉ làm lại khi benchmark cho thấy vượt vùng an toàn; đến lúc đó tách sang bảng riêng hoặc dùng plugin DB hooks).
- **Quick List dialog (mobile)**: `select` không search/pagination → chỉ hiển thị **top N** task; RHS desktop là chính cho danh sách dài.
- **Reminder trên cluster**: `cluster.Schedule` đảm bảo chỉ 1 node chạy job — tránh gửi DM trùng.
- **Hard delete cascade**: rủi ro crash giữa chừng để lại vài key rác (hiếm); không có transaction KV → chấp nhận, không dùng GC quét toàn bộ.
- **Timezone**: render due theo `preference` timezone của từng user (giống Lark — server lưu timestamp, client tự hiển thị).
- **Idempotent**: KHÔNG dùng `client_token` trong MVP (đã chốt bỏ).
- **Rate limiting / abuse prevention** (spam comment/create): **hoãn** (Vượt MVP).

---

## Phụ lục A — Thuật toán OrderKey (fractional index cho Kanban)

Thẻ trong cột Kanban dùng `OrderKey string` so sánh **lexicographic**. Kéo-thả chỉ cập nhật **1 thẻ**.

```go
const keyMin = "a"   // đầu cột
const keyMax = "z"   // cuối cột

// midpoint giữa 2 key lân cận
func orderKeyBetween(prev, next string) string {
    // giữa "a" và "c" → "b"; giữa "a" và "b" → "aV" (chèn ký tự giữa)
    // xử lý carry/overflow; trả về key nằm giữa prev và next
}
// Rebalance cục bộ khi len(OrderKey) > 50 hoặc hết khoảng trống:
//   gán lại keyMin..keyMax đều cho các thẻ trong cột (hiếm).
```
- Thẻ mới cuối cột → key lớn hơn keyMax hiện tại (append); chèn đầu → midpoint(keyMin, firstKey).

**Tạo task mới (OrderKey toàn cục):** OrderKey = **giá trị lớn hơn OrderKey lớn nhất hiện có toàn cục** (append suffix, vd nếu max="m" thì mới="m0"; luôn lớn hơn) → task mới luôn nằm **cuối cột** của status khởi tạo (todo). Khi user kéo-thả sang vị trí/cột khác → tính lại OrderKey = midpoint giữa 2 thẻ lân cận tại chỗ thả. **Đơn giản, ít rắc rối**; rebalance chỉ khi chuỗi OrderKey quá dài (>50 ký tự, hiếm).

**UX behavior (OrderKey toàn cục):** khi kéo thẻ từ cột A sang cột B, client tính OrderKey mới = midpoint giữa 2 thẻ lân cận tại vị trí thả trong cột B (theo thứ tự OrderKey hiện có trong cột). Mỗi cột hiển thị các thẻ của status đó **sắp theo OrderKey tăng dần** → vị trí thả quyết định OrderKey, độc lập với cột nguồn. Kết quả: kéo-thả luôn đặt thẻ đúng chỗ user thấy — trực giác như Trello/Lark, không “bất ngờ”.

## Phụ lục B — WebSocket event schema (real-time)
Server `p.API.PublishWebSocketEvent(event, payload, &model.WebsocketBroadcast{...})`; webapp `registerWebSocketEventHandler`.

```json
{
  "event": "task_updated",
  "payload": {
    "task_id": "01HXY...",
    "seq": 42,
    "updated_at": 1719000000000,
    "changed_fields": ["status"],
    "task": { "id":"...", "status":"done", "assignee_id":"...", "due":1719000000000 }
  }
}
```
- **Khi publish**: tạo/cập nhật/xoá task, đổi status/assignee/due, thêm comment, reminder fire.
- **Broadcast scope**: theo `ChannelID` (thành viên kênh) nếu channel task; personal → gửi riêng creator + assignee (`UserId` broadcast). Versioning: thêm `seq`/`updated_at` để client drop stale.

---

## Decisions Log

- **Rejected:** Mô tả chức năng người dùng chỉ bằng cú pháp slash command ngắn gọn. **Why:** Người dùng phản hồi cần rõ hơn về *chức năng người dùng* — các thao tác người dùng và tính năng chi tiết cho từng thao tác. Đã thay bằng **Phần 5 — Chức năng người dùng** gồm bảng tổng quan + chi tiết theo nhóm (cách gọi UI, đầu vào, hành vi, kết quả/thông báo, phân quyền) cho từng thao tác.
- **Rejected:** Lưu reminder bằng goroutine/ticker tự viết. **Why:** Starter template đã có `cluster.Schedule` (cluster-aware, chạy 1 node) — dùng lại an toàn hơn và tránh DM trùng trên cluster.
- **Rejected:** Bao gồm Tasklist/Follower/Custom Field/Attachment/Repeat/AI agent trong MVP. **Why:** Người dùng chọn phạm vi MVP = Task+Assignee + Subtask+Comment + Reminder+Notification; các tính năng còn lại đưa vào "Vượt MVP".
- **Rejected:** Model status chỉ 2 trạng thái "open/completed". **Why:** Người dùng yêu cầu bổ sung **bảng Kanban** theo dõi task theo status (cá nhân, kênh). Đổi `Status` thành `todo|in_progress|done|cancelled` (+ `OrderInStatus`, `CancelledAt`); thêm thao tác đổi status, bảng Kanban (drag-and-drop), `/task status`, `/task board`, `/task cancel`, endpoint `PUT /tasks/:id/status`, component `KanbanModal`/`KanbanBoard`.
- **Rejected:** Status workflow chỉ 3 trạng thái (todo/in_progress/done). **Why:** Người dùng yêu cầu thêm status **cancel** (hủy task không làm nữa). Đã bổ sung `cancelled` (gắn `CancelledAt`, ngừng reminder, khác `done`) vào model, Kanban column, command, scheduler.
- **Rejected:** Bố cục UI chưa xác định rõ (sidebar/modal/full-page lẫn lộn). **Why:** Người dùng chốt: **Quick List = RHS**, **New Task = popup dialog**, **Task Detail = RHS**, **Kanban = modal gần full màn**. Dùng `registerRightHandSidebarComponent` (RHS) + `registerRootComponent` (modal).
- **Rejected:** Cho phép cấu hình cột status trong System Console. **Why:** Người dùng chọn **cố định 4 cột** (To Do/In Progress/Done/Cancelled) — đơn giản, nhanh triển khai.
- **Rejected:** Subtask chỉ lồng trong Task Detail. **Why:** Người dùng muốn subtask hiện thành **thẻ riêng trên Kanban** (status độc lập) VẪN lồng trong detail task cha.
- **Rejected:** UI một ngôn ngữ cố định. **Why:** Người dùng chọn **i18n (Việt + Anh)** qua `registerTranslations` + `en.json`/`vi.json`.
- **Rejected:** Yêu cầu RHS/Kanban (webapp React) chạy trên mobile. **Why:** Mattermost mobile (React Native) **không render webapp plugin**; chỉ slash command + interactive message card + interactive dialog là cross-platform. Chiến lược = **graceful degradation**: đảm bảo mọi thao tác cốt lõi làm được qua chat trên mobile; RHS/Kanban là tăng cường desktop (mục **6.4**). Tương lai muốn modal/Kanban thật trên mobile → Mattermost Apps Framework.
- **Rejected:** Nhầm lẫn giữa “List” (Quick List/Kanban = VIEW) và “Tasklist” (Lark = ENTITY dự án). **Why:** Người dùng hỏi rõ sự khác biệt. Đã thêm mục **Thuật ngữ** phân biệt: List = cách hiển thị; Tasklist = thực thể “dự án” gom task (như Board Trello/Project Asana). Người dùng quyết định **không cần Tasklist** trong MVP (scope Cá nhân/Kênh + 4 cột status là đủ) → Tasklist giữ hoãn (Vượt MVP).
- **Rejected:** Bỏ qua Task Detail trên mobile (chỉ coi RHS là nơi xem detail). **Why:** Người dùng nhấn mạnh Task Detail là nội dung quan trọng, **mobile phải hỗ trợ**; gợi ý nghiên cứu plugin Calls. Nghiên cứu cho thấy call screen của Calls là **native build sẵn trong app mobile** (không mở cho plugin bên thứ 3) → **không áp dụng**. Người dùng yêu cầu cụ thể: **Task Detail và Quick List trên mobile dùng Interactive Dialog** (theo doc Mobile plugins: interactive dialogs cross-platform). Giải pháp = **dialog-first**: `/task show` & `/task list` mở `OpenInteractiveDialog` (xem+sửa được) trên mobile; RHS/Kanban React giữ cho desktop. Thêm `server/dialog.go`; cập nhật mục **6.4** + thao tác B/C.
- **Rejected:** Dùng `mattermost-plugin-api` repo riêng. **Why:** Repo đó đã **archived**; dùng `github.com/mattermost/mattermost/server/public/pluginapi` (template đã đúng). Mục **2.1** chốt phiên bản mới nhất: min_server_version 10.7.0+, Go 1.23+, React 18 (externals), Node 24/npm 11, `@dnd-kit`.
- **Rejected:** Task card chỉ hiển thị trong channel. **Why:** Người dùng yêu cầu task card khi tạo trong cả **channel và Direct Message**. Xác nhận **interactive message attachment** (buttons + context) tự hoạt động trong cả hai; thêm mục **6.3** + `message_attachment.go`.
- **Chốt:** UI = Hybrid (slash command + channel header button + interactive message card + sidebar React + **Kanban board**). Phạm vi task = Hybrid (có `channel_id` tuỳ chọn nhưng vẫn hiện toàn cục trong My Tasks).

### Đợt review kiến trúc (scale & correctness)
- **Rejected:** `KV.CompareAndSet`. **Why:** Đã **deprecated**. Dùng `client.KV.Set(key, value, pluginapi.SetAtomic(oldValue))` hoặc helper `SetAtomicWithRetries` (mục 4.3).
- **Rejected:** Lưu index dạng `[]taskID` lớn trong 1 key KV. **Why:** Gây O(n) marshal + CAS retry khi đông user. Chuyển sang **key-per-edge** (`idx:u:{uid}:assigned:{taskID}`) — ghi 1 Set/Delete không contention, query bằng `ListKeys(prefix)` (mục 4.1).
- **Rejected:** Lưu comment inline `t:{id}:comments -> []Comment`. **Why:** Phình object + CAS contention khi nhiều người comment. Chuyển sang **1 key/comment** `t:{id}:c:{ulid}` (thêm = 1 Set không conflict; list = ListKeys theo ULID) (mục 4.1).
- **Rejected:** Reminder quét toàn bộ task mỗi phút. **Why:** Không scalable (hàng chục nghìn task). Thêm **index riêng** `idx:reminder:{taskID} -> {due, offset}` — scheduler chỉ `ListKeys("idx:reminder:")` đọc value nhỏ, không load entity (mục 4.3, 7).
- **Rejected:** `OrderInStatus int`. **Why:** Chèn giữa 2 thẻ phải reindex cả cột → chuỗi CAS. Chuyển sang **`OrderKey` fractional index** (midpoint string) — kéo thả chỉ cập nhật 1 thẻ (mục 4.2/4.3).
- **Rejected:** TaskID tuần tự `seq:task`. **Why:** Counter chung là hotspot contention. Chuyển sang **ULID** (toàn cục, sortable) (mục 4.1/4.2).
- **Rejected:** Thiếu index toàn cục. **Why:** `/task list all` phải merge/dedup nhiều index. Thêm `idx:all:task:{taskID}` (mục 4.1).
- **Rejected:** Xoá cascade cứng (nhiều KV op, rác nếu crash). **Why:** Không có transaction. Chuyển sang **soft-delete** (`DeletedAt`) + xoá index trước + **GC job** dọn entity/subtask/comment mồ côi > 24h (mục 4.3).
- **Rejected:** Subtask đếm kép trong My Tasks. **Why:** Subtask có assignee riêng lọt index. My Tasks chỉ hiện **task gốc** (`ParentTaskID==""`); Kanban có **toggle hiện/ẩn subtask** + style khác (mục 5.5).
- **Rejected:** Phân quyền “creator HOẶC assignee đều sửa”. **Why:** Dẫn xung đột (assignee tự đổi due/gán lại). **Tách vai**: Creator = edit/delete/assign/đặt due; Assignee = status/comment/complete/cancel (mục 5.4).
- **Rejected:** Nhầm Interactive Dialog có `static_select`/`perform_lookup`/kiểu `users`. **Why:** Đó là **Apps Framework**, KHÔNG phải Interactive Dialog. Đúng API: phần tử `select` với `data_source:"users"`/`options` (+ multiselect mới) — vẫn picker được assignee (mục 6.4).
- **Rejected:** Thứ tự phase cũ (Reminder ở Phase 3, Kanban gộp Phase 4). **Why:** Giá trị sử dụng đến từ Task+Assignee+Reminder; Kanban (drag-and-drop) tốn UI nhất. Đưa **Reminder vào Phase 1**, **Kanban tách ra Phase 4 cuối** (mục 9).

### Đợt review #2 (correctness & UX)
- **Rejected:** Multi-assignee trong MVP. **Why:** Người dùng quyết định **chỉ 1 assignee/task** cho đơn giản. `AssigneeIDs []string` → `AssigneeID string`; `completion_mode` hoãn (mục 4.2, 5.4).
- **Rejected:** Task Detail từ Kanban mở RHS phía sau modal. **Why:** Modal phủ gần kín màn → RHS phía sau không thấy. Đổi: click thẻ trong Kanban mở **Task Detail panel bên trong modal** (mục 6.1/6.2).
- **Rejected:** Soft-delete + GC quét tìm subtask mồ côi. **Why:** Tìm mồ côi phải scan toàn bộ task → tốn kém. Đổi sang **HARD DELETE cascade** (liệt kê comment/subtask qua `ListKeys` prefix tại thời điểm xoá rồi xoá đệ quy) — không mồ côi, không cần GC (mục 4.3).
- **Rejected:** `PUT /tasks/:id` cho partial update. **Why:** Không chuẩn REST. Đổi **`PATCH /tasks/:id`** + gộp complete/reopen vào **`PATCH /tasks/:id/status`** (mục 5.3).
- **Rejected:** Chưa rõ visibility task personal. **Why:** Task `ChannelID==""` thì ai xem? Thêm rule: kênh → thành viên kênh; personal → chỉ creator+assignee (mục 5.4).
- **Rejected:** i18n chỉ cho webapp. **Why:** Bot DM/ephemeral cũng cần đa ngôn ngữ. Thêm **server-side i18n** (embed `assets/i18n/{en,vi}.json`, chọn theo `user.Locale`) (mục 7, Files).
- **Rejected:** Quick List dialog liệt kê toàn bộ task bằng `select`. **Why:** `select` không search/pagination → 100+ task không xử lý được. Giới hạn **top N** (gần/cận hạn); desktop RHS là chính (mục 6.4).
- **Verify:** `pluginapi.SetAtomic(oldValue)` & `SetAtomicWithRetries` **đã xác nhận tồn tại** (đọc source `server/public/pluginapi/kv.go`); `ListKeys(page,count,WithPrefix)` phân trang. Không phải đổi API (mục 4.3).

### Đợt review #3 (model/UX/API)
- **Rejected:** My Tasks chỉ hiện task gốc. **Why:** Assignee subtask mà không phải của cha sẽ không tìm thấy subtask. Đổi: My Tasks hiện **cả task gốc + subtask được gán**, UI phân biệt (badge `sub`, indent, group) (mục 5.5).
- **Rejected:** Model thiếu post_id của card. **Why:** Không update được card khi đổi status. Thêm `ChannelPostID` + `DMPostID` (mục 4.2).
- **Rejected:** Bảng quyền “Xem” mơ hồ. **Why:** ✅ chỉ cho channel task. Làm rõ trong bảng (mục 5.4).
- **Rejected:** `POST /tasks/:id/assignees` (số nhiều) cho single assignee. **Why:** Tên không khớp single. Đổi `POST /tasks/:id/assignee` body `{user_id}` (mục 5.3).
- **Rejected:** Luôn DM assignee khi tạo. **Why:** Spam nếu assignee==creator hoặc đang trong kênh. Rule: `assignee==creator` → không DM; trong kênh → ưu tiên mention kênh (mục 5.1.A).
- **Rejected:** Chưa có bot account. **Why:** Plugin cần bot để gửi DM/card. Thêm `EnsureBot` trong `OnActivate` + khai báo manifest (mục 2.3).
- **Rejected:** i18n server & webapp file riêng dễ drift. **Why:** Hai nguồn. Makefile copy `assets/i18n/*.json` → `webapp/i18n/` (single source) (Files).
- **Rejected:** Subtask có ChannelID riêng. **Why:** Visibility không nhất quán. **Subtask kế thừa ChannelID từ task cha** (mục 4.2). Parser slash đơn giản hóa: ưu tiên dialog cho `add/edit/remind` (mục 5.2).

### Đợt review #4 (đồng bộ & làm rõ)
- **Rejected:** Assignee không được edit/assign (tách vai cứng). **Why:** Review muốn assignee linh hoạt chuyển giao + sửa. Assignee = **co-owner** (edit, assign/unassign, status, complete, subtask, reminder, comment); chỉ **DELETE = creator** (mục 5.4, 5.1.D/H).
- **Rejected:** Subtask semantics chưa rõ. **Why:** Cần quy tắc cha-con. Subtask = task độc lập; **parent done chỉ khi mọi subtask done/cancelled**; **parent cancelled → cascade cancel** subtask todo/in_progress (mục 5.5).
- **Rejected:** Hard delete không có thứ tự. **Why:** Crash để rác. Thứ tự chặt: subtask→comment→index→entity (mục 4.3). + in-memory cache + ULID time-range paging giảm N+1.
- **Rejected:** OrderKey chưa định nghĩa thuật toán. **Why:** Cần implementation guide. Thêm **Phụ lục A** (keyMin/keyMax/midpoint/rebalance).
- **Rejected:** Kanban render toàn bộ task. **Why:** Hàng nghìn task chậm. **Phân trang từng cột** + sort Due→CreatedAt (mục 6.2).
- **Rejected:** Reminder index thiếu trigger. **Why:** Dễ miss case. Thêm `rebuildReminderIndex(task)` gọi mọi update path + list trigger (mục 7).
- **Rejected:** WebSocket chưa có schema. **Why:** Client không biết xử lý. Thêm **Phụ lục B** (payload + broadcast scope + versioning).
- **Verify:** Bot qua `EnsureBot` (Go) đáng tin hơn khai báo manifest; manifest `server.bots` cần verify template (mục 2.3). i18n **flat key-value** dùng chung server+webapp (Files). `/task add "title"` → mở dialog (mục 5.2). Rate limiting **hoãn** (Rủi ro).

### Đợt review #5 (đồng bộ nội bộ)
- **Rejected:** Intro 5.4 dùng lý do “tách vai cứng”. **Why:** Mâu thuẫn bảng (assignee = co-owner). Sửa intro: co-owner, chỉ creator xoá (mục 5.4).
- **Rejected:** Cột Kanban “cấu hình qua settings_schema” / thiếu Cancelled. **Why:** Trái quyết định 4 cột cố định. Đồng bộ: **4 cột cố định không cấu hình**, thêm Cancelled vào 5.1.E/M (mục 5.1).
- **Rejected:** Quick List group/sub-task cha-con. **Why:** Trái review #5. Quick List = **danh sách phẳng task độc lập** (gồm cả subtask), không group (mục 5.5, Phase 3).
- **Rejected:** `/task add [@assignee...]` (ngụ ý nhiều). **Why:** MVP single assignee. Đổi `/task add "<summary>"` → mở dialog (mục 5.2).
- **Rejected:** Tắt reminder qua offset=0. **Why:** Không rõ. Thêm `DELETE /tasks/:id/reminder` (mục 5.3).
- **Rejected:** Kanban sort theo Due + OrderKey kéo-thả (xung đột). **Why:** Sort Due ghi đè kéo-thả. Sort = **OrderKey duy nhất**; OrderKey chỉ tính theo due→created **lúc tạo**, sau đó do kéo-thả (mục 6.2).
- **Rejected:** OrderKey theo cột. **Why:** Đổi status → order cũ vô nghĩa. OrderKey **toàn cục** (mục 4.2).
- **Rejected:** In-memory cache TTL 5s (MVP). **Why:** Cluster nhiều node → stale, chưa invalidation. **Bỏ cache trong MVP**; chỉ thêm sau benchmark + invalidation (mục 4.3).

### Đợt review #6 (làm rõ)
- **Rejected:** Bảng 5.0 #13 “📋 Tasks → chế độ Kanban”. **Why:** Trái quyết định. Sửa: 📋 Tasks → RHS, 📊 Kanban → modal (mục 5.0).
- **Làm rõ:** OrderKey toàn cục — giải thích UX: thả ở đâu đặt OrderKey midpoint tại đó, mỗi cột sort theo OrderKey tăng dần (Phụ lục A).
- **Làm rõ:** DB riêng = **roadmap, hoãn** (Rủi ro).
- **Rejected:** Hard delete rác do crash chỉ chờ GC. **Why:** Đơn giản hơn = **self-healing read** (Get not-found → tự xoá marker rác) (mục 4.3).
- **Rejected:** Quick List dialog top N mơ hồ. **Why:** Cần cụ thể. N **config (default 20)** + **`/task search <keyword>`** escape hatch (mục 6.4, 5.2).
- **Làm rõ:** Subtask kế thừa ChannelID; permission check dùng ChannelID subtask; **assignee mặc định = assignee cha** (mục 5.1.I).
- **Làm rõ:** Error handling — `SetAtomicWithRetries` 5 retry/backoff 10ms; lỗi user → trả ephemeral; job nền → silent+log (Phase 0).

### Đợt review #7 (chốt cuối)
- **Chốt (final):** **Assignee = co-owner** (sửa/assign/status/complete/subtask/reminder/comment); chỉ **DELETE = creator**. Không bàn thêm.
- **Chốt (final):** Bot qua **`EnsureBot` trong `OnActivate`** (theo doc bot-accounts — đây là cách chính thức cho plugin, không phụ thuộc manifest) (mục 2.3).
- **Làm rõ:** Task cá nhân (`ChannelID==""`) — trigger: New Task dialog chọn scope *Personal* HOẶC `/task add` trong DM với bot (mục 5.1.A).
- **Chốt (final):** **Quick List/My Tasks = danh sách phẳng** (task và subtask bình đẳng, đều phải hoàn thành); không group (mục 5.5).
- **Làm rõ:** **Reset `ReminderFired=false`** khi reopen/đổi due → cho phép nhắc lại; mỗi chu kỳ fire 1 lần (mục 7).
- **Chốt (final):** Notification = **gửi DM tới assignee khi được gán** (trừ `assignee==creator`); **@mention trong kênh là NGOÀI scope** (mục 5.1.A).

### Đợt review #8 — CHỐT CUỐI (không hỏi lại)
- **Chốt:** Notification tạo/gán = **DM-only** (bot gửi DM; card đã post trong kênh). Assignee = **co-owner** (final). **Không thông báo creator** khi assignee đổi field.
- **Chốt:** `/task list mine` (KHÔNG có `/task mine` riêng).
- **Chốt:** `/task unassign <id>` (KHÔNG có `@user` — single assignee).
- **Chốt:** **KHÔNG có trạng thái `reopen`** (làm lại = `/task status todo|in_progress`, xoá CompletedAt/CancelledAt).
- **Chốt:** Bot = **`EnsureBot` trong OnActivate`**, **plugin.json KHÔNG khai báo bot** (chỉ min_server_version/settings_schema/server).
- **Chốt:** **Admin kênh được xoá** task channel (bảng 5.4).
- **Chốt:** WebSocket payload có `seq`+`updated_at` (Phụ lục B).
- **Chốt:** **Bỏ `client_token` idempotency** khỏi MVP.
- **Chốt:** Self-healing = **read defensive (bỏ qua entity not-found)**; **bỏ GC**, tolerate rác hiếm.
- **Chốt:** Reminder fire khi `now∈[due-offset, due+grace]`; due quá khứ → fire 1 lần ngay.
- **Chốt:** status → todo/in_progress ⇔ **xoá CompletedAt+CancelledAt**.
- **Chốt:** Kanban pagination = **cursor `after_order_key` + limit** (đơn giản); sort theo OrderKey (global). `/task board` mobile → **Quick List dialog**.
- **Chốt:** **“Tạo task từ tin nhắn” CÓ trong MVP** (`registerPostDropdownMenuAction`; message → summary+description; `server/message_action.go`).

### Đợt review #9 — CHỐT CUỐI (không hỏi lại)
- **Chốt:** `/task add "<tiêu đề>"` → mở dialog pre-filled tiêu đề (KHÔNG parse inline @assignee/due/desc).
- **Chốt:** Xoá = **creator HOẶC admin kênh** (channel task); assignee không xoá (sửa text 5.4 cho khớp bảng).
- **Chốt:** **KHÔNG DM** người bị `unassign`.
- **Chốt:** Hard delete — xoá index theo **key đầy đủ đã biết từ entity** (không ListKeys); chỉ ListKeys cho subtask (`idx:t:{id}:sub:`) và comment (`t:{id}:c:`) (mục 4.3).
- **Chốt:** OrderKey — task mới = **lớn hơn max toàn cục** (nằm cuối cột); kéo-thả = midpoint; rebalance khi chuỗi >50 ký tự. Thuật toán cụ thể trong Phụ lục A.
- **Chốt:** Thêm `PATCH /tasks/:id/order` body `{order_key, status}` cho kéo-thả Kanban (mục 5.3).
- **Chốt:** New Task — **desktop = React NewTaskDialog; mobile/fallback = Interactive Dialog**; cả 2 submit `POST /tasks` (không giảm duplication).
- **Khuyến nghị Phase 0**: test framework sớm (go test + CAS test); i18n wrapper server sớm; chuẩn hóa status transition; ULID `github.com/oklog/ulid`; log crash/atomic conflict.
- **Chốt (sửa nhầm lẫn):** Bảng 5.4 đồng bộ hoàn toàn co-owner — đổi dòng “Complete / Reopen” → **“Complete / Cancel”** (reopen đã bị loại bỏ); xoá = creator HOẶC admin kênh; assignee = co-owner (mọi thứ trừ xoá).
