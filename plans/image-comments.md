# Plan: Comment bằng hình ảnh (image comments)

## Mục tiêu

Cho phép comment trên task bằng ảnh: upload nhiều ảnh đồng thời (kèm hoặc không kèm text), hiển thị thumbnail trong Activity feed, click để zoom (lightbox). Tham chiếu UI chuẩn từ open-design project `mattermost-task-sidebar-3-2.html`.

## Bối cảnh kiến trúc (đã khảo sát)

- Comment = "comment-as-thread": comment là 1 MM post reply trong thread của task card. Content sống trong `post.Message`. `task_comments` chỉ lưu link (task_id ↔ post_id) + snapshot.
- Tạo comment: `POST /tasks/:id/comments` (`server/api.go:createComment`) → resolve card root/channel (`commentRoot`) → `CreatePost` → `LinkComment`.
- Composer hiện tại: text-only (`task_detail_panel.tsx`, class `task-detail__comment-box/field/input/send`).
- Client4 (`mattermost-redux/client`) đã import trong `webapp/src/client.ts`.

## Nguyên tắc (ponytail)

Reuse hạ tầng file/thumbnail sẵn có của Mattermost — KHÔNG tự tạo storage/thumbnail-server mới. Ảnh upload qua Plugin API `UploadFile`, attach vào post bằng `FileIds`. Webapp build URL ảnh từ file_id qua `Client4`.

## Quyết định kiến trúc

**Webapp upload trực tiếp qua Client4** (confirmed scope: không shared task). Webapp dùng `Client4.uploadFile(formData, full.channel_id)` upload ảnh vào channel home của task, lấy file_id, gửi `file_ids` trong `createComment`. Server attach `FileIds` vào post. Channel upload = `full.channel_id` = channel của card → nhất quán với channel post được tạo.

## Files ảnh & giới hạn

- Chỉ nhận image (`image/*`), validate cả client (`accept="image/*"`) lẫn server (MIME).
- Tối đa 5 ảnh/comment (client chặn + server chặn).
- Cho phép comment chỉ-ảnh (text rỗng khi có ảnh).
- Giới hạn size: theo MM server config (không reinvent).

## Tasks

### T1 — Webapp: upload helper qua Client4 (`client.ts`)

- `uploadCommentFiles(channelID, files): Promise<string[]>` — multipart `Client4.uploadFile(formData, 'files', channelID)` → trả `file_ids`. Validate ≤5 + image/* ở caller (composer).

### T2 — Server: createComment nhận file_ids (`server/api.go`)

- `createCommentRequest` thêm `FileIds []string`.
- Relax validation: cho phép khi `content != ""` **HOẶC** `len(FileIds) > 0`.
- Post thêm `FileIds: req.FileIds` (chỉ giữ id hợp lệ — kiểm tra `GetFileInfo`? → skip, MM tự bỏ id không tồn tại).

### T3 — Server: commentResponse thêm file_ids (`server/api.go`)

- `commentResponse` thêm `FileIDs []string json:"file_ids"`.
- `toCommentResponse` nhận thêm `fileIDs []string`; gọi thêm `post.FileIds`.
- listComments loop: `toCommentResponse(c, post.Message, false, post.FileIds)`.
- createComment: `toCommentResponse(comment, created.Message, false, created.FileIds)`.

### T4 — Webapp: types + client (`types/tasks.ts`, `client.ts`)

- `Comment` thêm `file_ids: string[]`.
- `CreateCommentInput` thêm `file_ids?: string[]`.
- `client.uploadCommentFiles(taskID, files): Promise<{file_id, name}[]>` — multipart fetch tới `/tasks/:id/comments/files`, reuse `Client4.getOptions` cho auth.

### T5 — Webapp: composer upload UI (`task_detail_panel.tsx` + `styles/index.scss`)

- State `pendingImages: {file: File, previewUrl: string}[]`.
- Nút attach (`task-detail__comment-attach`) + hidden `<input type="file" accept="image/*" multiple>` + badge số lượng.
- `.task-detail__attach-chips`: thumbnail 52px + nút xóa từng ảnh.
- `composerInputValid`: thêm điều kiện có ảnh.
- `addComment`: nếu có ảnh → upload trước → collect file_ids → `createComment({content, file_ids, channel_id})` → clear previews. Disable nút send khi đang upload.

### T6 — Webapp: hiển thị ảnh trong Activity feed

- Comment item: render `.task-detail__activity-images` grid từ `c.file_ids` (URL qua `Client4.getFileThumbnailUrl`, fallback full url).
- Click thumb → lightbox (full `Client4.getFileUrl`). Lightbox đơn giản: state `lightboxUrl`, render overlay, Esc/click đóng.

### T7 — Test

- Server: `api_test.go` — upload multipart, file_ids trong createComment + listComments, MIME reject, >5 reject.
- Webapp: `client.test.ts` (uploadCommentFiles), `task_detail_panel.test.tsx` (composerInputValid với ảnh, commentBodyText).

## Phạm vi KHÔNG làm (non-goals)

- Không paste/drag-and-drop ảnh (chỉ nút attach + picker). *Có thể thêm sau.*
- Không xóa/sửa ảnh sau khi post (theo thiết kế MM post).
- Không video/file khác (chỉ image).

## Thứ tự thực thi

T1 → T2 → T3 (server, testable) → T4 → T5 → T6 (webapp) → T7 (test) → build verify (`make`).

## Verify

- `cd server && go test ./...`
- `cd webapp && npm test`
- `make` (build plugin)
- Manual: upload 3 ảnh + text, check hiển thị + lightbox; comment chỉ-ảnh; >5 ảnh bị chặn; ảnh non-image bị chặn.
