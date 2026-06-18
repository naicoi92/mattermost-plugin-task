package taskutil

import "testing"

func TestNextOrderKey(t *testing.T) {
	cases := []struct {
		max  string
		want string
	}{
		{"", "n"}, // first task ever: midpoint of "a".."z"
		{"m", "m0"},
		{"m0", "m00"},
		{"n", "n0"},
		{"z", "z0"},
	}
	for _, tc := range cases {
		if got := NextOrderKey(tc.max); got != tc.want {
			t.Errorf("NextOrderKey(%q) = %q, want %q", tc.max, got, tc.want)
		}
	}
}

func TestNextOrderKey_AlwaysGreaterThanMax(t *testing.T) {
	for _, max := range []string{"a", "m", "n", "z", "n0", "zzz", "m00"} {
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
