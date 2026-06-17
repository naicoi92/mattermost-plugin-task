# Plan: Server-side i18n wrapper + shared translation files

Triển khai theo issue #4: cung cấp chuỗi dịch dùng chung cho server và webapp từ một nguồn duy nhất.

## Mục tiêu

- Tạo `assets/i18n/en.json` và `assets/i18n/vi.json` dạng flat key-value.
- Seed các key cần thiết cho Phase 0/1: notification DM/ephemeral, slash command response, validation/error chung.
- Viết `server/i18n.go` dùng `//go:embed assets/i18n/*.json`, expose `T(locale, key, args...)` với fallback tiếng Anh khi thiếu locale hoặc key.
- Cập nhật Makefile để copy `assets/i18n/*.json` → `webapp/i18n/` khi build, đảm bảo webapp dùng chung nguồn.

## Các file thay đổi / tạo mới

| File | Hành động | Ghi chú |
|---|---|---|
| `assets/i18n/en.json` | Tạo mới | Nguồn tiếng Anh, flat key-value |
| `assets/i18n/vi.json` | Tạo mới | Bản dịch tiếng Việt tương ứng |
| `assets/embed.go` | Tạo mới | `//go:embed i18n/*.json` trong package `assets` |
| `server/i18n.go` | Tạo mới | Load bundle, expose `T(locale, key, args...)` |
| `build/custom.mk` | Sửa | Thêm target copy i18n vào webapp (nếu dùng) |
| `Makefile` | Sửa | `webapp` phụ thuộc target copy i18n |
| `server/i18n_test.go` | Tạo mới (nếu cần) | Unit test fallback và argument interpolation |

## Chi tiết kỹ thuật

### Format JSON

```json
{
  "task.created": "Task created",
  "task.list.empty": "No tasks found",
  ...
}
```

Không dùng nested object. Cùng một file sẽ được webapp load qua `registerTranslations` ở Phase 3.

### API Go

```go
package main

func T(locale, key string, args ...interface{}) string
func MustLoadI18n() *I18n
```

- `T` dùng `text/template` hoặc `fmt.Sprintf` style placeholder cho argument interpolation. Do issue yêu cầu `args...`, dùng `fmt.Sprintf(message, args...)` là đủ.
- Fallback: nếu locale không tồn tại, dùng `en`; nếu key không tồn tại trong locale đã chọn (kể cả `en`), trả về key gốc.
- Load một lần khi plugin activate (hoặc dùng package-level init). Do `//go:embed` bundle vào binary, không phụ thuộc file system runtime.

### Makefile

Thêm target `i18n-copy`:

```makefile
.PHONY: i18n-copy
i18n-copy:
 mkdir -p webapp/i18n
 cp assets/i18n/*.json webapp/i18n/
```

Và sửa target `webapp` để phụ thuộc `i18n-copy`:

```makefile
webapp: webapp/node_modules i18n-copy
```

### Seed keys Phase 0/1

Bao gồm các nhóm:

1. **Generic / errors**
   - `error.generic`
   - `error.invalid_arguments`
   - `error.not_found`
   - `error.permission_denied`
   - `error.unknown_command`
   - `error.empty_command`

2. **Task lifecycle**
   - `task.created`
   - `task.updated`
   - `task.deleted`
   - `task.not_found`
   - `task.list.empty`
   - `task.status.invalid`
   - `task.completed`
   - `task.cancelled`
   - `task.reopened`

3. **Assignee / reminder / comment / subtask**
   - `task.assigned`
   - `task.unassigned`
   - `reminder.set`
   - `reminder.removed`
   - `comment.added`
   - `subtask.added`

4. **Notifications**
   - `notification.assigned`
   - `notification.completed`
   - `notification.commented`
   - `notification.reminder`
   - `notification.overdue`

5. **Slash command help**
   - `command.help.header`
   - `command.help.add`
   - `command.help.list`
   - `command.help.show`
   - `command.help.status`
   - `command.help.done`
   - `command.help.cancel`
   - `command.help.assign`
   - `command.help.unassign`
   - `command.help.subtask`
   - `command.help.comment`
   - `command.help.remind`
   - `command.help.board`
   - `command.help.search`

## Acceptance Criteria (từ issue)

- [ ] `assets/i18n/en.json` và `assets/i18n/vi.json` tồn tại, flat key-value.
- [ ] Ít nhất có các key cần cho Phase 0/1 notification và slash command response.
- [ ] `server/i18n.go` embed `assets/i18n/*.json` và expose `T(locale, key, args...)`.
- [ ] `T` fallback về tiếng Anh khi thiếu locale/key.
- [ ] Makefile copy `assets/i18n/*.json` → `webapp/i18n/` trong build.

## Verification

1. `go test ./server/...` pass.
2. `go build ./server` pass.
3. `cp assets/i18n/*.json webapp/i18n/` chạy đúng và file webapp/i18n/en.json, vi.json cập nhật.
