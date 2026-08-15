package editor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/editor"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

type toolLocation struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

type toolOutput struct {
	Locations []toolLocation `json:"locations"`
}

// connect starts manager, builds the editor MCP server on top of it, and
// connects a client to it over an in-memory transport — a real MCP session,
// with no subprocess or stdio involved on the client/server wire.
func connect(ctx context.Context, manager *lsp.Manager, cfg config.Config) *mcp.ClientSession {
	Expect(manager.Start(ctx)).To(Succeed())

	server := editor.New(manager, cfg)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	_, err := server.Connect(ctx, serverTransport, nil)
	Expect(err).NotTo(HaveOccurred())

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	Expect(err).NotTo(HaveOccurred())

	DeferCleanup(func() { _ = session.Close() })
	return session
}

func callTool(
	ctx context.Context, session *mcp.ClientSession, name, file string, line, column int,
) *mcp.CallToolResult {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: map[string]any{"file": file, "line": line, "column": column},
	})
	Expect(err).NotTo(HaveOccurred(), "a tool-level failure is not a protocol error")
	return result
}

func decodeOutput(result *mcp.CallToolResult) toolOutput {
	text, ok := result.Content[0].(*mcp.TextContent)
	Expect(ok).To(BeTrue())
	var out toolOutput
	Expect(json.Unmarshal([]byte(text.Text), &out)).To(Succeed())
	return out
}

// writeFile creates name under root with content and returns its full path,
// the fixture every editor spec needs before it can ask about a position.
func writeFile(root, name, content string) string {
	path := filepath.Join(root, name)
	Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
	return path
}

func errorText(result *mcp.CallToolResult) string {
	text, ok := result.Content[0].(*mcp.TextContent)
	Expect(ok).To(BeTrue())
	return text.Text
}

// fakeConfig is a single-entry config naming a "fake" server backed by
// fakelspPath for the ".fake" extension, with handshake readiness — the
// shape every get_definition scenario below needs, save for the flags
// passed to fakelsp.
func fakeConfig(args ...string) config.Config {
	return config.Config{LanguageServers: []config.LanguageServer{{
		Name:      "fake",
		Command:   fakelspPath,
		Args:      args,
		Readiness: config.ReadinessHandshake,
		Filetypes: map[string]string{".fake": "fake"},
	}}}
}

var _ = Describe("get_definition", func() {
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

	When("the file's language server is ready and reports a definition", func() {
		It("syncs the file's current content and returns the location, 1-based", func() {
			writeFile(root, "main.fake", "hello world")

			cfg := fakeConfig("-definition-line=4", "-definition-column=2")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			// "main.fake" is relative to root: proves path resolution joins
			// against the project root rather than passing it through raw.
			result := callTool(ctx, session, "get_definition", "main.fake", 1, 1)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			out := decodeOutput(result)
			Expect(out.Locations).To(Equal([]toolLocation{
				{File: filepath.Join(root, "main.fake"), Line: 5, Column: 3},
			}))
		})
	})

	When("the language server reports no definition for the position", func() {
		It("returns an empty result, not an error", func() {
			writeFile(root, "main.fake", "hello")

			cfg := fakeConfig() // no -definition-line: fakelsp reports no definition
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "get_definition", "main.fake", 1, 1)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })
			Expect(decodeOutput(result).Locations).To(BeEmpty())
		})
	})

	When("the line is less than 1", func() {
		It("returns a tool error stating that line and column are 1-based", func() {
			writeFile(root, "main.fake", "hello")

			cfg := fakeConfig()
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "get_definition", "main.fake", 0, 1)
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("1-based"))
		})
	})

	When("the column is less than 1", func() {
		It("returns a tool error stating that line and column are 1-based", func() {
			writeFile(root, "main.fake", "hello")

			cfg := fakeConfig()
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "get_definition", "main.fake", 1, 0)
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("1-based"))
		})
	})

	When("the file's extension has no configured language server", func() {
		It("returns a tool error naming the extension", func() {
			cfg := fakeConfig()
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callTool(ctx, session, "get_definition", "main.unknown", 1, 1)
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

			result := callTool(ctx, session, "get_definition", "main.fake", 1, 1)
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring("fake"))
		})
	})
})
