package eval

// Location is a 1-based source position returned by an evaluator path.
// File paths are relative to the evaluated fixture before comparison.
type Location struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

// sameLocations compares locations as a set because an LSP server is free to
// return equivalent locations in a different order. Duplicate locations are
// still rejected by the length check, so a noisy path cannot pass by repeating
// the one correct answer.
func sameLocations(actual, expected []Location) bool {
	if len(actual) != len(expected) {
		return false
	}

	expectedSet := make(map[Location]struct{}, len(expected))
	for _, location := range expected {
		expectedSet[location] = struct{}{}
	}
	for _, location := range actual {
		if _, ok := expectedSet[location]; !ok {
			return false
		}
	}
	return true
}
