package editor_test

import (
	"context"
	"encoding/json"
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/lsp"
	"github.com/gustavofsantos/waythrough/internal/status"
)

const statusAppURI = "ui://waythrough/status"

func getStatus(ctx context.Context, session *mcp.ClientSession) status.Report {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_status"})
	Expect(err).NotTo(HaveOccurred())
	Expect(result.IsError).To(BeFalse())
	return decodeToolOutput[status.Report](result)
}

func advertisedTool(ctx context.Context, session *mcp.ClientSession, name string) *mcp.Tool {
	tools, err := session.ListTools(ctx, nil)
	Expect(err).NotTo(HaveOccurred())
	for _, tool := range tools.Tools {
		if tool.Name == name {
			return tool
		}
	}
	Fail("no tool named " + name)
	return nil
}

// connectIdle connects to an editor whose one server no request has
// started, for the specs about the tool rather than about a server.
func connectIdle(ctx context.Context, root string) *mcp.ClientSession {
	cfg := fakeConfig()
	return connect(ctx, lsp.NewManager(root, cfg.LanguageServers, lsp.WithDemandStart()), cfg)
}

var _ = Describe("get_status", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		root   string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		root = checkoutDirectory()
	})

	AfterEach(func() {
		cancel()
	})

	It("reports each language server, and the requests it has served", func() {
		file := writeFile(root, "main.fake", "hello world")
		cfg := fakeConfig("-definition-line=0")
		manager := lsp.NewManager(root, cfg.LanguageServers, lsp.WithDemandStart())
		session := connect(ctx, manager, cfg)

		before := getStatus(ctx, session)
		Expect(before.Format).To(Equal(status.Format))
		Expect(before.Mode).To(Equal(status.ModeStandalone))
		Expect(before.Sessions).To(BeNil(), "a standalone serve has no sessions to report")
		Expect(before.LanguageServers).To(HaveLen(1))
		Expect(before.LanguageServers[0].Status).To(Equal("idle"))

		callTool(ctx, session, "get_definition", file, 1, 1)

		after := getStatus(ctx, session)
		Expect(after.Health).To(Equal(lsp.HealthHealthy))
		server := after.LanguageServers[0]
		Expect(server.Name).To(Equal("fake"))
		Expect(server.Root).To(Equal(root))
		Expect(server.Status).To(Equal("ready"))
		Expect(server.Requests.Total).To(Equal(uint64(1)))
	})

	It("is read-only, and points a host that renders MCP Apps to its page", func() {
		session := connectIdle(ctx, root)

		tool := advertisedTool(ctx, session, "get_status")
		Expect(tool.Annotations).NotTo(BeNil())
		Expect(tool.Annotations.ReadOnlyHint).To(BeTrue())
		Expect(tool.Meta).To(HaveKeyWithValue("ui",
			HaveKeyWithValue("resourceUri", statusAppURI)))
		Expect(tool.Meta).To(HaveKeyWithValue("ui/resourceUri", statusAppURI))
		Expect(tool.OutputSchema).NotTo(BeNil(),
			"an agent reads the report as structured content")
	})

	It("serves its page as a self-contained MCP App", func() {
		session := connectIdle(ctx, root)

		resources, err := session.ListResources(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resources.Resources).To(ContainElement(
			HaveField("URI", statusAppURI)))

		read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: statusAppURI})
		Expect(err).NotTo(HaveOccurred())
		Expect(read.Contents).To(HaveLen(1))
		page := read.Contents[0]
		Expect(page.MIMEType).To(Equal("text/html;profile=mcp-app"))
		Expect(page.Text).To(HavePrefix("<!doctype html>"))
		Expect(page.Text).To(ContainSubstring("ui/initialize"))
		// A host renders the page under a content security policy that, by
		// default, allows no network origin, so the page must load nothing.
		Expect(page.Text).NotTo(MatchRegexp(`https?://`))
		// Every report value is written as text. Markup built from a path or
		// a language server's error message would run whatever it carried.
		Expect(regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write`).
			MatchString(page.Text)).To(BeFalse())
	})

	It("answers with the report an agent and the page can both decode", func() {
		session := connectIdle(ctx, root)

		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_status"})
		Expect(err).NotTo(HaveOccurred())
		structured, err := json.Marshal(result.StructuredContent)
		Expect(err).NotTo(HaveOccurred())
		Expect(structured).To(MatchJSON(result.Content[0].(*mcp.TextContent).Text))
	})
})
