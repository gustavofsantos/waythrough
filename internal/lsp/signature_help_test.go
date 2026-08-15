package lsp_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// signatureHelpManager starts a manager whose "fake" server answers
// textDocument/signatureHelp with the canned result, and returns it with the
// path of the file every spec below asks about.
func signatureHelpManager(ctx context.Context, result string) (*lsp.Manager, string) {
	root := GinkgoT().TempDir()
	file := filepath.Join(root, "main.fake")
	Expect(os.WriteFile(file, []byte(`greet("world", "hi")`), 0o644)).To(Succeed())

	entry := config.LanguageServer{
		Name:      "fake",
		Command:   fakelspPath,
		Args:      []string{"-signature-help=" + result},
		Readiness: config.ReadinessHandshake,
		Filetypes: map[string]string{".fake": "fake"},
	}

	manager := lsp.NewManager(root, []config.LanguageServer{entry})
	Expect(manager.Start(ctx)).To(Succeed())
	Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())
	return manager, file
}

var _ = Describe("Manager.SignatureHelp", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
	})

	AfterEach(func() { cancel() })

	When("the server reports a signature but names no active index", func() {
		It("reads the signature, and falls back to the first of each, as the spec says", func() {
			manager, file := signatureHelpManager(ctx, `{
				"signatures": [{
					"label": "greet(name, greeting)",
					"documentation": "Greet someone by name.",
					"parameters": [{"label": "name"}, {"label": "greeting"}]
				}]
			}`)

			help, err := manager.SignatureHelp(ctx, "fake", file, 1, 7)
			Expect(err).NotTo(HaveOccurred())

			Expect(help).To(Equal(lsp.SignatureHelp{
				Signatures: []lsp.Signature{{
					Label:         "greet(name, greeting)",
					Documentation: "Greet someone by name.",
					Parameters:    []string{"name", "greeting"},
				}},
				ActiveSignature: 0,
				ActiveParameter: 0,
			}))
		})
	})

	When("the active signature carries its own active parameter", func() {
		It("prefers it over the whole result's, which LSP 3.16 deprecated", func() {
			manager, file := signatureHelpManager(ctx, `{
				"signatures": [
					{"label": "greet(name)", "parameters": [{"label": "name"}]},
					{
						"label": "greet(name, greeting)",
						"parameters": [{"label": "name"}, {"label": "greeting"}],
						"activeParameter": 1
					}
				],
				"activeSignature": 1,
				"activeParameter": 0
			}`)

			help, err := manager.SignatureHelp(ctx, "fake", file, 1, 16)
			Expect(err).NotTo(HaveOccurred())

			Expect(help.ActiveSignature).To(Equal(1))
			Expect(help.ActiveParameter).To(Equal(1),
				"the active parameter belongs to the active signature, not to the first one")
		})
	})

	When("the server names a signature that its own list does not hold", func() {
		It("falls back to the first signature, rather than to no signature at all", func() {
			manager, file := signatureHelpManager(ctx, `{
				"signatures": [{"label": "greet(name)", "parameters": [{"label": "name"}]}],
				"activeSignature": 7
			}`)

			help, err := manager.SignatureHelp(ctx, "fake", file, 1, 7)
			Expect(err).NotTo(HaveOccurred())
			Expect(help.ActiveSignature).To(Equal(0))
		})
	})

	When("the server names a parameter that the active signature does not hold", func() {
		It("falls back to the first parameter, the same way as for a signature", func() {
			manager, file := signatureHelpManager(ctx, `{
				"signatures": [{"label": "greet(name)", "parameters": [{"label": "name"}]}],
				"activeParameter": 7
			}`)

			help, err := manager.SignatureHelp(ctx, "fake", file, 1, 7)
			Expect(err).NotTo(HaveOccurred())
			Expect(help.ActiveParameter).To(Equal(0))
		})
	})

	When("the server writes a signature's documentation as markup", func() {
		It("hands the agent the text, not the markup envelope around it", func() {
			manager, file := signatureHelpManager(ctx, `{
				"signatures": [{
					"label": "greet(name)",
					"documentation": {"kind": "markdown", "value": "Greet **someone**."},
					"parameters": [{"label": "name"}]
				}]
			}`)

			help, err := manager.SignatureHelp(ctx, "fake", file, 1, 7)
			Expect(err).NotTo(HaveOccurred())
			Expect(help.Signatures[0].Documentation).To(Equal("Greet **someone**."))
		})
	})

	When("the server labels a parameter with offsets into the signature label", func() {
		It("fails naming the capability, rather than reporting a blank parameter", func() {
			// Waythrough advertises no labelOffsetSupport, so a
			// spec-compliant server must send a plain string here. Reporting
			// "" for the parameter would hide the server's broken promise.
			manager, file := signatureHelpManager(ctx, `{
				"signatures": [{"label": "greet(name)", "parameters": [{"label": [6, 10]}]}]
			}`)

			_, err := manager.SignatureHelp(ctx, "fake", file, 1, 7)
			Expect(err).To(MatchError(ContainSubstring("labelOffsetSupport")))
		})
	})

	When("the server has nothing to say about the call site", func() {
		It("reports no signatures, rather than failing the call", func() {
			manager, file := signatureHelpManager(ctx, "")

			help, err := manager.SignatureHelp(ctx, "fake", file, 1, 7)
			Expect(err).NotTo(HaveOccurred())
			Expect(help.Signatures).To(BeEmpty())
		})
	})
})
