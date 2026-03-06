package output

// TruncateAtLine truncates s at the last newline before the character budget.
// Returns the truncated string and whether truncation occurred.
func TruncateAtLine(s string, charBudget int) (string, bool) {
	if charBudget <= 0 || len(s) <= charBudget {
		return s, false
	}

	// Find the last newline at or before the budget
	cut := charBudget
	for cut > 0 && s[cut-1] != '\n' {
		cut--
	}
	if cut == 0 {
		// No newline found before budget — take whole first line
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				return s[:i+1] + "... (truncated)", true
			}
		}
		// Single line, no newline at all
		return s[:charBudget] + "... (truncated)", true
	}

	return s[:cut] + "... (truncated)", true
}

// TruncateBodyToTokenBudget truncates a body string to fit within a token budget.
// Uses the ~4 chars/token heuristic. Returns the body and whether it was truncated.
func TruncateBodyToTokenBudget(body string, budget, usedTokens int) (string, bool) {
	if budget <= 0 {
		return body, false
	}
	remaining := (budget - usedTokens) * 4
	if remaining <= 0 {
		return "", true
	}
	if remaining >= len(body) {
		return body, false
	}
	return TruncateAtLine(body, remaining)
}
