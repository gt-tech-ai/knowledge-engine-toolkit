package helpers

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
func IsAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// IsHex reports whether s is non-empty and every byte is an ASCII hexadecimal
// digit (0-9, a-f, A-F).
func IsHex(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if !isHexByte(s[i]) {
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
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	if s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for i := range len(s) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}
