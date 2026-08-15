package editor_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

type toolDiagnostic struct {
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Source      string `json:"source"`
	Code        string `json:"code"`
}

type diagnosticsToolOutput struct {
	Diagnostics []toolDiagnostic `json:"diagnostics"`
}

// callDiagnosticsTool calls get_diagnostics, which takes a file and nothing
// else: a diagnostic belongs to a file, not to a position in it.
func callDiagnosticsTool(
	ctx context.Context, session *mcp.ClientSession, file string,
) *mcp.CallToolResult {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_diagnostics",
		Arguments: map[string]any{"file": file},
	})
	Expect(err).NotTo(HaveOccurred(), "a tool-level failure is not a protocol error")
	return result
}

var _ = Describe("get_diagnostics", func() {
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

	When("the language server knows what is broken in the file", func() {
		It("returns each problem where it is, how bad it is, and what it says", func() {
			writeFile(root, "main.fake", "greet()\nfarewell()")

			cfg := fakeConfig("-pull-diagnostics", "-diagnostics="+`{
				"kind": "full",
				"items": [
					{
						"range": {
							"start": {"line": 0, "character": 0},
							"end": {"line": 0, "character": 7}
						},
						"severity": 1,
						"code": "arity",
						"source": "fakelint",
						"message": "greet expects 1 argument, got 0"
					},
					{
						"range": {
							"start": {"line": 1, "character": 0},
							"end": {"line": 1, "character": 10}
						},
						"severity": 2,
						"message": "farewell is deprecated"
					}
				]
			}`)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			// "main.fake" is relative to root: proves path resolution joins
			// against the project root rather than passing it through raw.
			result := callDiagnosticsTool(ctx, session, "main.fake")
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			out := decodeToolOutput[diagnosticsToolOutput](result)
			Expect(out.Diagnostics).To(Equal([]toolDiagnostic{
				{
					StartLine: 1, StartColumn: 1,
					EndLine: 1, EndColumn: 8,
					Severity: "error",
					Message:  "greet expects 1 argument, got 0",
					Source:   "fakelint",
					Code:     "arity",
				},
				{
					StartLine: 2, StartColumn: 1,
					EndLine: 2, EndColumn: 11,
					Severity: "warning",
					Message:  "farewell is deprecated",
				},
			}))
		})
	})

	When("the configured server never advertised pull diagnostics", func() {
		It("says so, naming the server, instead of reporting a clean file", func() {
			writeFile(root, "main.fake", "greet()")

			// No -pull-diagnostics, yet the server would answer if asked:
			// the error must come from the missing capability, not from the
			// answer.
			cfg := fakeConfig("-diagnostics=" + `{"kind":"full","items":[]}`)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callDiagnosticsTool(ctx, session, "main.fake")
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("fake"))
			Expect(errorText(result)).To(ContainSubstring("pull diagnostics"))
		})
	})
})
