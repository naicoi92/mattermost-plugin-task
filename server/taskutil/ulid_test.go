package taskutil

import (
	"regexp"
	"testing"
	"time"
)

// crockfordBase32 matches canonical ULID output: 26 chars of Crockford base32
// (0-9, A-Z excluding I L O U).
var crockfordBase32 = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

func TestGenerateULID_Format(t *testing.T) {
	for range 100 {
		id := GenerateULID()
		if !crockfordBase32.MatchString(id) {
			t.Errorf("GenerateULID() = %q, want 26-char Crockford base32", id)
		}
	}
}

func TestGenerateULID_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for range n {
		id := GenerateULID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ULID generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestGenerateULID_CrossMillisecondSorted(t *testing.T) {
	// ULIDs sort by their millisecond timestamp prefix; different milliseconds
	// must therefore be strictly ordered. (Within the same millisecond the
	// random tail dominates, so cross-millisecond is the guaranteed case.)
	first := GenerateULID()
	time.Sleep(2 * time.Millisecond)
	second := GenerateULID()
	if first >= second {
		t.Errorf("expected %q < %q across milliseconds", first, second)
	}
}
