package agentstream

const (
	maxTurnMessageBytes = 1024 * 1024
	maxCommandTailBytes = 16 * 1024
	maxTurnDiffBytes    = 512 * 1024
)

func clipString(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	return s[:max], true
}

func clipWithBudget(s string, maxField int, budget *int) (string, bool) {
	truncated := false
	if maxField > 0 && len(s) > maxField {
		s = s[:maxField]
		truncated = true
	}
	if budget == nil {
		return s, truncated
	}
	if *budget <= 0 {
		return "", true
	}
	if len(s) > *budget {
		s = s[:*budget]
		truncated = true
	}
	*budget -= len(s)
	return s, truncated
}
