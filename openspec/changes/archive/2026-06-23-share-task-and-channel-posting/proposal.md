## Why

Khi tạo New Task, task card đôi khi **không** được post ra channel. Nguyên nhân không phải ngẫu nhiên mà là **hành vi deterministic theo design hiện tại**: chỉ task tạo trong channel loại `O/P/G` mới được post card; task tạo trong **DM (type D)** hoặc **không có channel context** thì `channel_id` rỗng → server không post (xem `server/api.go` `createTask` và `webapp/.../new_task_dialog.tsx` `deriveNewTaskContext`). Người dùng cảm thấy "lúc có lúc không" vì nó phụ thuộc vào nơi tạo task. Cần hành vi **nhất quán**: tạo task ở đâu thì post card ở đó, kể cả DM.

Đồng thời, plugin **thiếu khả năng share** một task đã tồn tại vào channel để team cùng theo dõi. Cần nút **Share Task** trong Task Detail (RHS) để post task card vào channel hiện tại, với linkage refresh khi task đổi (dùng infra `task_posts` đã có).

## What Changes

**Fix — deterministic creation posting:**

- New Task luôn post task card vào **originating channel** (channel mà user đang ở khi tạo), kể cả DM. Chấm dứt tình trạng "im lặng" khi tạo trong DM.
- Thay đổi webapp `deriveNewTaskContext`: DM channel cũng set `channel_id` bằng id của DM channel (vẫn giữ gợi ý assignee = DM partner).
- **Hệ quả semantic (cần lưu ý)**: task tạo trong DM sẽ chuyển từ *personal task* (chỉ creator + assignee thấy) thành *channel-scoped task* gắn với DM channel (cả hai bên DM thấy qua scope `channel`). Đây là tradeoff cần giải quyết trong `design.md`.

**Feature — Share Task button:**

- Thêm nút **Share Task** trong Task Detail (RHS). Bấm → post task card vào channel hiện tại.
- Shared post được **link với task** qua `task_posts` và **tự refresh** khi task đổi (status, assignee, due) — giống card tạo ban đầu.
- Server: thêm endpoint mới (ví dụ `POST /tasks/{id}/share`) nhận `channel_id` đích, reuse `postCard`, ghi `task_posts`.
- Webapp: thêm button + handler trong `task_detail_panel`, gọi API client mới.

## Capabilities

### New Capabilities

- `task-creation-posting`: Hành vi post task card ngay khi tạo task — **deterministic**, luôn post vào originating channel kể cả DM, kèm linkage `task_posts` để card có thể refresh sau này.
- `task-sharing`: Hành vi share một task **đã tồn tại** vào channel hiện tại qua nút Share trong Task Detail, kèm linkage và refresh khi task thay đổi.

### Modified Capabilities

<!-- Chưa có spec nào tồn tại trong openspec/specs/ — toàn bộ là capability mới. -->

## Impact

- **Webapp**:
  - `components/new_task_dialog/new_task_dialog.tsx` — `deriveNewTaskContext` (DM → set `channel_id`).
  - `components/task_detail_panel/task_detail_panel.tsx` — thêm nút Share Task + handler.
  - `client.ts` — thêm API call share; `types/tasks.ts` — type nếu cần.
- **Server**:
  - `api.go` — route + handler `POST /tasks/{id}/share`; xác nhận posting trong `createTask`.
  - `message_attachment.go` — reuse `postCard`; mở rộng linkage `task_posts` cho shared post.
  - `task/service.go` — có thể cần `SetPostIDs`/linkage đa-post (nhiều card cho 1 task).
- **Semantic change**: DM task từ personal → DM-channel-scoped. Cần verify tương tác với scope `direct` (partner-based) để tránh trùng lặp trong list. Quyết định implementation cuối cùng (scope change vs. decoupled post) thuộc về `design.md`.
- **Tests**: strict TDD (`make test`). Unit tests cho `deriveNewTaskContext` (case DM), server share endpoint, và linkage refresh.
