package lsp

import (
	"strings"
	"testing"
)

func TestBoundedIncomingResultRejectsPayloadBeforeDecode(t *testing.T) {
	payload := make([]byte, maxDirectedCallResultBytes+1)
	var result boundedIncomingCallResult

	err := result.UnmarshalJSON(payload)

	if err == nil || !strings.Contains(err.Error(), "result has") {
		t.Fatalf("expected result-size error, got %v", err)
	}
}

func TestBoundedIncomingResultStopsAfterCallBudgetSentinel(t *testing.T) {
	payload := "[" + strings.Repeat("{},", maxCallHierarchyCalls+1) + "{}]"
	var result boundedIncomingCallResult

	err := result.UnmarshalJSON([]byte(payload))

	if err == nil || !strings.Contains(err.Error(), "decoded more than 4097 calls") {
		t.Fatalf("expected decoded-call limit error, got %v", err)
	}
}

func TestBoundedIncomingResultStopsAfterSiteBudgetSentinel(t *testing.T) {
	payload := `[{"fromRanges":[` +
		strings.Repeat("{},", maxCallHierarchySites+1) + `{ }]}]`
	var result boundedIncomingCallResult

	err := result.UnmarshalJSON([]byte(payload))

	if err == nil || !strings.Contains(err.Error(), "decoded more than 16385 call sites") {
		t.Fatalf("expected decoded-site limit error, got %v", err)
	}
}
