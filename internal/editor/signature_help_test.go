package editor_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

type toolSignature struct {
	Label         string   `json:"label"`
	Documentation string   `json:"documentation"`
	Parameters    []string `json:"parameters"`
}

type signatureHelpToolOutput struct {
	Signatures      []toolSignature `json:"signatures"`
	ActiveSignature int             `json:"active_signature"`
	ActiveParameter int             `json:"active_parameter"`
}

func decodeSignatureHelpOutput(result *mcp.CallToolResult) signatureHelpToolOutput {
	text, ok := result.Content[0].(*mcp.TextContent)
	Expect(ok).To(BeTrue())
	var out signatureHelpToolOutput
	Expect(json.Unmarshal([]byte(text.Text), &out)).To(Succeed())
	return out
}

var _ = Describe("signature_help", func() {
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

	When("the call site has overloads and the server marks one of them active", func() {
		It("returns every overload, and points at the parameter the call site is on", func() {
			writeFile(root, "main.fake", `greet("world", "hi")`)

			cfg := fakeConfig("-signature-help=" + `{
				"signatures": [
					{
						"label": "greet(name)",
						"documentation": "Greet someone by name.",
						"parameters": [{"label": "name"}]
					},
					{
						"label": "greet(name, greeting)",
						"parameters": [{"label": "name"}, {"label": "greeting"}]
					}
				],
				"activeSignature": 1,
				"activeParameter": 1
			}`)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			// Column 16 sits on the second argument: the whole point of the
			// tool is that the agent learns this without reading the
			// declaration.
			result := callTool(ctx, session, "signature_help", "main.fake", 1, 16)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			Expect(decodeSignatureHelpOutput(result)).To(Equal(signatureHelpToolOutput{
				Signatures: []toolSignature{
					{
						Label:         "greet(name)",
						Documentation: "Greet someone by name.",
						Parameters:    []string{"name"},
					},
					{
						Label:      "greet(name, greeting)",
						Parameters: []string{"name", "greeting"},
					},
				},
				ActiveSignature: 1,
				ActiveParameter: 1,
			}))
		})
	})

	When("the position is not inside a call the server recognises", func() {
		It("returns no signatures, rather than a failed tool call", func() {
			writeFile(root, "main.fake", "hello")

			cfg := fakeConfig() // no -signature-help: fakelsp reports none.
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "signature_help", "main.fake", 1, 1)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })
			Expect(decodeSignatureHelpOutput(result).Signatures).To(BeEmpty())
		})
	})
})
