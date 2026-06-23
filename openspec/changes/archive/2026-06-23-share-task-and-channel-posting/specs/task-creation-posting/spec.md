## ADDED Requirements

### Requirement: New Task always posts a card to the originating channel

Khi tạo task, hệ thống SHALL post task card vào originating channel (channel mà user đang ở khi tạo), kể cả khi originating channel là DM. Hành vi này phải **deterministic**: không còn trường hợp tạo task trong DM mà không có card nào được post.

#### Scenario: Tạo task trong channel thường (O/P/G) thì post card vào channel đó

- **WHEN** user tạo task khi đang ở trong một channel loại `O/P/G` (channel_id có giá trị)
- **THEN** hệ thống post đúng một task card vào channel đó và ghi linkage `task_posts` (kind = `channel`)

#### Scenario: Tạo task trong DM thì vẫn post card vào DM đó

- **WHEN** user tạo task khi đang ở trong một DM (channel type `D`)
- **THEN** hệ thống post đúng một task card vào DM đó và ghi linkage `task_posts`
- **AND** task vẫn có `channel_id` rỗng (giữ scope personal)

#### Scenario: Tạo personal task không có channel context thì không post

- **WHEN** user tạo task mà không có originating channel (không context)
- **THEN** hệ thống KHÔNG post card nào (giữ hành vi hiện tại cho personal task thuần)

### Requirement: DM task giữ scope personal

Task tạo trong DM MUST giữ scope là personal (member-gated: chỉ creator + assignee là member). Việc thêm card thông báo vào DM KHÔNG thay đổi scope hay khả năng hiển thị của task trong listing.

#### Scenario: DM task không đổi channel_id

- **WHEN** user tạo task trong DM
- **THEN** `task.channel_id` bằng rỗng (personal scope) bất kể card có được post vào DM

#### Scenario: Listing scope=direct không bị ảnh hưởng

- **WHEN** truy vấn `scope=direct&partner_id=<DM partner>`
- **THEN** DM task (personal) vẫn xuất hiện theo logic `scope=direct` hiện tại, không thay đổi

### Requirement: Announce-posting là cộng thêm, không thay thế assignee-notification

Announce card (mới) và assignee-notification DM card (đã có) đi vào **hai surface khác nhau** nên KHÔNG trùng lặp: announce card đi vào originating channel (vd DM giữa user và partner), còn assignee-notification đi vào DM giữa bot và assignee. Việc thêm announce-posting MUST KHÔNG thay đổi logic assignee-notification hiện có (surgical, nhất quán với channel task vốn đã post cả channel card lẫn assignee bot-DM).

#### Scenario: DM task có assignee = partner thì partner thấy 2 card ở 2 surface khác nhau

- **WHEN** user tạo task trong DM với partner P, và assignee = P
- **THEN** hệ thống post một announce card vào DM(user, P) (originating channel)
- **AND** hệ thống vẫn post assignee-notification DM card vào DM(bot, P) theo logic hiện có (không bị dedup hay skip)
- **AND** đây là hành vi nhất quán với channel task (channel card + assignee bot-DM)
