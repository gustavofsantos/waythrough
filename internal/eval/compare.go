package eval

// Location is a 1-based source position returned by an evaluator path.
// File paths are relative to the evaluated fixture before comparison.
type Location struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type qualityMetrics struct {
	TruePositives  int
	FalsePositives int
	FalseNegatives int
	Precision      float64
	Recall         float64
	F1             float64
	ExactMatch     bool
}

func scoreLocations(actual, expected []Location) qualityMetrics {
	expectedSet := make(map[Location]struct{}, len(expected))
	for _, location := range expected {
		expectedSet[location] = struct{}{}
	}

	matched := make(map[Location]struct{}, len(actual))
	truePositives := 0
	falsePositives := 0
	for _, location := range actual {
		if _, expected := expectedSet[location]; expected {
			if _, alreadyMatched := matched[location]; !alreadyMatched {
				matched[location] = struct{}{}
				truePositives++
				continue
			}
		}
		falsePositives++
	}

	falseNegatives := len(expectedSet) - truePositives
	precision, recall, f1 := qualityRates(truePositives, falsePositives, falseNegatives)
	if len(actual) == 0 && len(expectedSet) == 0 {
		precision = 1
		recall = 1
		f1 = 1
	}

	return qualityMetrics{
		TruePositives:  truePositives,
		FalsePositives: falsePositives,
		FalseNegatives: falseNegatives,
		Precision:      precision,
		Recall:         recall,
		F1:             f1,
		ExactMatch:     sameLocations(actual, expected),
	}
}

func qualityRates(truePositives, falsePositives, falseNegatives int) (float64, float64, float64) {
	precision := 0.0
	if truePositives+falsePositives > 0 {
		precision = float64(truePositives) / float64(truePositives+falsePositives)
	}
	recall := 0.0
	if truePositives+falseNegatives > 0 {
		recall = float64(truePositives) / float64(truePositives+falseNegatives)
	}
	f1 := 0.0
	if precision+recall > 0 {
		f1 = 2 * precision * recall / (precision + recall)
	}
	return precision, recall, f1
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
