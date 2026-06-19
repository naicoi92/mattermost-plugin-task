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

// fakeTranslator returns a deterministic, locale-tagged string per key.
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

func newTestNotifier(api API) *Notifier {
	return New(api, fakeTranslator{}, "bot")
}

func userWithLocale(id, locale string) *model.User {
	return &model.User{Id: id, Username: "user_" + id, Locale: locale}
}

func messagesTo(s string, posts []recordedPost) []string {
	var out []string
	for _, p := range posts {
		if p.message == s {
			out = append(out, p.channelID)
		}
	}
	return out
}

func TestNotifyAssigned_DMsAssigneeNotCreator(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"assignee": userWithLocale("assignee", "vi"),
			"creator":  userWithLocale("creator", "en"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyAssigned("assignee", "creator", TaskSummary{Summary: "Fix bug"})

	require.Len(t, api.posts, 1)
	assert.Equal(t, "bot", api.posts[0].userID)
	// Translated to the assignee's locale (vi), with summary + actor name.
	assert.Equal(t, "vi:notification.assigned:Fix bug:@user_creator", api.posts[0].message)
}

func TestNotifyAssigned_SkipsSelfAssign(t *testing.T) {
	api := &fakeAPI{users: map[string]*model.User{"u1": userWithLocale("u1", "en")}}
	n := newTestNotifier(api)

	// assignee == creator -> no DM.
	n.NotifyAssigned("u1", "u1", TaskSummary{Summary: "x"})
	assert.Empty(t, api.posts)
}

func TestNotifyAssigned_EmptyAssigneeNoop(t *testing.T) {
	n := newTestNotifier(&fakeAPI{})
	n.NotifyAssigned("", "creator", TaskSummary{})
	// No panic, no posts.
}

func TestNotifyCompleted_DMSCreatorAndAssigneeExcludesActor(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"actor":    userWithLocale("actor", "en"),
			"creator":  userWithLocale("creator", "en"),
			"assignee": userWithLocale("assignee", "vi"),
		},
	}
	n := newTestNotifier(api)

	// actor == assignee: creator gets a DM, assignee (actor) is excluded.
	n.NotifyCompleted(TaskSummary{Summary: "Ship it"}, "actor", "creator", "actor")

	// Exactly one DM to creator (not the actor).
	channels := messagesTo("en:notification.completed:Ship it:@user_actor", api.posts)
	require.Len(t, channels, 1)
	assert.Contains(t, channels[0], "creator")
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

	// All distinct: creator + assignee (actor excluded). 2 DMs.
	n.NotifyCompleted(TaskSummary{Summary: "T"}, "actor", "creator", "assignee")
	assert.Len(t, api.posts, 2)
}

func TestNotifyCancelled_DMSParticipants(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"actor":    userWithLocale("actor", "en"),
			"creator":  userWithLocale("creator", "en"),
			"assignee": userWithLocale("assignee", "en"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyCancelled(TaskSummary{Summary: "Drop it"}, "actor", "creator", "assignee")
	assert.Len(t, api.posts, 2)
	for _, p := range api.posts {
		assert.Contains(t, p.message, "notification.cancelled")
	}
}

func TestNotifyCommented_DMSParticipantsExcludesCommenter(t *testing.T) {
	api := &fakeAPI{
		users: map[string]*model.User{
			"actor":    userWithLocale("actor", "en"),
			"creator":  userWithLocale("creator", "en"),
			"assignee": userWithLocale("assignee", "en"),
		},
	}
	n := newTestNotifier(api)

	n.NotifyCommented(TaskSummary{Summary: "Discuss"}, "actor", "creator", "assignee")
	// creator + assignee DM'd; commenter excluded.
	assert.Len(t, api.posts, 2)
	for _, p := range api.posts {
		assert.Contains(t, p.message, "notification.commented")
		assert.NotContains(t, p.channelID, "actor")
	}
}

func TestNotifyReminder_DMAssignee(t *testing.T) {
	api := &fakeAPI{users: map[string]*model.User{"assignee": userWithLocale("assignee", "vi")}}
	n := newTestNotifier(api)

	require.NoError(t, n.NotifyReminder("assignee", TaskSummary{Summary: "Due soon"}))
	require.Len(t, api.posts, 1)
	assert.Equal(t, "vi:notification.reminder:Due soon", api.posts[0].message)
}

func TestNotifyReminder_EmptyAssigneeNoop(t *testing.T) {
	n := newTestNotifier(&fakeAPI{})
	require.NoError(t, n.NotifyReminder("", TaskSummary{}))
}

func TestLocaleFor_DefaultsWhenUserMissing(t *testing.T) {
	n := newTestNotifier(&fakeAPI{users: map[string]*model.User{}})
	assert.Equal(t, defaultLocale, n.localeFor("ghost"))
}

func TestNotifyReminder_DeliveryFailureReturnsError(t *testing.T) {
	// Unlike the synchronous event DMs (assign/done/comment), the reminder DM
	// reports its delivery error so the scheduler can retry instead of marking
	// the reminder fired and losing it.
	api := &fakeAPI{
		users:  map[string]*model.User{"u1": userWithLocale("u1", "en")},
		dmFail: map[string]bool{"u1": true},
	}
	n := newTestNotifier(api)

	err := n.NotifyReminder("u1", TaskSummary{Summary: "x"})
	require.Error(t, err, "delivery failure must be reported")
	assert.Empty(t, api.posts)
}

func TestUniqueRecipients(t *testing.T) {
	got := uniqueRecipients("actor", "creator", "assignee", "creator", "", "actor")
	assert.Equal(t, []string{"creator", "assignee"}, got)
}

func TestDisplayName(t *testing.T) {
	n := newTestNotifier(&fakeAPI{users: map[string]*model.User{
		"u1": {Id: "u1", Username: "alice"},
	}})
	assert.Equal(t, "@alice", n.displayName("u1"))
	assert.Equal(t, "unknown", n.displayName("unknown"))
}
