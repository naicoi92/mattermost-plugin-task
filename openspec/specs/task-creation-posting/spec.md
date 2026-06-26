# Spec: Task Creation & Channel Posting

## Purpose

Đặc tả hành vi post task card khi tạo task. Đảm bảo New Task **luôn deterministic** post card vào originating channel (kể cả DM), trong khi vẫn giữ scope personal cho DM task và không trùng lặp với assignee-notification.

## Requirements

### Requirement: New Task always posts a card to its ChannelID

Khi tạo task, hệ thống SHALL post đúng **một** task card vào `task.ChannelID` (surface duy nhất). Hành vi này deterministic cho mọi trường hợp (team channel, DM, self-DM) vì `ChannelID` luôn có giá trị không rỗng. Hệ thống SHALL KHÔNG post thêm card vào surface nào khác (không còn `postCardDM` bot↔assignee card, không còn `DMPostID`).

#### Scenario: Tạo task trong channel thường thì post card vào channel đó

- **WHEN** user tạo task khi đang ở trong một channel loại `O/P/G` (channel_id có giá trị)
- **THEN** hệ thống post đúng một task card vào channel đó và ghi linkage `task_posts` (kind = `channel`)

#### Scenario: Tạo task trong DM thì post card vào DM đó với ChannelID = DM id

- **WHEN** user tạo task khi đang ở trong một DM (channel type `D`)
- **THEN** hệ thống post đúng một task card vào DM đó và ghi linkage `task_posts`
- **AND** `task.channel_id` = id của DM đó (KHÔNG còn rỗng)

#### Scenario: Tạo task trong self-DM thì post card vào self-DM đó

- **WHEN** user tạo task trong self-DM (`<uid>__<uid>`)
- **THEN** hệ thống post đúng một task card vào self-DM đó
- **AND** `task.channel_id` = id của self-DM đó

#### Scenario: Tạo task thiếu channel_id bị reject thay vì post card

- **WHEN** client gọi `POST /tasks` với `channel_id` rỗng hoặc thiếu
- **THEN** hệ thống trả HTTP 400, không post card nào

