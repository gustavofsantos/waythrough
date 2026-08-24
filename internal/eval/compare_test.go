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
