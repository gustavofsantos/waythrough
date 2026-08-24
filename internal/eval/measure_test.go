package eval

import "testing"

func TestTheFirstEvaluationRunIsLabeledCold(t *testing.T) {
	if got := measurementPhase(1); got != "cold" {
		t.Fatalf("phase for first run = %q, want cold", got)
	}
}

func TestLaterEvaluationRunsAreLabeledWarm(t *testing.T) {
	if got := measurementPhase(2); got != "warm" {
		t.Fatalf("phase for second run = %q, want warm", got)
	}
}

func TestTheEvaluatorRejectsAnUnboundedRepeatCount(t *testing.T) {
	if _, err := normalizeRepeat(repeatMax + 1); err == nil {
		t.Fatal("repeat counts above the bound should be rejected")
	}
}
