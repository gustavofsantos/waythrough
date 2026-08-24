package eval

import "testing"

func TestTextOnlyNavigationReportsEveryMatchingLocation(t *testing.T) {
	output := "./provider.go:3:4:// Target is the declaration\n" +
		"./provider.go:4:6:func Target(value int) int\n" +
		"./consumer.go:4:9:return Target(41)\n"

	locations, err := parseRipgrepLocations(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []Location{
		{File: "provider.go", Line: 3, Column: 4},
		{File: "provider.go", Line: 4, Column: 6},
		{File: "consumer.go", Line: 4, Column: 9},
	}
	if !sameLocations(locations, want) {
		t.Fatalf("locations = %#v, want %#v", locations, want)
	}
}

func TestManifestRejectsAGoldLocationOutsideTheFixture(t *testing.T) {
	scenario := Scenario{
		Name: "definition.outside_fixture", Tool: "get_definition", File: "consumer.go",
		Line: 1, Column: 1, Symbol: "Target",
		Gold: []Location{{File: "../provider.go", Line: 1, Column: 1}},
	}

	if err := validateScenario(scenario); err == nil {
		t.Fatal("a gold location outside the fixture should be rejected")
	}
}

func TestTheEvaluatorAggregatesQualityAcrossScenarios(t *testing.T) {
	results := []Result{
		{
			Method:         "waythrough",
			TruePositives:  1,
			FalsePositives: 0,
			FalseNegatives: 0,
			ExactMatch:     true,
			OutputBytes:    10,
		},
		{
			Method:         "waythrough",
			TruePositives:  1,
			FalsePositives: 1,
			FalseNegatives: 0,
			ExactMatch:     false,
			OutputBytes:    20,
		},
	}

	summaries := summarizeResults(results)
	if len(summaries) != 1 {
		t.Fatalf("summary count = %d, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.ScenarioCount != 2 || summary.TruePositives != 2 ||
		summary.FalsePositives != 1 || summary.FalseNegatives != 0 ||
		summary.Precision != 2.0/3.0 || summary.Recall != 1 ||
		summary.F1 != 0.8 || summary.ExactMatchRate != 0.5 || summary.OutputBytes != 30 {
		t.Fatalf("summary = %#v, want aggregated metrics", summary)
	}
}
