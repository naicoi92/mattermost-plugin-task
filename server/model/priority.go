package model

// Task priority constants. These mirror the Mattermost message-priority enum
// (developers.mattermost.com/integrate/reference/message-priority): standard is
// the implicit default, important and urgent are the two explicit elevated
// levels. IsValidPriority rejects anything else so a typo can't persist an
// unknown priority the UI later can't render.
const (
	PriorityStandard  = "standard"
	PriorityImportant = "important"
	PriorityUrgent    = "urgent"
)

// IsValidPriority reports whether s is one of the recognized Priority* constants.
// The store and service layers reject anything else so the namespace stays
// controlled and the UI can render every priority.
func IsValidPriority(s string) bool {
	switch s {
	case PriorityStandard, PriorityImportant, PriorityUrgent:
		return true
	default:
		return false
	}
}
