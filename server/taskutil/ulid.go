// Package taskutil contains reusable helpers shared by slash commands and REST
// handlers: ULID generation, the canonical Task status state machine, and the
// Kanban OrderKey algorithm. Keeping these in one package guarantees that
// every code path applies status transitions and ordering consistently.
package taskutil

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// GenerateULID returns a new canonical 26-character ULID string. ULIDs are
// globally unique and time-sortable, so the ID doubles as the creation-order
// sort key without needing a central sequence counter (which would be a
// contention hotspot).
//
// MustNew panics only if the entropy reader fails; DefaultEntropy uses
// crypto/rand, which effectively never errors in a running plugin, so a panic
// here is treated as a non-recoverable infrastructure failure.
func GenerateULID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), ulid.DefaultEntropy()).String()
}
