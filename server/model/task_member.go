package model

// TaskMember roles. The MVP uses only Creator and Assignee (one each per task,
// enforced at the application layer); Follower is reserved for a future
// multi-participant feature and is included so the schema does not need to
// change later.
const (
	MemberRoleCreator  = "creator"
	MemberRoleAssignee = "assignee"
	MemberRoleFollower = "follower"
)

// TaskMember is one edge in the task_members table: a user related to a task
// by a role. The (TaskID, UserID, Role) triple is the primary key, so the
// same user can hold different roles on the same task and re-adding an
// existing edge is idempotent.
type TaskMember struct {
	// TaskID is the task this membership refers to.
	TaskID string `json:"task_id" db:"task_id"`
	// UserID is the Mattermost user id of the member.
	UserID string `json:"user_id" db:"user_id"`
	// Role is one of the MemberRole* constants.
	Role string `json:"role" db:"role"`
	// CreatedAt is the ms-UTC timestamp the membership was recorded.
	CreatedAt int64 `json:"created_at" db:"created_at"`
}

// IsValidMemberRole reports whether role is one of the recognized MemberRole*
// constants. The store layer rejects anything else so a typo can't silently
// create an unknown role.
func IsValidMemberRole(role string) bool {
	switch role {
	case MemberRoleCreator, MemberRoleAssignee, MemberRoleFollower:
		return true
	default:
		return false
	}
}
