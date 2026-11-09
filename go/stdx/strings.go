package stdx

// uuidLength is the length of a canonical UUID string.
const uuidLength = 36

// Truncate returns s truncated to at most limit runes. If s fits within limit,
// it is returned unchanged.
func Truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}

	return string(runes[:limit])
}

// IsAllDigits reports whether s is non-empty and every byte is an ASCII digit.
func IsAllDigits(text string) bool {
	if text == "" {
		return false
	}

	for i := range len(text) {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}

	return true
}

// IsHex reports whether s is non-empty and every byte is an ASCII hexadecimal
// digit (0-9, a-f, A-F).
func IsHex(text string) bool {
	if text == "" {
		return false
	}

	for i := range len(text) {
		if !isHexByte(text[i]) {
			return false
		}
	}

	return true
}

// isHexByte reports whether b is an ASCII hexadecimal digit (0-9, a-f, A-F).
func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// IsUUID reports whether s is a canonical 8-4-4-4-12 hyphenated UUID: 36 bytes,
// ASCII hex with hyphens at positions 8, 13, 18, and 23.
func IsUUID(text string) bool {
	if len(text) != uuidLength {
		return false
	}

	for i := range len(text) {
		if !uuidByteOK(i, text[i]) {
			return false
		}
	}

	return true
}

// uuidByteOK checks the byte at i: a dash at the four group breaks, hex elsewhere.
func uuidByteOK(i int, count byte) bool {
	if i == uuidFirstDash || i == uuidSecondDash || i == uuidThirdDash ||
		i == uuidFourthDash {
		return count == '-'
	}

	return isHexByte(count)
}
