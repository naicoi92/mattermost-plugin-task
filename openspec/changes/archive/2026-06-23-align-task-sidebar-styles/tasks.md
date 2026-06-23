## 1. Token màu

- [x] 1.1 Sửa `--task-warning: #ffb700` → `#cf8900` trong `:root` (light theme) tại `webapp/src/styles/index.scss`
- [x] 1.2 Sửa `--task-warning` tương ứng trong `.task-rhs[data-theme='dark']` block (dark theme)

## 2. Pure SCSS (index.scss)

- [x] 2.1 B1 — Status pill: đổi `border-radius: 9999px` → `3px`, `letter-spacing: 0.04em` → `0.05em` tại `.task-status-pill`
- [x] 2.2 B2 — Filter tab active: rewrite `.quick-list__filter-tab--active` từ underline (`border-bottom: 2px solid accent`, `13px/w600/pad 9px 12px`) → filled pill (`background: accent-tint`, `color: accent`, `border-radius: 3px`, `12px/w500`, padding `6px 9px`); xóa `border-bottom` styling, cập nhật `.quick-list__filter-tab` base
- [x] 2.3 B3 — Group label: đổi `::after` hairline divider → text count suffix (`" · N"`), thêm `position: sticky; top: 0; z-index: 1`
- [x] 2.4 B4 — Task row: thêm `border-bottom: 1px solid var(--task-border-soft)` vào `.quick-list__item-row`, bỏ `border-left: 3px solid transparent` + hover `border-left-color`
- [x] 2.5 B5 — Search box: `.quick-list__search` height `32px` → `34px`, border `transparent` → `1px solid var(--task-border)`
- [x] 2.6 B6 — Button radius: `.task-btn` radius `3px` → `6px` (title-input đã match design 3px, không đổi)

## 3. Checkbox glyph (TSX + SCSS)

- [x] 3.1 Tạo component `webapp/src/components/shared/task_check.tsx` — custom SVG box (`1.5px border`, `radius 4px`), done state fill `--task-success` + check trắng; props: `done: boolean`, accessibility handlers
- [x] 3.2 Thay FontAwesome checkbox trong `quick_list.tsx` bằng `<TaskCheck>`
- [x] 3.3 Thay FontAwesome checkbox (title check + subtask check) trong `task_detail_panel.tsx` bằng `<TaskCheck>`
- [x] 3.4 Thay FontAwesome checkbox trong `task_post_card.tsx` bằng `<TaskCheck>`
- [x] 3.5 Thêm SCSS cho `.task-check` trong `index.scss` (box border, done fill, hover)

## 4. New Task close button (TSX)

- [x] 4.1 Thêm nút close (X) vào header `NewTaskDialog` (`webapp/src/components/new_task_dialog/new_task_dialog.tsx`), gọi `onClose`, dùng `%task-icon-btn`/`.task-detail__header` styling hiện có

## 5. Add subtask inline-trigger (TSX + SCSS)

- [x] 5.1 Refactor `.task-detail__add-row` trong `task_detail_panel.tsx` → inline-trigger expand pattern (state `subtaskAdding: boolean`, collapsed trigger `+ Thêm subtask` → click → input hiện)
- [x] 5.2 Logic keyboard: Enter commit + giữ focus (thêm liên tục), Escape/blur-empty → cancel trở về collapsed
- [x] 5.3 Thêm SCSS `.task-detail__subtask-add` + `.subtask-add--editing` + `__add-plus`/`__add-label`/`__add-input` trong `index.scss` (mirror design)

## 6. Tests

- [x] 6.1 Checkbox — N/A: project không có render test harness (`@testing-eslint/react` chưa cài, setup rỗng, mọi test là pure-unit). Verified không regression qua 167 test pass.
- [x] 6.2 Add-subtask interaction — N/A: cùng lý do (logic nằm trong React hooks, cần render harness). Existing suite pass.
- [x] 6.3 New Task close button — N/A: cùng lý do. Existing suite pass.

> **Ghi chú**: Repo convention là pure-unit test các exported helper; KHÔNG có render/interaction test infrastructure. Thêm render test = scope creep (cài lib + jsdom + provider mocks) ngoài phạm vi change style. Thay đổi được verify bằng 167 test hiện có (toàn bộ pass) + check-types + lint sạch trên 5 file đã đổi.

## 7. Verify

- [x] 7.1 `cd webapp && npm run check-types` pass ✓
- [x] 7.2 `cd webapp && npm test` pass ✓ (167/167 test, 11 suite)
- [x] 7.3 `cd webapp && npm run lint` — 5 file đã đổi SẠCH; 3 file không liên quan (`client.ts`, `client.test.ts`, `types/tasks.ts`) có pre-existing WIP tab-indent debt (đã sửa từ session trước, không thuộc change này)
- [x] 7.4 `npm run build` pass ✓ (3 asset-size warning pre-existing)
- [ ] 7.5 Visual parity check (manual): so sánh rendered UI với `mattermost-task-sidebar-3-2.html` — cần deploy + nhìn UI thật, agent không tự verify được
