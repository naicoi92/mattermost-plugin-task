## Context

Hành vi post task card hiện tại là **deterministic theo channel type**, không phải ngẫu nhiên:

- `server/api.go` `createTask` chỉ post card khi `created.ChannelID != ""` (slot `channel`), và post thêm DM cho assignee khi `assignee != creator` (slot `dm`).
- `webapp/.../new_task_dialog.tsx` `deriveNewTaskContext` set `channel_id` **chỉ cho channel loại `O/P/G`**; DM (`type D`) và không-context → `channel_id = ""` (personal task) → server không post card.

→ Người dùng cảm thấy "lúc có lúc không" vì phụ thuộc nơi tạo task.

Cơ chế linkage hiện tại **rất thuận lợi**:

- `task_posts` là bảng normalized: 1 task có **N** card post, mỗi cái gắn `kind` (VARCHAR(32), hiện `channel`/`dm`).
- `updateTaskCards(t)` (`server/message_attachment.go`) **đã iterate toàn bộ `task_posts`** và refresh mọi card khi task đổi → yêu cầu "shared post refresh" **đã được thỏa mãn sẵn**, không cần sửa logic refresh.
- `SetPostIDs` ghi post qua `AddPost(taskID, postID, kind)`.

## Goals / Non-Goals

**Goals:**

- New Task **luôn** post card vào originating channel, kể cả DM (fix cảm giác "im lặng").
- Thêm nút **Share Task** trong Task Detail (RHS) để post card của task đã tồn tại vào channel hiện tại.
- Shared post tự refresh khi task đổi (status/assignee/due).

**Non-Goals:**

- Đổi scope semantics của task (DM task vẫn là **personal**, member-gated). Không động đến `scope=direct` / Quick List.
- Share tới channel khác / DM khác (picker) — chỉ share vào **channel hiện tại** (theo quyết định user).
- Security overhaul `CanUserViewTask` (issue #157) — out of scope, chỉ note dependency.
- Migration task DM cũ (personal) sang có card — chỉ task tạo mới áp dụng hành vi mới.

## Decisions

### Decision 1: Decoupled announce-posting cho DM (giữ Personal scope)

**Chọn:** Tách biệt "scope của task" khỏi "nơi post card".

- Task tạo trong DM vẫn có `channel_id = ""` (personal, member-gated) — scope semantics **không đổi**.
- Client gửi thêm `post_channel_id` = originating channel (channel hoặc DM) trong create request.
- Server `createTask`: nếu `task.channel_id != ""` → post vào `task.channel_id` (giữ nguyên channel tasks); **else nếu `post_channel_id != ""`** → post vào `post_channel_id` (trường hợp DM/personal-in-channel).
- Channel tasks (`O/P/G`): path hiện tại, **không đổi**. Client cho channel task có thể không cần gửi `post_channel_id` (redundant), hoặc gửi cùng giá trị — server ưu tiên `task.channel_id`.

**Additive, không dedup:** Announce card đi vào originating channel (vd DM(user, partner)); assignee-bot-DM (pre-existing) đi vào DM(bot, assignee) — **hai surface khác nhau, không trùng**. Announce-posting là cộng thêm, KHÔNG thay thế assignee-notification. Giữ logic slot `dm` hiện tại nguyên vẹn (surgical, consistent với channel task vốn đã post cả channel card + assignee bot-DM).

**Alternatives:**

- *DM-channel-scoped (Option A)*: set `channel_id = DM id` cho DM task → server post sẵn, ít code hơn. **Bị loại** vì đổi scope semantics (personal → channel-scoped), ảnh hưởng `scope=direct` trong listing (trùng lặp/gap cho DM task không assignee), và task DM cũ không migrate. User đã chọn giữ Personal.

### Decision 2: Share = "post card của task đã tồn tại + linkage"

- Endpoint mới: `POST /tasks/{id}/share` với body `{ "channel_id": "<đích>" }`.
- Server: `postCard(channelID, task)` (reuse) → `AddPost(taskID, postID, kind=share)` → trả `{ "post_id": "..." }`.
- **Refresh**: tự động qua `updateTaskCards` hiện có (iterate tất cả `task_posts`). Không sửa logic refresh.
- **Kind mới**: thêm `PostKindShare = "share"` vào `model/task_post.go` + `IsValidPostKind`. VARCHAR(32) nhận giá trị mới mà **không cần DDL**.
- **Single-share invariant (sửa sau review)**: bảng `task_posts` có constraint `UNIQUE(task_id, kind)` (migration 000007) — mỗi task có tối đa MỘT card mỗi kind (channel/dm/share). Do đó một task chỉ share được vào **ĐÚNG MỘT channel**. Share lại cùng channel → idempotent (trả post id cũ); share sang channel **khác** khi đã có share → **409** "task already shared in another channel" (không tạo card mồ côi, không vi phạm constraint). **User chọn single-share** thay vì multi-channel (multi-channel cần drop constraint qua migration + verify code channel/dm chịu nhiều row/kind — scope lớn hơn). Nếu sau này cần multi-channel: drop `uq_posts_task_kind` (migration comment đã note sẵn path này).

### Decision 3: Auth & guard cho share

- Caller **phải là member** của `channel_id` đích (kiểm `p.API.GetChannelMember`) — chống post vào channel không có quyền.
- Caller **phải xem được task**: kiểm viewer permission (align với issue #157 `CanUserViewTask`). Vì #157 chưa done, bản tạm: reject nếu task không có member là caller (creator/assignee). Note rõ đây là placeholder chờ #157.
- **Dedup per-channel + single-share**: nếu task đã có card (`task_posts`) trong cùng `channel_id` → trả **200 idempotent** với post id cũ (chống spam share lặp). Nếu đã có một share-kind card ở channel **khác** → trả **409** (single-share invariant, xem Decision 2).

### Decision 4: Share button UX

- Nút **Share Task** ở header Task Detail (RHS), cạnh các action hiện có.
- Click → gọi `POST /tasks/{id}/share` với `channel_id` = current channel id (lấy từ RHS context / Redux current channel).
- Success: toast/ephemeral "Đã share vào #channel"; card xuất hiện trong channel. Error: toast lỗi.
- Disable/hide khi không có current channel context (vd. mở RHS từ nơi không có channel).

## Risks / Trade-offs

- **[Asymmetry: task personal nhưng card nằm trong DM]** → Card trong DM chỉ visible cho 2 participant, vốn là creator + assignee (trường hợp assignee = DM partner). Không leak. Edge case assignee ≠ DM partner: partner thấy card của task họ không phải member → **acceptable** (partner thấy thông báo trong cuộc trò chuyện chung), document rõ.
- **[Partner thấy 2 card khi assignee = DM partner]** → Announce card trong DM(user,partner) + bot-DM trong DM(bot,partner) là 2 surface khác nhau, nhất quán với channel task (channel card + assignee bot-DM). **Accepted**, không dedup. Nếu sau này muốn giảm spam là refinement riêng.
- **[Share vào channel không có quyền]** → Mitigation: check channel membership (Decision 3).
- **[Share khi task không viewable (security gap #157)]** → Mitigation: placeholder viewer check; flag #157 là prerequisite chính thức.
- **[Kind mới `share` bị store reject nếu quên cập nhật validation]** → Mitigation: cập nhật `IsValidPostKind` + unit test.

## Migration Plan

- **Schema**: không migration. `task_posts.kind` là VARCHAR(32); thêm giá trị `"share"` không cần DDL. `PostKindShare` chỉ là hằng số code.
- **Data**: task DM cũ (personal, không card) **không** tự nhận card. Chỉ task tạo **sau** thay đổi mới có announce card. Hành vi mới áp dụng forward — document trong changelog.
- **Rollback**: revert code là đủ (không có schema change). Card đã share (kind=share) vẫn render bình thường khi rollback (chỉ mất khả năng share mới).

## Open Questions

1. **Share dedup**: trả `409 Conflict` hay `200` idempotent (trả post id cũ) khi share lặp vào cùng channel? → Recommend `200` idempotent (đơn giản cho client).
2. **#157 dependency**: placeholder viewer-check đủ an toàn cho bản này, hay block share tới khi #157 done? → Recommend placeholder + note rủi ro, vì #157 là issue riêng.
3. **Share vào DM (current channel là DM)**: cho phép? → Recommend cho phép (như channel thường), card post vào DM hiện tại.
