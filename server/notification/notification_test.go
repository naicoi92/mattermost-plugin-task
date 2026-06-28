package notification

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAPI records the DMs sent so tests can assert recipient + message.
type fakeAPI struct {
	users  map[string]*model.User
	posts  []recordedPost
	dmFail map[string]bool // recipientID -> force DM channel failure
	logged []string
}

type recordedPost struct {
	channelID string
	userID    string
	message   string
}

func (f *fakeAPI) GetUser(userID string) (*model.User, error) {
	return f.users[userID], nil
}

func (f *fakeAPI) GetDirectChannel(userID1, userID2 string) (*model.Channel, error) {
	if f.dmFail[userID1] {
		return nil, errFake("dm failed")
	}
	// Stable synthetic channel id combining both users.
	return &model.Channel{Id: "dm:" + userID1 + ":" + userID2}, nil
}

func (f *fakeAPI) CreatePost(post *model.Post) (*model.Post, error) {
	f.posts = append(f.posts, recordedPost{
		channelID: post.ChannelId,
		userID:    post.UserId,
		message:   post.Message,
	})
	return post, nil
}

func (f *fakeAPI) LogError(message string, keyValuePairs ...any) {
	f.logged = append(f.logged, message)
}

type errFake string

func (e errFake) Error() string { return string(e) }

// fakeTranslator returns a deterministic, locale-tagged string per key so tests
// can assert which key was used and the arg order, independent of the real
// i18n wording. Non-string args (e.g. an int day count) render as "".
type fakeTranslator struct{}

func (fakeTranslator) T(locale, key string, args ...any) string {
	parts := make([]string, 0, 2+len(args))
	parts = append(parts, locale, key)
	for _, a := range args {
		parts = append(parts, asString(a))
	}
	return strings.Join(parts, ":")
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// newTestNotifier builds a notifier with NO site URL (task name renders as
// plain text — the graceful fallback path).
func newTestNotifier(api API) *Notifier {
	return New(api, fakeTranslator{}, "bot", "", "")
}

// newTestNotifierWithSite builds a notifier whose task names render as
// clickable markdown links to <siteURL>/plug/<pluginID>/task/<id>.
func newTestNotifierWithSite(api API, siteURL string) *Notifier {
	return New(api, fakeTranslator{}, "bot", siteURL, "test-plugin")
}

func userWithLocale(id, locale string) *model.User {
	return &model.User{Id: id, Username: "user_" + id, Locale: locale}
}

// userNamed builds a user whose GetDisplayName(ShowNicknameFullName) resolves to
// the given display name (Nickname first, then FirstName + " " + LastName).
func userNamed(id, nickname string) *model.User {
	return &model.User{Id: id, Username: "user_" + id, Nickname: nickname}
}

// newTestNotifierAtTime returns a notifier whose clock is pinned to nowMs so
// the due-band emoji prefix (⚠/🔴) is deterministic regardless of wall time.
func newTestNotifierAtTime(api API, nowMs int64) *Notifier {
	n := newTestNotifier(api)
	n.nowMs = func() int64 { return nowMs }
	return n
}

func TestNotifyAssigned_EmojiPrefixByBand(t *testing.T) {
	now := int64(1_700_000_000_000)
	const hour = int64(60 * 60 * 1000)

	cases := []struct {
		name   string
		dueAt  *int64
		status string
		want   string // expected emoji prefix at start of message
	}{
		{"warning band (48h) → ⚠", ptrInt64(now + (48 * hour)), "todo", "⚠ "},
		{"danger band (12h) → 🔴", ptrInt64(now + (12 * hour)), "todo", "🔴 "},
		{"overdue → 🔴 (danger)", ptrInt64(now - (5 * hour)), "todo", "🔴 "},
		{"muted band (>72h) → no prefix", ptrInt64(now + (73 * hour)), "todo", ""},
		{"no due → no prefix", nil, "todo", ""},
		{"terminal done → no prefix even if overdue", ptrInt64(now - hour), "done", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeAPI{users: map[string]*model.User{
				"a": userWithLocale("a", "vi"),
				"c": userNamed("c", "Alice"),
			}}
			n := newTestNotifierAtTime(api, now)
			n.NotifyAssigned("a", "c", TaskSummary{
				ID: "01HXYZTASK0001", Summary: "x", Status: c.status, DueAt: c.dueAt,
			})
			require.Len(t, api.posts, 1)
			assert.True(t, strings.HasPrefix(api.posts[0].message, c.want),
				"message %q should start with %q", api.posts[0].message, c.want)
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestNotifyAssigned_DMsAssigneeNotCreator(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"assignee": userWithLocale("assignee", "vi"),
			"creator":  userNamed("creator", "Alice"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyAssigned("assignee", "creator", TaskSummary{
		ID: "01HXYZASSIGN001", Summary: "Fix bug", Status: "todo",
	})

	require.Len(t, api.posts, 1)
	assert.Equal(t, "bot", api.posts[0].userID)
	msg := api.posts[0].message
	// Localized to the assignee's locale (vi), core key used.
	assert.Contains(t, msg, "vi:notification.assigned:")
	// Actor shown by display name (nickname "Alice"), NOT @-mentioned.
	assert.Contains(t, msg, "Alice")
	assert.NotContains(t, msg, "@user_creator")
	// Short id + status label present.
	assert.Contains(t, msg, "01HXYZAS")
	assert.Contains(t, msg, "vi:task.status.todo")
}

func TestNotifyAssigned_ClickableTaskNameWithSiteURL(t *testing.T) {
	api := &fakeAPI{users: map[string]*model.User{
		"a": userWithLocale("a", "vi"), "c": userNamed("c", "Alice"),
	}}
	n := newTestNotifierWithSite(api, "https://team.example.com")

	n.NotifyAssigned("a", "c", TaskSummary{ID: "01HXYZTASK0001", Summary: "Fix bug", Status: "todo"})

	require.Len(t, api.posts, 1)
	assert.Contains(t, api.posts[0].message,
		"[Fix bug](https://team.example.com/plug/test-plugin/task/01HXYZTASK0001)")
}

func TestNotifyAssigned_PlainNameWhenNoSiteURL(t *testing.T) {
	api := &fakeAPI{users: map[string]*model.User{"a": userWithLocale("a", "vi")}}
	n := newTestNotifier(api) // no site URL

	n.NotifyAssigned("a", "x", TaskSummary{ID: "01HXYZTASK0001", Summary: "Fix [bug]", Status: "todo"})

	require.Len(t, api.posts, 1)
	// No markdown link; plain-text fallback escapes brackets so a title like
	// "Fix [bug]" can't become a spurious link label in the DM body.
	assert.Contains(t, api.posts[0].message, `Fix \[bug\]`)
	assert.NotContains(t, api.posts[0].message, "/plug/")
}

func TestNotifyAssigned_IncludesSelfAssign(t *testing.T) {
	api := &fakeAPI{users: map[string]*model.User{"u1": userWithLocale("u1", "en")}}
	n := newTestNotifier(api)

	n.NotifyAssigned("u1", "u1", TaskSummary{ID: "01HXYZSELF00001", Summary: "x"})
	require.Len(t, api.posts, 1)
	assert.Contains(t, api.posts[0].message, "en:notification.assigned:")
}

func TestNotifyAssigned_EmptyAssigneeNoop(t *testing.T) {
	api := &fakeAPI{}
	n := newTestNotifier(api)
	n.NotifyAssigned("", "creator", TaskSummary{})
	// No DM must be sent for an empty assignee.
	assert.Empty(t, api.posts)
}

func TestNotifyAssigned_OmitsDueClauseWhenNoDue(t *testing.T) {
	api := &fakeAPI{users: map[string]*model.User{"a": userWithLocale("a", "vi")}}
	n := newTestNotifier(api)

	n.NotifyAssigned("a", "c", TaskSummary{ID: "01HXYZTASK0001", Summary: "x", Status: "todo"})
	require.Len(t, api.posts, 1)
	assert.NotContains(t, api.posts[0].message, "notification.due.suffix")
}

func TestNotifyAssigned_AppendsDueClauseWhenDueSet(t *testing.T) {
	due := int64(1_700_000_000_000)
	api := &fakeAPI{users: map[string]*model.User{"a": userWithLocale("a", "vi")}}
	n := newTestNotifier(api)

	n.NotifyAssigned("a", "c", TaskSummary{ID: "01HXYZTASK0001", Summary: "x", Status: "todo", DueAt: &due, IsAllDay: true})
	require.Len(t, api.posts, 1)
	assert.Contains(t, api.posts[0].message, "vi:notification.due.suffix:")
}

func TestNotifyCompleted_DMSCreatorAndAssigneeExcludesActor(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"actor":    userNamed("actor", "Alice"),
			"creator":  userWithLocale("creator", "en"),
			"assignee": userWithLocale("assignee", "vi"),
		},
	}
	n := newTestNotifier(api)

	// actor == assignee: creator gets a DM, assignee (actor) is excluded.
	n.NotifyCompleted(TaskSummary{ID: "01HXYZTASK0001", Summary: "Ship it"}, "actor", "creator", "actor")

	require.Len(t, api.posts, 1)
	assert.Contains(t, api.posts[0].channelID, "creator")
	msg := api.posts[0].message
	assert.Contains(t, msg, "en:notification.completed:")
	assert.Contains(t, msg, "Alice") // actor display name, no @
	assert.NotContains(t, msg, "@user_actor")
	assert.NotContains(t, msg, "task.status") // completed omits status
}

func TestNotifyCompleted_DistinctRecipients(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"actor":    userWithLocale("actor", "en"),
			"creator":  userWithLocale("creator", "en"),
			"assignee": userWithLocale("assignee", "en"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyCompleted(TaskSummary{ID: "01HXYZTASK0001", Summary: "T"}, "actor", "creator", "assignee")
	assert.Len(t, api.posts, 2)
}

func TestNotifyCancelled_DMSParticipants(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"actor":    userNamed("actor", "Bob"),
			"creator":  userWithLocale("creator", "en"),
			"assignee": userWithLocale("assignee", "en"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyCancelled(TaskSummary{ID: "01HXYZTASK0001", Summary: "Drop it"}, "actor", "creator", "assignee")
	assert.Len(t, api.posts, 2)
	for _, p := range api.posts {
		assert.Contains(t, p.message, "notification.cancelled")
		assert.NotContains(t, p.message, "task.status")
		assert.Contains(t, p.message, "Bob")
	}
}

func TestNotifyCommented_DMSParticipantsExcludesCommenter(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"actor":    userNamed("actor", "Alice"),
			"creator":  userWithLocale("creator", "en"),
			"assignee": userWithLocale("assignee", "en"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyCommented(TaskSummary{
		ID: "01HXYZTASK0001", Summary: "Discuss", Status: "todo",
		CommentPreview: "looks good to me",
	}, "actor", "creator", "assignee")
	assert.Len(t, api.posts, 2)
	for _, p := range api.posts {
		assert.Contains(t, p.message, "notification.commented")
		assert.NotContains(t, p.channelID, "actor")       // commenter excluded
		assert.Contains(t, p.message, "looks good to me") // preview present
	}
}

func TestNotifyCommented_OmitsPreviewWhenEmpty(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"actor":   userNamed("actor", "Alice"),
			"creator": userWithLocale("creator", "en"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyCommented(TaskSummary{ID: "01HXYZTASK0001", Summary: "Discuss"}, "actor", "creator", "")
	require.Len(t, api.posts, 1)
	assert.NotContains(t, api.posts[0].message, "notification.preview.suffix")
}

func TestNotifyReminder_DMAssignee(t *testing.T) {
	api := &fakeAPI{users: map[string]*model.User{"assignee": userWithLocale("assignee", "vi")}}
	n := newTestNotifier(api)

	require.NoError(t, n.NotifyReminder("assignee", TaskSummary{ID: "01HXYZTASK0001", Summary: "Due soon", Status: "todo"}))
	require.Len(t, api.posts, 1)
	msg := api.posts[0].message
	assert.Contains(t, msg, "vi:notification.reminder:")
	assert.NotContains(t, msg, "@") // no actor in reminder
}

func TestNotifyReminder_EmptyAssigneeNoop(t *testing.T) {
	api := &fakeAPI{}
	n := newTestNotifier(api)
	require.NoError(t, n.NotifyReminder("", TaskSummary{}))
	assert.Empty(t, api.posts)
}

func TestNotifyReminder_DeliveryFailureReturnsError(t *testing.T) {
	api := &fakeAPI{
		users:  map[string]*model.User{"u1": userWithLocale("u1", "en")},
		dmFail: map[string]bool{"u1": true},
	}
	n := newTestNotifier(api)

	err := n.NotifyReminder("u1", TaskSummary{Summary: "x"})
	require.Error(t, err)
	assert.Empty(t, api.posts)
}

func TestNotifyDueSoon_DMsOnlyAssignee(t *testing.T) {
	now := int64(1_700_000_000_000)
	const hour = int64(60 * 60 * 1000)
	due := now + (12 * hour) // within 24h → due-soon
	api := &fakeAPI{users: map[string]*model.User{
		"creator":  userWithLocale("creator", "vi"),
		"assignee": userWithLocale("assignee", "vi"),
	}}
	n := newTestNotifierAtTime(api, now)

	n.NotifyDueSoon("assignee", TaskSummary{
		ID: "01HXYZTASK0001", Summary: "Slipped", Status: "todo", DueAt: &due,
	})

	// Only assignee notified (not creator).
	require.Len(t, api.posts, 1)
	assert.Contains(t, api.posts[0].channelID, "assignee")
	msg := api.posts[0].message
	// Danger band (<24h) → 🔴 prefix.
	assert.True(t, strings.HasPrefix(msg, "🔴 "))
	assert.Contains(t, msg, "vi:notification.due_soon:")
	assert.Contains(t, msg, "vi:task.status.todo")
}

func TestNotifyDueSoon_EmptyAssigneeNoop(t *testing.T) {
	n := newTestNotifier(&fakeAPI{})
	n.NotifyDueSoon("", TaskSummary{ID: "x", Summary: "s"})
}

func TestNotifyOverdue_DMsCreatorAndAssigneeNoActor(t *testing.T) {
	due := int64(1_700_000_000_000)
	now := due + 3*24*60*60*1000 // 3 days later
	api := &fakeAPI{
		users: map[string]*model.User{
			"creator":  userWithLocale("creator", "vi"),
			"assignee": userWithLocale("assignee", "vi"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyOverdue(TaskSummary{
		ID: "01HXYZTASK0001", Summary: "Slipped", Status: "in_progress", DueAt: &due,
	}, now, "creator", "assignee")

	// Both participants notified; no actor exclusion.
	assert.Len(t, api.posts, 2)
	for _, p := range api.posts {
		assert.Contains(t, p.message, "vi:notification.overdue:")
		assert.Contains(t, p.message, "vi:task.status.in_progress")
		// Duration key invoked (number not rendered by fakeTranslator).
		assert.Contains(t, p.message, "vi:notification.overdue.duration")
	}
}

func TestNotifyOverdue_BestEffortDoesNotPanic(t *testing.T) {
	// Delivery failure is logged, not returned — overdue retries next day.
	api := &fakeAPI{
		users:  map[string]*model.User{"u1": userWithLocale("u1", "en")},
		dmFail: map[string]bool{"u1": true},
	}
	n := newTestNotifier(api)

	due := int64(1_700_000_000_000)
	assert.NotPanics(t, func() {
		n.NotifyOverdue(TaskSummary{ID: "x", Summary: "s", DueAt: &due}, due+1000, "u1", "")
	})
}

func TestLocaleFor_DefaultsWhenUserMissing(t *testing.T) {
	n := newTestNotifier(&fakeAPI{users: map[string]*model.User{}})
	assert.Equal(t, defaultLocale, n.localeFor("ghost"))
}

func TestUniqueRecipients(t *testing.T) {
	got := uniqueRecipients("actor", "creator", "assignee", "creator", "", "actor")
	assert.Equal(t, []string{"creator", "assignee"}, got)
}

func TestDisplayName_NoAtPrefix(t *testing.T) {
	n := newTestNotifier(&fakeAPI{users: map[string]*model.User{
		"u1":    {Id: "u1", Username: "alice", Nickname: "Alice"},
		"plain": {Id: "plain", Username: "bob"},
	}})
	assert.Equal(t, "Alice", n.displayName("u1"))  // nickname wins
	assert.Equal(t, "bob", n.displayName("plain")) // username fallback, no @
	assert.Equal(t, "unknown", n.displayName("unknown"))
}

// --- helper unit tests ---

func TestShortID(t *testing.T) {
	assert.Equal(t, "01HXYZ01", shortID("01HXYZ0123456789ABCDEF"))
	assert.Equal(t, "short", shortID("short")) // < 8 chars → whole id
	assert.Equal(t, "", shortID(""))
}

func TestOverdueDays(t *testing.T) {
	const day = 24 * 60 * 60 * 1000
	assert.Equal(t, 1, overdueDays(0, 0))            // same instant → min 1
	assert.Equal(t, 1, overdueDays(5*60*60*1000, 0)) // 5h → floored to 1
	assert.Equal(t, 3, overdueDays(3*day, 0))        // exactly 3 days
	assert.Equal(t, 3, overdueDays(3*day+1000, 0))   // 3 days + 1s → still 3
	assert.Equal(t, 3, overdueDays(4*day-1, 0))      // just under 4 days → floored to 3
}

func TestTruncateForPreview(t *testing.T) {
	// Empty / whitespace → "".
	assert.Equal(t, "", TruncateForPreview("", 100))
	assert.Equal(t, "", TruncateForPreview("   \n  ", 100))
	// Short content unchanged.
	assert.Equal(t, "hello", TruncateForPreview("hello", 100))
	// Markdown stripped: link label kept, emphasis markers dropped.
	assert.Equal(t, "see here bold", TruncateForPreview("see [here](http://x) *bold*", 100))
	// Truncation appends … at the rune boundary.
	long := strings.Repeat("あ", 120) // multibyte
	got := TruncateForPreview(long, 10)
	assert.Equal(t, strings.Repeat("あ", 10)+"…", got)
}

func TestEscapeMarkdown(t *testing.T) {
	assert.Equal(t, `a\[b\] \(c\) d`, escapeMarkdown("a[b] (c) d"))
}

func TestFormatDue_LocaleAndAllDay(t *testing.T) {
	// 2023-11-14 09:00:00 UTC
	ms := int64(1_699_952_400_000)
	assert.Equal(t, "14/11/2023", formatDue("vi", ms, true))
	assert.Equal(t, "2023-11-14", formatDue("en", ms, true))
	assert.Equal(t, "14/11/2023 09:00", formatDue("vi", ms, false))
	assert.Equal(t, "2023-11-14 09:00", formatDue("en", ms, false))
}
