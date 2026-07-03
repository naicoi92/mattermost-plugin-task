package taskutil

// orderKeyAlphabet is the base-62 lexicographic alphabet used for order keys.
// '0' is the smallest character and 'z' the largest. Order keys are compared
// as ordinary Go strings (byte-wise lexicographic); since every character here
// is ASCII ordered ascending by code point, that comparison matches the
// intended numeric ordering.
const orderKeyAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// orderKeyCharIndex maps an alphabet byte to its position (0..61); -1 means the
// byte is outside the alphabet. Inputs to NextOrderKey are always valid order
// keys (legacy data uses only 'n' and '0'), so this only guards correctness.
var orderKeyCharIndex [256]int8

func init() {
	for i := range orderKeyCharIndex {
		orderKeyCharIndex[i] = -1
	}
	for i := range len(orderKeyAlphabet) {
		orderKeyCharIndex[orderKeyAlphabet[i]] = int8(i)
	}
}

// NextOrderKey returns an order key strictly greater than maxOrderKey so a newly
// created task always lands at the end of its default column. It treats
// maxOrderKey as a base-62 integer and increments it by one:
//
//	maxOrderKey == "" -> "n"   (first task ever)
//	"m"  -> "n"
//	"n0" -> "n1"               (keep length)
//	"n9" -> "nA"               (carry across digit->letter boundary)
//	"z"  -> "z0"               (all-largest -> append one char)
//
// The result keeps the same length as the input except when the input is made
// entirely of 'z', in which case one '0' is appended. Reaching that state needs
// roughly 62^len prior increments, so in practice the length never grows. This
// bounds the key length and prevents the VARCHAR(64) overflow the previous
// append-a-"0" implementation hit once 64 tasks existed.
func NextOrderKey(maxOrderKey string) string {
	if maxOrderKey == "" {
		return "n"
	}
	return incrementOrderKey(maxOrderKey)
}

// incrementOrderKey returns s+1 in base-62 with carry, always strictly greater
// than s. On all-'z' input it appends '0', since s is a prefix of s+"0" and a
// string is strictly less than its proper extensions.
func incrementOrderKey(s string) string {
	b := []byte(s)
	last := int8(len(orderKeyAlphabet) - 1) // index of 'z'
	for i := len(b) - 1; i >= 0; i-- {
		idx := orderKeyCharIndex[b[i]]
		if idx < last { // not 'z': increment this position and stop
			b[i] = orderKeyAlphabet[idx+1]
			return string(b)
		}
		b[i] = orderKeyAlphabet[0] // carry: reset to '0', continue left
	}
	// Every position was 'z': append one '0' (s+"0" > s by the prefix rule).
	return s + "0"
}
