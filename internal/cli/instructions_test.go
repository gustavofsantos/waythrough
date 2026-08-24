package cli_test

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/cli"
	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/editor"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// listToolsTimeout bounds the in-memory MCP handshake below, so a session
// that never answers fails this spec instead of hanging the suite.
const listToolsTimeout = 10 * time.Second

// backtickedName matches one `tool_name` in the printed block. The block
// backticks tool names and nothing else, which is what lets a spec read the
// set of tools it claims without hard-coding that set a second time.
var backtickedName = regexp.MustCompile("`([a-z_]+)`")

// advertisedToolNames asks a live editor MCP server which tools it
// registers, over an in-memory transport. It starts no language server:
// listing tools never reaches one, so the manager here is built and left
// unstarted on purpose.
func advertisedToolNames(ctx context.Context) []string {
	manager := lsp.NewManager(GinkgoT().TempDir(), config.Default().LanguageServers)
	server := editor.New(manager, config.Default(), slog.New(slog.DiscardHandler))

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := server.Connect(ctx, serverTransport, nil)
	Expect(err).NotTo(HaveOccurred())

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = session.Close() })

	result, err := session.ListTools(ctx, nil)
	Expect(err).NotTo(HaveOccurred())

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// claimedToolNames reads back the tool names the printed block names, in
// the form an agent would act on: a backticked identifier.
func claimedToolNames(instructions string) []string {
	matches := backtickedName.FindAllStringSubmatch(instructions, -1)

	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

var _ = Describe("waythrough instructions", func() {
	var (
		stdout *bytes.Buffer
		stderr *bytes.Buffer
	)

	BeforeEach(func() {
		stdout = &bytes.Buffer{}
		stderr = &bytes.Buffer{}
	})

	It("prints the block on stdout and exits zero", func() {
		Expect(cli.Execute([]string{"instructions"}, stdout, stderr)).To(Equal(0))
		Expect(stderr.String()).To(BeEmpty())
		Expect(stdout.String()).To(HaveSuffix("\n"))
	})

	It("tells the agent to prefer the tools over searching text", func() {
		Expect(cli.Execute([]string{"instructions"}, stdout, stderr)).To(Equal(0))
		Expect(stdout.String()).To(ContainSubstring("grep"))
	})

	It("takes no config flag, so it works before a project has a config file", func() {
		Expect(cli.Execute([]string{"instructions", "--config", "/nonexistent.yaml"},
			stdout, stderr)).NotTo(Equal(0))
		Expect(cli.Execute([]string{"instructions"}, stdout, stderr)).To(Equal(0))
		Expect(stdout.String()).NotTo(BeEmpty())
	})

	It("rejects arguments rather than ignoring them", func() {
		Expect(cli.Execute([]string{"instructions", "extra"}, stdout, stderr)).NotTo(Equal(0))
		Expect(stdout.String()).To(BeEmpty())
	})

	// The block is what a coding agent acts on, so a name in it that no
	// server registers sends the agent after a call that can only fail, and
	// a tool missing from it stays invisible. Both directions are checked
	// against the live tool list rather than a copy, because a copy would
	// drift with the block it is meant to catch.
	It("names every registered MCP tool, and no other tool", func() {
		ctx, cancel := context.WithTimeout(context.Background(), listToolsTimeout)
		DeferCleanup(cancel)

		Expect(cli.Execute([]string{"instructions"}, stdout, stderr)).To(Equal(0))

		advertised := advertisedToolNames(ctx)
		Expect(advertised).NotTo(BeEmpty())

		claimed := claimedToolNames(stdout.String())
		for _, name := range advertised {
			Expect(claimed).To(ContainElement(name), "tool missing from the instructions")
		}
		for _, name := range claimed {
			// Argument names are backticked too, and name no tool.
			if isPositionArgument(name) {
				continue
			}
			Expect(advertised).To(ContainElement(name), "instructions name an unknown tool")
		}
	})
})

// isPositionArgument reports whether a backticked name in the block is one
// of the position fields every tool takes, rather than a tool name. Those
// fields are spelled out for the agent in the same backticked form, so the
// drift check has to tell the two apart.
func isPositionArgument(name string) bool {
	switch strings.ToLower(name) {
	case "file", "line", "column":
		return true
	default:
		return false
	}
}
