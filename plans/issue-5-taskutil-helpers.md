# Plan — Issue #5: Task helpers (ULID, status state machine, OrderKey)

Link: <https://github.com/naicoi92/mattermost-plugin-task/issues/5>
Branch: `i5`
Dependency: Issue #2 (model `Task`, `Comment`, status constants) — đã merge.

## Mục tiêu

Tạo package `server/taskutil/` chứa các helper dùng chung (reused bởi slash commands + REST handlers):

1. `GenerateULID() string` — sinh ID lexicographically sortable.
2. `ApplyStatus(...)` — state machine cập nhật `CompletedAt`/`CancelledAt`/`UpdatedAt`.
3. `NextOrderKey(maxOrderKey string) string` — OrderKey lớn hơn max hiện tại (task mới nằm cuối cột).

## Acceptance Criteria (từ issue)

- [x] Thêm dependency `github.com/oklog/ulid` (hoặc `/v2` — xem Q1).
- [x] `GenerateULID() string`.
- [x] `ApplyStatus(task, newStatus)`:
  - `todo` / `in_progress` → clear `CompletedAt` + `CancelledAt`
  - `done` → set `CompletedAt`, clear `CancelledAt`
  - `cancelled` → set `CancelledAt`, clear `CompletedAt`
  - update `UpdatedAt`, return task.
- [x] `NextOrderKey(maxOrderKey)` → key lexicographically lớn hơn (append suffix, vd `m` → `m0`).
- [x] Đặt trong package riêng `server/taskutil/`.

## Quyết định cần chốt (hỏi user)

- **Q1 — ULID version:** `oklog/ulid/v2` (v2.1.1, maintained 2025-05) hay `oklog/ulid` (v1 path theo đúng chữ issue)?
- **Q2 — ApplyStatus signature:** thêm param `now int64` (pure, testable) hay dùng `time.Now()` nội bộ (đúng chữ issue)?

## Thiết kế chi tiết

### Package layout

```
server/taskutil/
  ulid.go       // GenerateULID
  status.go     // ApplyStatus
  orderkey.go   // NextOrderKey
  *_test.go     // test cho từng file
```

Không tách sub-package — 3 file gọn trong 1 package `taskutil`.

### GenerateULID

```go
func GenerateULID() string {
    return ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy()).String()
}
```

- `MustNew` panic chỉ khi entropy reader lỗi (không xảy ra với `DefaultEntropy` = crypto/rand). Plugin process không nên tiếp tục nếu không sinh được ID → panic ở boot/đường nghiêm trọng là chấp nhận được (sẽ không xảy ra trong thực tế).
- Test: verify định dạng 26 ký tự Crockford base32, unique, monotonic-ish (sort theo thời gian sinh).

### ApplyStatus (tuỳ Q2)

- Logic switch theo `model.Status*` constants (dùng lại từ issue #2, không hardcode string).
- Trả về cùng pointer `*model.Task` để chain.

### NextOrderKey

```go
func NextOrderKey(maxOrderKey string) string {
    if maxOrderKey == "" {
        return "n" // task đầu: midpoint giữa keyMin="a" (PLAN Phụ lục A) và keyMax="z"
    }
    return maxOrderKey + "0"
}
```

- `""` → `"n"` (giá trị khởi tạo hợp lý, dư khoảng trống cả 2 phía cho midpoint kéo-thả Phase 4).
- Non-empty → `max + "0"` (`"m" > ""`, `"m0" > "m"` vì prefix ngắn hơn nhỏ hơn).
- Test: monotonic (mỗi lần gọi liên tiếp ra key lớn hơn), `""` → `"n"`.

## Verification

- `go test ./server/taskutil/...` pass.
- `go vet ./...` sạch.
- `golangci-lint run` (nếu có) sạch.
- `go build ./...` / `make dist` build OK.

## Out of scope (sẽ làm ở phase sau)

- Midpoint algorithm cho drag-and-drop (Phase 4 — Phụ lục A).
- Tích hợp helper vào command/REST handler (issue sau).
