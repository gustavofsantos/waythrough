package editor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

type toolEdit struct {
	File        string `json:"file"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
	NewText     string `json:"new_text"`
}

type renameToolOutput struct {
	Edits []toolEdit `json:"edits"`
}

func callRenameTool(ctx context.Context, session *mcp.ClientSession, file string, line, column int, newName string) *mcp.CallToolResult {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "rename_symbol",
		Arguments: map[string]any{
			"file": file, "line": line, "column": column, "new_name": newName,
		},
	})
	Expect(err).NotTo(HaveOccurred(), "a tool-level failure is not a protocol error")
	return result
}

func decodeRenameOutput(result *mcp.CallToolResult) renameToolOutput {
	text, ok := result.Content[0].(*mcp.TextContent)
	Expect(ok).To(BeTrue())
	var out renameToolOutput
	Expect(json.Unmarshal([]byte(text.Text), &out)).To(Succeed())
	return out
}

var _ = Describe("rename_symbol", func() {
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

	When("the file's language server is ready and reports edits spanning multiple files", func() {
		It("syncs the requested file's current content and returns every edit, 1-based, across every affected file", func() {
			Expect(os.WriteFile(filepath.Join(root, "main.fake"), []byte("hello world"), 0o644)).To(Succeed())
			otherFile := filepath.Join(root, "other.fake")
			Expect(os.WriteFile(otherFile, []byte("hello again"), 0o644)).To(Succeed())

			cfg := fakeConfig(
				"-rename-line=0", "-rename-column=6", "-rename-length=5",
				"-rename-other-file="+otherFile,
				"-rename-other-line=2", "-rename-other-column=1", "-rename-other-length=5",
			)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			// "main.fake" is relative to root: proves path resolution joins
			// against the project root rather than passing it through raw.
			result := callRenameTool(ctx, session, "main.fake", 1, 7, "planet")
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			out := decodeRenameOutput(result)
			Expect(out.Edits).To(Equal([]toolEdit{
				{File: filepath.Join(root, "main.fake"), StartLine: 1, StartColumn: 7, EndLine: 1, EndColumn: 12, NewText: "planet"},
				{File: otherFile, StartLine: 3, StartColumn: 2, EndLine: 3, EndColumn: 7, NewText: "planet"},
			}))
		})
	})

	When("the language server declines to rename the symbol", func() {
		It("returns an empty list, not an error", func() {
			Expect(os.WriteFile(filepath.Join(root, "main.fake"), []byte("hello"), 0o644)).To(Succeed())

			cfg := fakeConfig() // no -rename-line: fakelsp declines the rename
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callRenameTool(ctx, session, "main.fake", 1, 1, "renamed")
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })
			Expect(decodeRenameOutput(result).Edits).To(BeEmpty())
		})
	})

	When("the new name is empty", func() {
		It("returns a tool error, not a rename that blanks out the symbol", func() {
			Expect(os.WriteFile(filepath.Join(root, "main.fake"), []byte("hello"), 0o644)).To(Succeed())

			cfg := fakeConfig("-rename-line=0", "-rename-column=0", "-rename-length=5")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callRenameTool(ctx, session, "main.fake", 1, 1, "")
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("new_name"))
		})
	})
})
