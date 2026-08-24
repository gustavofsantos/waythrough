package eval

import "testing"

func TestTheEvaluatorAcceptsTheGoldDefinitionLocation(t *testing.T) {
	gold := []Location{{File: "provider.go", Line: 3, Column: 6}}

	if !sameLocations(gold, gold) {
		t.Fatal("the gold location should be an exact match")
	}
}

func TestTheEvaluatorRejectsAnExtraTextMatch(t *testing.T) {
	gold := []Location{{File: "provider.go", Line: 3, Column: 6}}
	textMatches := append([]Location{}, gold...)
	textMatches = append(textMatches, Location{File: "consumer.go", Line: 4, Column: 8})

	if sameLocations(textMatches, gold) {
		t.Fatal("a text match at the call site must not count as the definition")
	}
}

func TestTheQualityMetricsCountCorrectExtraAndMissingLocations(t *testing.T) {
	expected := []Location{
		{File: "provider.go", Line: 4, Column: 6},
		{File: "provider.go", Line: 8, Column: 6},
	}
	actual := []Location{
		{File: "provider.go", Line: 4, Column: 6},
		{File: "consumer.go", Line: 4, Column: 9},
	}

	metrics := scoreLocations(actual, expected)
	if metrics.TruePositives != 1 || metrics.FalsePositives != 1 || metrics.FalseNegatives != 1 {
		t.Fatalf("counts = %#v, want one true, extra, and missing location", metrics)
	}
	if metrics.Precision != 0.5 || metrics.Recall != 0.5 || metrics.F1 != 0.5 {
		t.Fatalf("rates = %#v, want 0.5 precision, recall, and F1", metrics)
	}
	if metrics.ExactMatch {
		t.Fatal("a result with extra and missing locations must not be exact")
	}
}

func TestTheQualityMetricsTreatNoAnswerAsACompleteAnswerWhenNothingIsExpected(t *testing.T) {
	metrics := scoreLocations(nil, nil)
	if metrics.Precision != 1 || metrics.Recall != 1 || metrics.F1 != 1 || !metrics.ExactMatch {
		t.Fatalf("metrics = %#v, want a complete empty answer", metrics)
	}
}

func TestTheQualityMetricsPenalizeNoAnswerWhenAResultIsExpected(t *testing.T) {
	metrics := scoreLocations(nil, []Location{{File: "provider.go", Line: 4, Column: 6}})
	if metrics.TruePositives != 0 || metrics.FalsePositives != 0 || metrics.FalseNegatives != 1 ||
		metrics.Precision != 0 || metrics.Recall != 0 || metrics.F1 != 0 || metrics.ExactMatch {
		t.Fatalf("metrics = %#v, want one missed location", metrics)
	}
}
