package lsp

import (
	"sort"
	"testing"
)

func TestCallHierarchyOrderingBreaksSerializedTies(t *testing.T) {
	location := Location{File: "/root/main.fake", Line: 1, Column: 1}
	root := Symbol{Name: "root", Kind: "function", Location: location}
	callSymbol := Symbol{Name: "called", Kind: "function", Location: location}
	hierarchies := []CallHierarchy{
		{
			Symbol: Symbol{Name: root.Name, Kind: root.Kind, Detail: "z", Location: location},
			Calls: []Call{{
				Symbol:    callSymbol,
				CallSites: []Location{{File: "/root/z.fake", Line: 1, Column: 1}},
			}},
		},
		{
			Symbol: Symbol{Name: root.Name, Kind: root.Kind, Detail: "a", Location: location},
			Calls: []Call{{
				Symbol:    callSymbol,
				CallSites: []Location{{File: "/root/a.fake", Line: 1, Column: 1}},
			}},
		},
	}

	sort.Slice(hierarchies, func(left, right int) bool {
		return callHierarchyLess(hierarchies[left], hierarchies[right])
	})
	if got := hierarchies[0].Symbol.Detail; got != "a" {
		t.Fatalf("first detail = %q, want a", got)
	}

	calls := []Call{hierarchies[0].Calls[0], hierarchies[1].Calls[0]}
	sortCalls(calls)
	if got := calls[0].CallSites[0].File; got != "/root/a.fake" {
		t.Fatalf("first tied call site = %q, want /root/a.fake", got)
	}
}
