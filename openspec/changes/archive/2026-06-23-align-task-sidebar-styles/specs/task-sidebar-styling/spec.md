## ADDED Requirements

### Requirement: Token màu amber phải khớp thiết kế

`--task-warning` token trong `index.scss` SHALL có giá trị `#cf8900` (light theme) và `#e6a23c` (dark theme) — cam đậm, không phải `#ffb700` (vàng).

#### Scenario: Due "soon" hiển thị cam đậm

- **WHEN** một task có due date trong hôm nay (due soon) và status open
- **THEN** due chip hiển thị màu `--task-warning` = `#cf8900`, không phải vàng `#ffb700`

#### Scenario: Priority important hiển thị cam đậm

- **WHEN** một task có priority = important
- **THEN** priority dot/pill hiển thị màu `--task-warning` = `#cf8900`

### Requirement: Status pill phải là rounded-rect, không phải pill tròn

Status pill (`.task-status-pill`) SHALL có `border-radius: 3px` (rounded-rect) và `letter-spacing: 0.05em`, không phải `border-radius: 9999px` (pill tròn).

#### Scenario: Status pill render bo góc chữ nhật

- **WHEN** StatusPill render cho status bất kỳ (todo/in_progress/done/cancelled)
- **THEN** pill có `border-radius: 3px`, góc bo nhẹ chứ không phải viên tròn đầy

### Requirement: Filter tab active phải là filled pill, không phải underline

Filter tab active (`.quick-list__filter-tab--active`) SHALL hiển thị dạng filled pill: `background: var(--task-accent-tint)`, `color: var(--task-accent)`, `border-radius: 3px`, font `12px/w500`, padding `6px 9px`. KHÔNG dùng `border-bottom: 2px solid accent` (underline).

#### Scenario: Tab active hiển thị nền accent-tint

- **WHEN** user chọn filter tab "Tất cả"
- **THEN** tab active có nền `accent-tint`, chữ màu accent, bo góc 3px; các tab không active giữ style mặc định

### Requirement: Group label hiển thị count suffix và sticky

Group label (`.quick-list__group-label`) SHALL hiển thị dạng text với count suffix (`" · N"`, N = số task trong group) và có `position: sticky; top: 0; z-index: 1`. KHÔNG dùng hairline `::after` divider flex-fill.

#### Scenario: Group label dính đầu khi scroll

- **WHEN** user scroll danh sách task và group label chạm đầu list
- **THEN** group label dính (sticky) ở đầu, không cuộn đi theo list

#### Scenario: Group label hiển thị số task

- **WHEN** group "Cần chú ý" có 2 task
- **THEN** group label hiển thị `Cần chú ý · 2`

### Requirement: Task row có separator line, không có left-accent hover

Task row (`.quick-list__item-row`) SHALL có `border-bottom: 1px solid var(--task-border-soft)` (separator line giữa các row) và hover chỉ đổi `background`, KHÔNG thêm `border-left: 3px solid accent`.

#### Scenario: Row hiển thị đường phân cách

- **WHEN** Quick List render nhiều task row
- **THEN** giữa các row có hairline border-bottom separator

#### Scenario: Hover row chỉ đổi nền

- **WHEN** user hover một task row
- **THEN** row đổi background thành surface, không xuất hiện gạch accent bên trái

### Requirement: Search box có border visible và height 34px

Search box (`.quick-list__search`) SHALL có `height: 34px` và `border: 1px solid var(--task-border)` luôn visible (không phải `border: transparent` chỉ hiện khi focus).

#### Scenario: Search box hiển thị viền mặc định

- **WHEN** Quick List render chưa focus vào search
- **THEN** search box có viền `1px solid var(--task-border)` visible

### Requirement: Button radius 6px

`.task-btn` SHALL có `border-radius: 6px` (radius-md), không phải `3px` (radius-sm). `.task-detail__title-input` giữ `3px` (đã match design).

#### Scenario: Primary button bo góc 6px

- **WHEN** render `.task-btn--primary` (ví dụ nút "Tạo task")
- **THEN** button có `border-radius: 6px`

### Requirement: Checkbox dùng custom SVG box, done màu xanh lá

Checkbox (trong QuickList, TaskDetailPanel, TaskPostCard) SHALL render custom SVG: box `1.5px border`, `border-radius: 4px`. Khi done, box fill màu `--task-success` (xanh lá `#06ad6d`) + SVG check trắng, KHÔNG dùng FontAwesome `fa-square-o`/`fa-check-square` và KHÔNG màu accent xanh dương.

#### Scenario: Checkbox open state

- **WHEN** task chưa done
- **THEN** checkbox hiển thị box rỗng (`1.5px border`, `border-radius: 4px`), không có check mark

#### Scenario: Checkbox done state xanh lá

- **WHEN** task đã done
- **THEN** checkbox fill `--task-success` (xanh lá), có SVG check trắng bên trong

### Requirement: New Task header có nút close

Header của New Task view (`.task-detail__header` trong `NewTaskDialog`) SHALL có 3 element: back arrow, title "Tạo task", và nút close (X). Nút close gọi `onClose` (giống back).

#### Scenario: Render nút close trong New Task

- **WHEN** New Task view mở
- **THEN** header có nút close (X) ở bên phải, bên cạnh title

#### Scenario: Click close gọi onClose

- **WHEN** user click nút close (X)
- **THEN** `onClose` được gọi, đóng New Task view

### Requirement: Add subtask dùng inline-trigger expand pattern

Add subtask (trong TaskDetailPanel) SHALL dùng inline-trigger expand: default collapsed (hiển thị `+ Thêm subtask`), click → input hiện inline (label ẩn). Submit Enter commit subtask và giữ focus (thêm liên tục); Escape hoặc blur-empty → cancel. KHÔNG dùng input + button tách rời luôn mở.

#### Scenario: Default collapsed

- **WHEN** TaskDetailPanel render với chưa tương tác add subtask
- **THEN** hiển thị trigger `+ Thêm subtask`, input ẩn

#### Scenario: Click trigger mở input

- **WHEN** user click trigger `+ Thêm subtask`
- **THEN** label ẩn, input hiện và được focus

#### Scenario: Enter commit và giữ focus

- **WHEN** user nhập text vào input rồi Enter
- **THEN** subtask được tạo, input clear và giữ focus để thêm tiếp

#### Scenario: Escape cancel

- **WHEN** user nhấn Escape khi đang trong input
- **THEN** input ẩn, trở về collapsed trigger, text đã nhập bị hủy

### Requirement: Detail scroll container có top padding

`.task-detail__scroll` (container của cả Task Detail và New Task view) SHALL có top padding `16px` để title không dính sát header, khớp thiết kế (`.det-scroll { padding: 16px }`).

#### Scenario: Title có khoảng cách phía trên

- **WHEN** render Task Detail hoặc New Task view
- **THEN** title row cách header trên cùng 16px, không bị dính sát
