package editor_test

import (
	"context"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

var _ = Describe("list_references", func() {
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

	When("the file's language server is ready and reports references", func() {
		It("syncs the file's current content and returns every location, 1-based", func() {
			writeFile(root, "main.fake", "hello world")

			cfg := fakeConfig("-references-line=2", "-references-column=1", "-references-count=2")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "list_references", "main.fake", 1, 1)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			out := decodeOutput(result)
			Expect(out.Locations).To(Equal([]toolLocation{
				{File: filepath.Join(root, "main.fake"), Line: 3, Column: 2},
				{File: filepath.Join(root, "main.fake"), Line: 4, Column: 2},
			}))
		})
	})

	When("the language server reports no references for the position", func() {
		It("returns an empty list, not an error", func() {
			writeFile(root, "main.fake", "hello")

			cfg := fakeConfig() // no -references-line: fakelsp reports no references
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "list_references", "main.fake", 1, 1)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })
			Expect(decodeOutput(result).Locations).To(BeEmpty())
		})
	})

	When("the file's extension has no configured language server", func() {
		It("returns a tool error naming the extension", func() {
			cfg := fakeConfig()
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "list_references", "main.unknown", 1, 1)
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring(".unknown"))
		})
	})

	When("the file's language server keeps failing to start", func() {
		It("returns a tool error naming the server, not an empty result", func() {
			cfg := fakeConfig("-crash")
			manager := lsp.NewManager(root, cfg.LanguageServers,
				lsp.WithRestartLimit(2, time.Minute),
				lsp.WithToolCallTimeout(5*time.Second))
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "list_references", "main.fake", 1, 1)
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("fake"))
		})
	})
})
