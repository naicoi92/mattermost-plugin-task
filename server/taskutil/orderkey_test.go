package taskutil

import (
	"strings"
	"testing"
)

func TestNextOrderKey(t *testing.T) {
	cases := []struct {
		max  string
		want string
	}{
		{"", "n"},  // first task ever
		{"m", "n"}, // simple increment
		{"n", "o"},
		{"n0", "n1"}, // increment last digit, keep length
		{"n9", "nA"}, // carry across digit->letter boundary
		{"z", "z0"},  // all-largest -> append one char
		{"zz", "zz0"},
		{"z9", "zA"},
	}
	for _, tc := range cases {
		if got := NextOrderKey(tc.max); got != tc.want {
			t.Errorf("NextOrderKey(%q) = %q, want %q", tc.max, got, tc.want)
		}
	}
}

func TestNextOrderKey_AlwaysGreaterThanMax(t *testing.T) {
	for _, max := range []string{"a", "m", "n", "z", "n0", "zzz", "m00", "0", "n9", "zA"} {
		got := NextOrderKey(max)
		if got <= max {
			t.Errorf("NextOrderKey(%q) = %q, expected > %q", max, got, max)
		}
	}
}

func TestNextOrderKey_MonotonicChain(t *testing.T) {
	// Simulate creating many tasks in sequence: each new key must be strictly
	// greater than the running max so new tasks always append at the end.
	max := NextOrderKey("") // first task
	for range 50 {
		next := NextOrderKey(max)
		if next <= max {
			t.Fatalf("NextOrderKey(%q) = %q, not greater than max", max, next)
		}
		max = next
	}
}

func TestNextOrderKey_BoundedLength(t *testing.T) {
	// Start from a worst-case legacy max (len 64, all smallest chars '0' like
	// the old append-a-"0" algorithm produced). The key must stay strictly
	// increasing and never exceed VARCHAR(64), proving the overflow is gone.
	max := "n" + strings.Repeat("0", 63) // len 64, mimics legacy accumulation
	if len(max) != 64 {
		t.Fatalf("setup: max len = %d, want 64", len(max))
	}
	for i := range 10000 {
		next := NextOrderKey(max)
		if next <= max {
			t.Fatalf("iter %d: NextOrderKey(%q) = %q, not greater", i, max, next)
		}
		if len(next) > 64 {
			t.Fatalf("iter %d: key %q len %d exceeds 64", i, next, len(next))
		}
		max = next
	}
}
