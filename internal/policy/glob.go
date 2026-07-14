package policy

// Glob reports whether s matches the pattern, where '*' matches any run
// of characters (including none, and including '.') and '?' matches
// exactly one character. Matching is case-sensitive and has no character
// classes — action names are chosen by the operator, so the pattern
// language stays deliberately small and predictable.
func Glob(pattern, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		switch {
		case px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]):
			px++
			sx++
		case px < len(pattern) && pattern[px] == '*':
			star, mark = px, sx
			px++
		case star >= 0:
			// Backtrack: let the last '*' swallow one more character.
			px = star + 1
			mark++
			sx = mark
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}
