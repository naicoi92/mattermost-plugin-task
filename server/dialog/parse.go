package dialog

import (
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// ParseCreateSubmission extracts a CreateSubmission from a dialog's submission
// map (the JSON body of a dialog submit callback). Summary is required and
// trimmed; an empty summary yields ErrSummaryRequired. Due, if present, must be
// a valid integer (ms). Scope defaults to channel when the originating channel
// is known, else personal.
func ParseCreateSubmission(sub map[string]any) (CreateSubmission, error) {
	out := CreateSubmission{
		Summary:     strVal(sub, FieldSummary),
		Description: strVal(sub, FieldDescription),
	}
	out.Summary = strings.TrimSpace(out.Summary)
	if out.Summary == "" {
		return out, ErrSummaryRequired
	}

	if a := strings.TrimSpace(strVal(sub, FieldAssignee)); a != "" {
		out.AssigneeID = &a
	}

	if d := strings.TrimSpace(strVal(sub, FieldDue)); d != "" {
		ms, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			return out, ErrInvalidDue
		}
		out.Due = &ms
	}

	if s := strVal(sub, FieldScope); s == ScopePersonal {
		out.Personal = true
	}
	return out, nil
}

func strVal(sub map[string]any, key string) string {
	v, ok := sub[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	default:
		// JSON numbers/bools may arrive as float64/bool; coerce to string.
		return strings.TrimSpace(formatAny(x))
	}
}

func formatAny(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// Sentinel errors for dialog submission validation.
var (
	ErrSummaryRequired = errors.New("summary is required")
	ErrInvalidDue      = errors.New("due must be a numeric millisecond timestamp")
)
