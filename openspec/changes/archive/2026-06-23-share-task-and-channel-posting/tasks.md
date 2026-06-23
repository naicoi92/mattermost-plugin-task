# Tasks — share-task-and-channel-posting

Strict TDD (`make test`). Mỗi nhóm server/webapp: viết test RED trước, implement GREEN, refactor. Tham chiếu `specs/` (WHAT) và `design.md` (HOW).

## 1. Server foundation: PostKindShare

- [x] 1.1 Viết test RED: `model.IsValidPostKind("share")` trả true; store `AddPost` reject kind không hợp lệ (vd `"bogus"`)
- [x] 1.2 Implement: thêm `PostKindShare = "share"` vào `server/model/task_post.go` + cập nhật `IsValidPostKind`; GREEN
- [x] 1.3 Verify `go test ./server/...` qua nhóm này

## 2. Server: decoupled announce-posting khi tạo task (FIX)

- [x] 2.1 Viết test RED trong `server/api_test.go`: tạo task với `channel_id` rỗng + `post_channel_id` = DM id → có đúng 1 card post vào DM, `task_posts` được link, `task.channel_id` vẫn rỗng
- [x] 2.2 Implement: thêm field `PostChannelID` vào `createTaskRequest` (`server/api.go`); trong `createTask`, khi `created.ChannelID == ""` && `post_channel_id != ""` thì `postCard(post_channel_id)` + link `task_posts`; GREEN
- [x] 2.3 Verify nhóm 2: `go test ./server/...` (không dedup — announce-posting là cộng thêm, assignee-bot-DM giữ nguyên, consistent với channel task)

## 3. Server: Share endpoint + auth + idempotency (FEATURE)

- [x] 3.1 Viết test RED (`server/api_test.go`): `POST /tasks/{id}/share` `{channel_id}` → HTTP 200, card post vào channel, `task_posts` kind=`share`, body `{post_id}`
- [x] 3.2 Implement handler `shareTask` (`server/api.go`): `postCard` + `AddPost(kind=share)` + trả `{post_id}`; đăng ký route `POST /tasks/{id}/share`; GREEN
- [x] 3.3 Viết test RED: 404 khi task không tồn tại; 400 khi thiếu `channel_id`
- [x] 3.4 Implement error paths (load task not found → 404; empty channel_id → 400); GREEN
- [x] 3.5 Viết test RED: 403 khi caller không phải member của `channel_id`; 403 khi caller không phải member của task (placeholder viewer-check)
- [x] 3.6 Implement auth: `p.API.GetChannelMember(channelID, userID)` check + viewer-check placeholder; GREEN
- [x] 3.7 Viết test RED: share lặp vào cùng channel → 200 trả post id cũ, không tạo card mới
- [x] 3.8 Implement per-channel dedup: list `task_posts` cho task, nếu đã có post trong `channel_id` thì trả idempotent; GREEN
- [x] 3.9 Verify nhóm 3: `go test ./server/...`

## 4. Webapp: New Task gửi post_channel_id (FIX)

- [x] 4.1 Viết test RED (`webapp/src/components/new_task_dialog/new_task_dialog.test.tsx`): submit task trong DM context → `input.channel_id` rỗng NHƯNG `input.post_channel_id` = originating channel id
- [x] 4.2 Implement: trong `submit()`, set `input.post_channel_id` = originating `channelID` prop (khi có); thêm field vào `CreateTaskInput` type (`webapp/src/types/tasks.ts`); GREEN
- [x] 4.3 Verify: `cd webapp && npm test`

## 5. Webapp: Share client + button (FEATURE)

- [x] 5.1 Viết test RED (`webapp/src/client.test.ts`): `client.shareTask(id, channelID)` gọi `POST /tasks/:id/share` với body `{channel_id}`
- [x] 5.2 Implement `shareTask` trong `webapp/src/client.ts` + type trả `{post_id}`; GREEN
- [x] 5.3 Viết test RED (`task_detail_panel.test.tsx`): render nút Share; click → dispatch/share với current channel id; disable khi không có current channel — *SKIP (decided): panel theo pattern pure-helper (không RTL/provider harness); client.shareTask đã unit-test; nút verify qua lint/typecheck + type; manual smoke = task 6.4*
- [x] 5.4 Implement nút Share Task ở header `task_detail_panel.tsx`: lấy current channel id (Redux), gọi `shareTask`, toast success/error; GREEN
- [x] 5.5 Verify: `cd webapp && npm test`

## 6. Verification, i18n, smoke

- [x] 6.1 Thêm i18n strings cho nút Share + toast (en `webapp/src/i18n/en.json` + vi), giữ key technical
- [x] 6.2 `make test` (full suite server + webapp) — GREEN
- [x] 6.3 `cd webapp && npm run lint` — clean
- [ ] 6.4 Manual smoke: (a) tạo task trong channel → card post; (b) tạo trong DM → card post vào DM; (c) share từ Task Detail → card trong current channel; (d) đổi status → shared card + card gốc cùng refresh; (e) share lặp → không duplicate
