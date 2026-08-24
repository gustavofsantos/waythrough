package lsp_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

var _ = Describe("call hierarchy", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
	})

	AfterEach(func() { cancel() })

	When("the prepared function has an incoming caller", func() {
		It("returns the root, caller, and every call site", func() {
			manager, file := fakeManager(ctx, "target()",
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[{
					"name":"target","kind":12,"detail":"func target()",
					"uri":"file:///root/main.fake",
					"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":8}},
					"selectionRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":6}},
					"data":{"opaque":"preserve me"}
				}]`,
				"-incoming-calls="+`[{
					"from":{
						"name":"caller","kind":12,"detail":"func caller()",
						"uri":"file:///root/caller.fake",
						"range":{"start":{"line":3,"character":0},"end":{"line":5,"character":1}},
						"selectionRange":{"start":{"line":3,"character":5},"end":{"line":3,"character":11}}
					},
					"fromRanges":[
						{"start":{"line":4,"character":1},"end":{"line":4,"character":7}},
						{"start":{"line":5,"character":1},"end":{"line":5,"character":7}}
					]
				}]`)

			hierarchies, err := manager.CallHierarchy(ctx, "fake", file, 1, 1, "incoming")
			Expect(err).NotTo(HaveOccurred())
			Expect(hierarchies).To(Equal([]lsp.CallHierarchy{{
				Symbol: lsp.Symbol{
					Name: "target", Kind: "function", Detail: "func target()",
					Location: lsp.Location{File: "/root/main.fake", Line: 1, Column: 1},
				},
				Calls: []lsp.Call{{
					Symbol: lsp.Symbol{
						Name: "caller", Kind: "function", Detail: "func caller()",
						Location: lsp.Location{File: "/root/caller.fake", Line: 4, Column: 6},
					},
					CallSites: []lsp.Location{
						{File: "/root/caller.fake", Line: 5, Column: 2},
						{File: "/root/caller.fake", Line: 6, Column: 2},
					},
				}},
			}}))
		})
	})

	When("the prepared function has an outgoing callee", func() {
		It("returns the callee and locates the call site in the root", func() {
			manager, file := fakeManager(ctx, "root()",
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[{
					"name":"root","kind":12,"uri":"file:///root/main.fake",
					"range":{"start":{"line":0,"character":0},"end":{"line":2,"character":1}},
					"selectionRange":{"start":{"line":0,"character":5},"end":{"line":0,"character":9}}
				}]`,
				"-outgoing-calls="+`[{
					"to":{
						"name":"callee","kind":6,"detail":"func callee()",
						"uri":"file:///root/callee.fake",
						"range":{"start":{"line":6,"character":0},"end":{"line":8,"character":1}},
						"selectionRange":{"start":{"line":6,"character":5},"end":{"line":6,"character":11}}
					},
					"fromRanges":[{"start":{"line":1,"character":1},"end":{"line":1,"character":7}}]
				}]`)

			hierarchies, err := manager.CallHierarchy(ctx, "fake", file, 1, 6, "outgoing")
			Expect(err).NotTo(HaveOccurred())
			Expect(hierarchies).To(Equal([]lsp.CallHierarchy{{
				Symbol: lsp.Symbol{
					Name: "root", Kind: "function",
					Location: lsp.Location{File: "/root/main.fake", Line: 1, Column: 6},
				},
				Calls: []lsp.Call{{
					Symbol: lsp.Symbol{
						Name: "callee", Kind: "method", Detail: "func callee()",
						Location: lsp.Location{File: "/root/callee.fake", Line: 7, Column: 6},
					},
					CallSites: []lsp.Location{{File: "/root/main.fake", Line: 2, Column: 2}},
				}},
			}}))
		})
	})
})
