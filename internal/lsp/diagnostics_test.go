package lsp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// diagnosticsManager starts a manager whose "fake" server runs with args,
// and returns it with the path of the file every spec below asks about.
func diagnosticsManager(ctx context.Context, args ...string) (*lsp.Manager, string) {
	root := GinkgoT().TempDir()
	file := filepath.Join(root, "main.fake")
	Expect(os.WriteFile(file, []byte("greet()"), 0o644)).To(Succeed())

	entry := config.LanguageServer{
		Name:      "fake",
		Command:   fakelspPath,
		Args:      args,
		Readiness: config.ReadinessHandshake,
		Filetypes: map[string]string{".fake": "fake"},
	}

	manager := lsp.NewManager(root, []config.LanguageServer{entry})
	Expect(manager.Start(ctx)).To(Succeed())
	Expect(manager.WaitReady(ctx, "fake", time.Second)).To(Succeed())
	return manager, file
}

func readRequestLog(path string) []string {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

var _ = Describe("Manager.Diagnostics", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
	})

	AfterEach(func() { cancel() })

	When("the server's own initialize result never advertised pull diagnostics", func() {
		It("refuses without asking, because the server never promised an answer", func() {
			requestLog := filepath.Join(GinkgoT().TempDir(), "requests.log")
			// The fake would answer if asked. Only the missing capability
			// stands between Waythrough and that answer.
			manager, file := diagnosticsManager(ctx,
				"-request-log="+requestLog,
				"-diagnostics="+`{"kind":"full","items":[]}`)

			_, err := manager.Diagnostics(ctx, "fake", file)
			Expect(err).To(MatchError(ContainSubstring(`"fake"`)))
			Expect(err).To(MatchError(ContainSubstring("pull diagnostics")))

			Expect(readRequestLog(requestLog)).NotTo(ContainElement("textDocument/diagnostic"),
				"the capability check must answer on its own, without spending a round trip")
		})
	})

	When("the server finds nothing wrong with the file", func() {
		It("reports a clean file, rather than failing the call", func() {
			manager, file := diagnosticsManager(ctx, "-pull-diagnostics")

			diagnostics, err := manager.Diagnostics(ctx, "fake", file)
			Expect(err).NotTo(HaveOccurred())
			Expect(diagnostics).To(BeEmpty())
		})
	})

	When("the server leaves out a diagnostic's severity, source and code", func() {
		It("leaves each one blank, rather than inventing a severity", func() {
			manager, file := diagnosticsManager(ctx, "-pull-diagnostics", "-diagnostics="+`{
				"kind": "full",
				"items": [{
					"range": {
						"start": {"line": 0, "character": 0},
						"end": {"line": 0, "character": 5}
					},
					"message": "something is wrong"
				}]
			}`)

			diagnostics, err := manager.Diagnostics(ctx, "fake", file)
			Expect(err).NotTo(HaveOccurred())
			Expect(diagnostics).To(Equal([]lsp.Diagnostic{{
				StartLine: 1, StartColumn: 1,
				EndLine: 1, EndColumn: 6,
				Message: "something is wrong",
			}}))
		})
	})

	When("the server sends a numbered code and a message written as markup", func() {
		It("hands the agent text for both, since an agent reads text", func() {
			manager, file := diagnosticsManager(ctx, "-pull-diagnostics", "-diagnostics="+`{
				"kind": "full",
				"items": [{
					"range": {
						"start": {"line": 0, "character": 0},
						"end": {"line": 0, "character": 5}
					},
					"severity": 3,
					"code": 2304,
					"message": {"kind": "markdown", "value": "cannot find name **greet**"}
				}]
			}`)

			diagnostics, err := manager.Diagnostics(ctx, "fake", file)
			Expect(err).NotTo(HaveOccurred())
			Expect(diagnostics[0].Code).To(Equal("2304"))
			Expect(diagnostics[0].Message).To(Equal("cannot find name **greet**"))
			Expect(diagnostics[0].Severity).To(Equal("information"))
		})
	})

	When("the server answers that its last report still stands", func() {
		It("fails, because Waythrough asked for a fresh report and holds no last one", func() {
			// Waythrough never sends a previousResultId, so a server may not
			// send this. Treating it as a clean file would hide real problems.
			manager, file := diagnosticsManager(ctx, "-pull-diagnostics",
				"-diagnostics="+`{"kind":"unchanged","resultId":"1"}`)

			_, err := manager.Diagnostics(ctx, "fake", file)
			Expect(err).To(MatchError(ContainSubstring("previousResultId")))
		})
	})
})
