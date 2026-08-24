package editor_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

type toolCallHierarchySymbol struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type toolCall struct {
	Symbol    toolCallHierarchySymbol `json:"symbol"`
	CallSites []toolLocation          `json:"call_sites"`
}

type toolCallHierarchyRoot struct {
	Symbol toolCallHierarchySymbol `json:"symbol"`
	Calls  []toolCall              `json:"calls"`
}

type callHierarchyToolOutput struct {
	Roots []toolCallHierarchyRoot `json:"roots"`
}

func callHierarchyTool(
	ctx context.Context, session *mcp.ClientSession, file string, line, column int, direction string,
) *mcp.CallToolResult {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_call_hierarchy",
		Arguments: map[string]any{
			"file": file, "line": line, "column": column, "direction": direction,
		},
	})
	Expect(err).NotTo(HaveOccurred(), "a tool-level failure is not a protocol error")
	return result
}

var _ = Describe("get_call_hierarchy", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		root   string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		root = GinkgoT().TempDir()
	})

	AfterEach(func() { cancel() })

	When("the position identifies a function with a caller", func() {
		It("returns the direct incoming call and every call site, 1-based", func() {
			writeFile(root, "main.fake", "target()")

			cfg := fakeConfig(
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[{
					"name":"target",
					"kind":12,
					"detail":"func target()",
					"uri":"file:///root/main.fake",
					"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":8}},
					"selectionRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":6}},
					"data":{"opaque":"preserve me"}
				}]`,
				"-incoming-calls="+`[{
					"from":{
						"name":"caller",
						"kind":12,
						"detail":"func caller()",
						"uri":"file:///root/caller.fake",
						"range":{"start":{"line":3,"character":0},"end":{"line":5,"character":1}},
						"selectionRange":{"start":{"line":3,"character":5},"end":{"line":3,"character":11}}
					},
					"fromRanges":[
						{"start":{"line":4,"character":1},"end":{"line":4,"character":7}},
						{"start":{"line":5,"character":1},"end":{"line":5,"character":7}}
					]
				}]`,
			)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "incoming")
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			out := decodeToolOutput[callHierarchyToolOutput](result)
			Expect(out.Roots).To(Equal([]toolCallHierarchyRoot{{
				Symbol: toolCallHierarchySymbol{
					Name: "target", Kind: "function", Detail: "func target()",
					File: "/root/main.fake", Line: 1, Column: 1,
				},
				Calls: []toolCall{{
					Symbol: toolCallHierarchySymbol{
						Name: "caller", Kind: "function", Detail: "func caller()",
						File: "/root/caller.fake", Line: 4, Column: 6,
					},
					CallSites: []toolLocation{
						{File: filepath.Clean("/root/caller.fake"), Line: 5, Column: 2},
						{File: filepath.Clean("/root/caller.fake"), Line: 6, Column: 2},
					},
				}},
			}}))
		})
	})

	When("the position identifies a function that calls another function", func() {
		It("returns the direct outgoing call and its call site in the root", func() {
			writeFile(root, "main.fake", "root()")

			cfg := fakeConfig(
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
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 6, "outgoing")
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			out := decodeToolOutput[callHierarchyToolOutput](result)
			Expect(out.Roots).To(Equal([]toolCallHierarchyRoot{{
				Symbol: toolCallHierarchySymbol{
					Name: "root", Kind: "function", File: "/root/main.fake", Line: 1, Column: 6,
				},
				Calls: []toolCall{{
					Symbol: toolCallHierarchySymbol{
						Name: "callee", Kind: "method", Detail: "func callee()",
						File: "/root/callee.fake", Line: 7, Column: 6,
					},
					CallSites: []toolLocation{{File: "/root/main.fake", Line: 2, Column: 2}},
				}},
			}}))
		})
	})
})
