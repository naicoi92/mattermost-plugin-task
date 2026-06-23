## Why

Giao diện webapp (Quick List + Task Detail + New Task) hiện lệch nhiều giá trị CSS so với thiết kế tham chiếu `mattermost-task-sidebar-3-2.html` (project open-design `c80d39a2`). Token màu amber sai, pill/filter-tab/task-row có shape khác, checkbox dùng FontAwesome thay vì SVG, và 2 layout (New Task header + add subtask) lệch cả cấu trúc DOM. UI trông chưa tinh tế và không đồng nhất với thiết kế đã duyệt.

## What Changes

### Token màu

- Sửa `--task-warning` từ `#ffb700` (vàng) → `#cf8900` (cam đậm, light theme) và `#e6a23c` (dark theme) trong `index.scss`. Ảnh hưởng toàn bộ chỗ amber: due "soon", priority important.

### Pure SCSS (6 chỗ, chỉ `index.scss`)

- **B1. Status pill**: `border-radius: 9999px` → `3px` (rounded-rect), `letter-spacing: 0.04em` → `0.05em`.
- **B2. Filter tab active**: từ underline (`border-bottom: 2px solid accent`, `13px/w600/pad 9px 12px`) → filled pill (`accent-tint` bg, accent color, `radius 3px`, `12px/w500/pad 6px 9px`).
- **B3. Group label**: từ `::after` hairline divider flex-fill → text suffix `" · N"` + `position: sticky; top: 0; z-index: 1`.
- **B4. Task row**: từ `border: 0` + hover `border-left: 3px accent` → `border-bottom: 1px border-soft` (separator lines) + hover = bg only.
- **B5. Search box**: `height: 32px` → `34px`, `border: transparent` → `1px solid border` (luôn visible).
- **B6. Button radius**: `.task-btn` radius `3px` → `6px`, padding theo design. (`.task-detail__title-input` giữ `3px` vì đã match design.)

### Checkbox glyph (3 TSX + SCSS)

- **C**: Thay FontAwesome `fa-square-o`/`fa-check-square` bằng custom SVG box (`1.5px border`, `radius 4px`) + SVG check. Done state màu `--task-success` (xanh lá) thay vì accent (xanh dương). Files: `quick_list.tsx`, `task_detail_panel.tsx`, `task_post_card.tsx`.

### Layout (2 TSX + SCSS)

- **E. New Task close**: Thêm nút close (X) vào header `NewTaskDialog`, gọi `onClose`. Header có 3 element: back + title + close.
- **F. Add subtask**: Đổi `.task-detail__add-row` (input + button luôn mở) → inline-trigger expand pattern (collapsed: `+ Thêm subtask` → expanded: hidden label + input). Submit Enter (giữ focus thêm liên tục), cancel Escape/blur-empty.

### Out of scope

- **Priority naming**: giữ enum `standard/important/urgent` (không đổi sang `low/med/high` — cross-cutting server/types/i18n).
- **Comment / activity feed**: tách proposal riêng.
- **Dark navy header**: platform constraint (Mattermost native RHS header).
- **TailwindCSS**: giữ SCSS hiện tại.

## Capabilities

### New Capabilities

- `task-sidebar-styling`: Tiêu chuẩn hình ảnh (visual parity) cho webapp sidebar — token màu, shape của pill/tab/row/checkbox, và layout của New Task header + add subtask. Định nghĩa các yêu tố CSS phải khớp thiết kế tham chiếu.

### Modified Capabilities
<!-- Không có — styling là capability mới, không sửa behavior spec hiện có -->

## Impact

- **Code**: `webapp/src/styles/index.scss` (token + 6 SCSS block + checkbox + subtask CSS), `webapp/src/components/task_sidebar/quick_list.tsx` (checkbox), `webapp/src/components/task_detail_panel/task_detail_panel.tsx` (checkbox + add-row → subtask-add), `webapp/src/components/new_task_dialog/new_task_dialog.tsx` (close button), `webapp/src/components/task_post_card/task_post_card.tsx` (checkbox).
- **Tests**: snapshot/visual test cho 3 component checkbox cần update; logic test cho add-subtask interaction (collapsed/expanded/Enter/Escape).
- **APIs**: không thay đổi — thuần frontend.
- **Dependencies**: không thêm (giữ FontAwesome cho icon khác, chỉ bỏ ở checkbox).
- **Risk**: thấp — CSS thuần + 3 component có test phủ. Token warning ảnh hưởng rộng nhưng là 1 dòng + đã scoped.
