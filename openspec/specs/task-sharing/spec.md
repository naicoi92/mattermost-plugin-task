# Spec: Task Sharing

## Purpose

Đặc tả khả năng share một task đã tồn tại vào channel khác (endpoint `POST /tasks/{id}/share`): post task card vào channel đích, ghi linkage để refresh khi task thay đổi, và đảm bảo authorization + idempotency + single-share.

## Requirements

### Requirement: Share endpoint posts an existing task card to a channel

Hệ thống SHALL cung cấp endpoint `POST /tasks/{id}/share` nhận `{ "channel_id": "<đích>" }`, post task card của task đã tồn tại vào channel đích, và trả về id của post mới tạo.

#### Scenario: Share task thành công vào channel

- **WHEN** client gọi `POST /tasks/{id}/share` với `channel_id` hợp lệ và caller là member của channel đó
- **THEN** hệ thống post task card vào channel đích (dạng `custom_task` props, giống card tạo ban đầu)
- **AND** trả HTTP 200 với `{ "post_id": "<id>" }`

#### Scenario: Share khi task không tồn tại

- **WHEN** client gọi share với `id` không tồn tại
- **THEN** hệ thống trả HTTP 404

#### Scenario: Share thiếu channel_id

- **WHEN** client gọi share mà không có `channel_id`
- **THEN** hệ thống trả HTTP 400 (bad request)

### Requirement: Shared post links to the task and refreshes on task changes

Shared post MUST được ghi linkage `task_posts` (kind = `share`). Khi task thay đổi (status, assignee, due), shared post SHALL tự refresh qua cơ chế `updateTaskCards` hiện có (iterate toàn bộ `task_posts`).

#### Scenario: Shared post được ghi linkage

- **WHEN** share thành công
- **THEN** có một row `task_posts` với `task_id`, `post_id`, `kind = "share"`

#### Scenario: Shared post refresh khi task đổi status

- **WHEN** task đã share đổi status (vd sang `done`)
- **THEN** shared post trong channel đích được cập nhật props/attachment để phản ánh status mới
- **AND** card channel/dm gốc của task cũng được refresh

#### Scenario: Xóa task thì shared post được xử lý nhất quán với các card khác

- **WHEN** task đã share bị xóa
- **THEN** hành vi với shared post khớp với hành vi xóa của card channel/dm hiện tại (cùng cơ chế cleanup `task_posts`)

### Requirement: Share respects authorization

Caller MUST là member của `channel_id` đích và MUST có quyền xem task. Hệ thống MUST reject share nếu caller không thỏa điều kiện.

#### Scenario: Caller không phải member của channel đích

- **WHEN** caller share vào channel mà caller không phải member
- **THEN** hệ thống trả HTTP 403

#### Scenario: Caller không có quyền xem task

- **WHEN** caller share task mà caller không phải member của task (placeholder chờ `CanUserViewTask` issue #157)
- **THEN** hệ thống trả HTTP 403

### Requirement: Share is idempotent per channel (no duplicate spam)

Nếu task đã có card (`task_posts`) trong cùng `channel_id` đích, hệ thống SHALL không tạo card mới mà trả về dạng idempotent.

#### Scenario: Share lặp vào cùng channel thì không duplicate

- **WHEN** caller share task vào channel C, và task đã có card trong C
- **THEN** hệ thống KHÔNG post thêm card mới
- **AND** trả HTTP 200 với post id của card đã tồn tại trong C

### Requirement: Share là single-share (một task — một channel share)

Vì `task_posts` có constraint `UNIQUE(task_id, kind)`, một task chỉ có tối đa MỘT card kind=`share`. Hệ thống MUST reject share sang channel **khác** khi task đã được share ở nơi khác, thay vì vi phạm constraint (tránh card mồ côi + lỗi 500). (Multi-channel share là beyond-MVP — cần drop constraint qua migration.)

#### Scenario: Share sang channel khác khi đã share ở nơi khác → 409

- **WHEN** task đã có card kind=`share` ở channel A, và caller share lại sang channel B (B ≠ A)
- **THEN** hệ thống trả HTTP 409 "task already shared in another channel"
- **AND** KHÔNG post thêm card nào vào B (không card mồ côi, không vi phạm constraint)
