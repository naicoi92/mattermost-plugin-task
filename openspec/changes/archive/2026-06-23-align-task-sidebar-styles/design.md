## Context

Webapp plugin (`webapp/src/styles/index.scss`, ~1900 dòng SCSS) hiện đã có token system (`--task-*`), scoped namespace (`.task-rhs`, `.quick-list`, `.task-detail`), và dark/light theme. Nhưng nhiều giá trị CSS lệch thiết kế tham chiếu `mattermost-task-sidebar-3-2.html` (open-design project `c80d39a2`): token amber sai, pill/tab/row/checkbox có shape khác, và 2 layout (New Task header, add subtask) lệch cấu trúc DOM.

Checkbox hiện dùng FontAwesome `fa-square-o`/`fa-check-square` (3 component: QuickList, TaskDetailPanel, TaskPostCard). Add subtask dùng input + button tách rời, không phải inline-trigger expand như design.

## Goals / Non-Goals

**Goals:**

- Đạt visual parity với thiết kế tham chiếu cho 9 điểm (A token + B1–B6 SCSS + C checkbox + E close + F subtask).
- Giữ nguyên architecture hiện tại: SCSS + CSS vars token, scoped namespace, dark/light theme.
- Giữ enum priority `standard/important/urgent` (không rename).

**Non-Goals:**

- Không migrate sang TailwindCSS (giữ SCSS).
- Không đổi dark navy header (platform constraint: Mattermost native RHS header).
- Không xử lý comment/activity feed (tách proposal riêng).
- Không rename priority enum.

## Decisions

### Decision 1: Giữ SCSS, không dùng TailwindCSS

**Lý do:** SCSS hiện tại đã scoped (`.task-rhs` prefix → không collide host CSS), đã có token system với CSS vars (dark/light theme hoạt động), đã chạy ổn. 9 gap đều là giá trị CSS (radius, color, border) — fix thẳng trong SCSS. Tailwind đòi hỏi setup postcss-loader + webpack config + purge + giải quyết theme token mapping + naming collision → overhead lớn, không xứng đáng cho việc fix style.

**Alternatives considered:**

- Thêm Tailwind qua postcss-loader vào webpack chain: rejected — phức tạp, rủi ro collision với host CSS, mất dynamic theme nếu không cẩn thận.
- Tailwind CLI pre-build: rejected — 2 bước build, phức tạp hơn.

### Decision 2: Checkbox — custom React SVG component, không dùng FontAwesome

**Lý do:** Design dùng rounded-rect box (`1.5px border`, `radius 4px`) + SVG check, done = xanh lá (`--task-success`). FontAwesome `fa-square-o` không khớp shape (góc nhọn) và done color (accent xanh dương). Tạo 1 `<TaskCheck>` component SVG tái dùng cho cả 3 chỗ.

**Alternatives considered:**

- CSS override FontAwesome: rejected — không kiểm soát được shape viền + check glyph riêng.
- Giữ FontAwesome: rejected — không đạt parity shape + color.

### Decision 3: Add subtask — inline-trigger expand pattern

**Lý do:** Design dùng collapsed trigger (`+ Thêm subtask`) → click → input hiện inline (label ẩn). Sạch hơn input + button luôn mở. Pattern quen thuộc (Linear/Notion). Logic: click trigger → focus input; Enter → commit + giữ focus (thêm liên tục); Escape/blur-empty → cancel.

**Alternatives considered:**

- Giữ input + button: rejected — clutter, không match design.

### Decision 4: Token `--task-warning` fix là root cause cho amber

**Lý do:** Toàn bộ chỗ amber (due "soon", priority important) dùng chung `--task-warning`. Sửa 1 dòng token (`#ffb700` → `#cf8900`) fix hết, không cần đụng từng selector. Áp dụng cả light + dark theme block.

## Risks / Trade-offs

- **[Token warning ảnh hưởng rộng]** → Mitigation: 1 dòng, đã scoped trong `.task-rhs`, test visual sau khi đổi.
- **[Checkbox rewrite cần update snapshot test]** → Mitigation: update snapshot cho 3 component (QuickList, TaskDetailPanel, TaskPostCard).
- **[Add subtask đổi interaction pattern]** → Mitigation: thêm logic test cho collapsed/expanded/Enter/Escape; giữ `addSubtask` API call không đổi.
- **[New Task close button trùng function back]** → Trade-off chấp nhận: match design 100%, cả 2 đều gọi `onClose`.

## Migration Plan

Không cần migration dữ liệu — thuần frontend CSS + component. Deploy:

1. Sửa token + SCSS block trong `index.scss`.
2. Refactor 3 component checkbox + add-subtask + close button.
3. Update snapshot tests.
4. `cd webapp && npm test` + `npm run check-types` + build.
5. Deploy qua `make deploy`.

Rollback: revert commit, không có schema/data change.
