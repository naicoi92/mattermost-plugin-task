package taskutil

// NextOrderKey returns a Kanban OrderKey that is lexicographically greater than
// maxOrderKey, so a newly created task always lands at the end of its default
// column. The midpoint algorithm used for drag-and-drop reordering is added
// later (Phase 4); for now we simply append a suffix to the current maximum
// (see PLAN.md, Phụ lục A).
//
//	maxOrderKey == ""  -> "n"   (first task ever: midpoint of "a".."z")
//	maxOrderKey == "m" -> "m0"
//	maxOrderKey == "m0"-> "m00"
//
// Appending a character always yields a greater string, because a string is
// strictly greater than any of its own proper prefixes.
func NextOrderKey(maxOrderKey string) string {
	if maxOrderKey == "" {
		return "n"
	}
	return maxOrderKey + "0"
}
