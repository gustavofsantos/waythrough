package editor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

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

func cannedCallHierarchyRoots(count int) string {
	items := make([]protocol.CallHierarchyItem, count)
	for index := range items {
		name := fmt.Sprintf("root-%02d", index)
		items[index] = protocol.CallHierarchyItem{
			Name:           name,
			Kind:           protocol.SymbolKindFunction,
			URI:            uri.URI(fmt.Sprintf("file:///root/%s.fake", name)),
			Range:          protocol.Range{},
			SelectionRange: protocol.Range{},
		}
	}
	encoded, err := json.Marshal(items)
	Expect(err).NotTo(HaveOccurred())
	return string(encoded)
}

func recordedMethods(path string) string {
	content, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return string(content)
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
				`-call-hierarchy-required-data={"opaque":"preserve me"}`,
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

	When("the direction is not incoming or outgoing", func() {
		It("rejects the call before sending a hierarchy request", func() {
			writeFile(root, "main.fake", "target()")
			requestLog := filepath.Join(root, "requests.log")
			cfg := fakeConfig("-call-hierarchy", "-request-log="+requestLog)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "sideways")
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("incoming or outgoing"))
			Expect(recordedMethods(requestLog)).NotTo(ContainSubstring("CallHierarchy"))
			Expect(recordedMethods(requestLog)).NotTo(ContainSubstring("callHierarchy/"))
		})
	})

	When("the language server advertises no call hierarchy capability", func() {
		It("returns an explicit error without sending a hierarchy request", func() {
			writeFile(root, "main.fake", "target()")
			requestLog := filepath.Join(root, "requests.log")
			cfg := fakeConfig("-request-log=" + requestLog)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "incoming")
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("does not support call hierarchy"))
			methods := recordedMethods(requestLog)
			Expect(methods).NotTo(ContainSubstring("prepareCallHierarchy"))
			Expect(methods).NotTo(ContainSubstring("callHierarchy/"))
		})
	})

	When("the language server explicitly advertises call hierarchy as false", func() {
		It("returns the same unsupported capability error", func() {
			writeFile(root, "main.fake", "target()")
			cfg := fakeConfig("-call-hierarchy-false")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "incoming")
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("does not support call hierarchy"))
		})
	})

	When("the server prepares no symbol at the position", func() {
		It("returns no roots, rather than a failed tool call", func() {
			writeFile(root, "main.fake", "target()")
			cfg := fakeConfig("-call-hierarchy")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "incoming")
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })
			Expect(decodeToolOutput[callHierarchyToolOutput](result).Roots).To(BeEmpty())
		})
	})

	When("the server prepares more than sixteen roots", func() {
		It("fails before requesting calls for any root", func() {
			writeFile(root, "main.fake", "target()")
			requestLog := filepath.Join(root, "requests.log")
			cfg := fakeConfig(
				"-call-hierarchy",
				"-call-hierarchy-roots="+cannedCallHierarchyRoots(17),
				"-request-log="+requestLog)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "incoming")
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("maximum is 16"))
			Expect(recordedMethods(requestLog)).NotTo(ContainSubstring("callHierarchy/incomingCalls"))
		})
	})

	When("the server prepares exactly sixteen roots", func() {
		It("queries every root and returns all of them", func() {
			writeFile(root, "main.fake", "target()")
			requestLog := filepath.Join(root, "requests.log")
			cfg := fakeConfig(
				"-call-hierarchy",
				"-call-hierarchy-roots="+cannedCallHierarchyRoots(16),
				"-request-log="+requestLog)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "incoming")
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })
			Expect(decodeToolOutput[callHierarchyToolOutput](result).Roots).To(HaveLen(16))
			Expect(strings.Count(recordedMethods(requestLog), "callHierarchy/incomingCalls")).To(Equal(16))
		})
	})

	When("a later prepared root fails", func() {
		It("returns an error and no partial hierarchy", func() {
			writeFile(root, "main.fake", "target()")
			cfg := fakeConfig(
				"-call-hierarchy",
				"-call-hierarchy-roots="+cannedCallHierarchyRoots(2),
				"-call-hierarchy-error-after=1")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "incoming")
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("incoming calls for"))
			Expect(errorText(result)).NotTo(ContainSubstring(`"roots"`))
		})
	})

	When("the server prepares roots in an arbitrary order", func() {
		It("returns a stable source order", func() {
			writeFile(root, "main.fake", "target()")
			cfg := fakeConfig(
				"-call-hierarchy",
				"-call-hierarchy-roots="+`[
					{"name":"root-z","kind":12,"uri":"file:///root/z.fake","range":{"start":{"line":8,"character":0},"end":{"line":8,"character":1}},"selectionRange":{"start":{"line":8,"character":0},"end":{"line":8,"character":1}}},
					{"name":"root-a","kind":12,"uri":"file:///root/a.fake","range":{"start":{"line":1,"character":0},"end":{"line":1,"character":1}},"selectionRange":{"start":{"line":1,"character":0},"end":{"line":1,"character":1}}}
				]`)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callHierarchyTool(ctx, session, "main.fake", 1, 1, "incoming")
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })
			out := decodeToolOutput[callHierarchyToolOutput](result)
			Expect([]string{out.Roots[0].Symbol.Name, out.Roots[1].Symbol.Name}).To(
				Equal([]string{"root-a", "root-z"}))
		})
	})
})
