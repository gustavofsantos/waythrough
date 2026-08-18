package editor_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// recordingLogger is the logger --debug builds, pointed at a buffer. A
// session's records all arrive on the goroutine handling the call, and
// every spec here reads the buffer only after that call has returned.
func recordingLogger() (*slog.Logger, *bytes.Buffer) {
	var recorded bytes.Buffer
	handler := slog.NewTextHandler(&recorded, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(handler), &recorded
}

var _ = Describe("what the MCP server records about a tool call", func() {
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

	// This is the whole point of --debug: an agent's tool call, the answer
	// it got, and what it cost. Anything less cannot say whether Waythrough
	// is worth the place it takes in an agent's tool list.
	When("a tool answers", func() {
		It("records the tool, its arguments, the answer, and how long it took", func() {
			writeFile(root, "main.fake", "hello world")

			logger, recorded := recordingLogger()
			cfg := fakeConfig("-definition-line=4", "-definition-column=2")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connectLogging(ctx, manager, cfg, logger)

			result := callTool(ctx, session, "get_definition", "main.fake", 1, 1)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			logs := recorded.String()
			Expect(logs).To(ContainSubstring("mcp request received"))
			Expect(logs).To(ContainSubstring("mcp request answered"))
			Expect(logs).To(ContainSubstring("tool=get_definition"))
			Expect(logs).To(ContainSubstring(`\"line\":1`),
				"the arguments are recorded as the agent sent them")
			Expect(logs).To(ContainSubstring("outcome=ok"))
			Expect(logs).To(ContainSubstring("duration_ms="))
			Expect(logs).To(ContainSubstring("locations"),
				"a call that answered with nothing and one that answered with a "+
					"location are the two cases a reader must tell apart")
		})
	})

	When("a tool refuses the call", func() {
		It("records the refusal as its own outcome, not as a failure to answer", func() {
			logger, recorded := recordingLogger()
			cfg := fakeConfig()
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connectLogging(ctx, manager, cfg, logger)

			result := callTool(ctx, session, "get_definition", "main.unknown", 1, 1)
			Expect(result.IsError).To(BeTrue())

			logs := recorded.String()
			Expect(logs).To(ContainSubstring("outcome=tool_error"))
			Expect(logs).To(ContainSubstring(".unknown"),
				"the reason the tool gave the agent is the reason worth recording")
		})
	})

	// A language server is free to answer a reference query with as much as
	// it likes, so the record's size cannot be left for it to decide.
	When("a tool answers with more than one record may carry", func() {
		It("cuts the answer at the cap and says that it cut", func() {
			writeFile(root, "main.fake", strings.Repeat("hello world\n", 400))

			logger, recorded := recordingLogger()
			cfg := fakeConfig("-references-line=0", "-references-count=400")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connectLogging(ctx, manager, cfg, logger)

			result := callTool(ctx, session, "list_references", "main.fake", 1, 1)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })

			logs := recorded.String()
			Expect(logs).To(ContainSubstring("[truncated]"))
			Expect(len(logs)).To(BeNumerically("<", 8192),
				"the cap is what keeps one answer from filling the log")
		})
	})

	When("the logger has no debug handler", func() {
		It("records nothing, and the tool answers as it always did", func() {
			writeFile(root, "main.fake", "hello world")

			var recorded bytes.Buffer
			handler := slog.NewTextHandler(&recorded, &slog.HandlerOptions{Level: slog.LevelWarn})

			cfg := fakeConfig("-definition-line=4", "-definition-column=2")
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connectLogging(ctx, manager, cfg, slog.New(handler))

			result := callTool(ctx, session, "get_definition", "main.fake", 1, 1)
			Expect(result.IsError).To(BeFalse(), func() string { return errorText(result) })
			Expect(decodeToolOutput[toolOutput](result).Locations).To(HaveLen(1))

			Expect(recorded.String()).To(BeEmpty())
		})
	})
})
