package editor_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// restartToolOutput mirrors what restart_server answers: which server it
// restarted, and the state that server is in by the time the call returns.
type restartToolOutput struct {
	Server string `json:"server"`
	Status string `json:"status"`
}

// callRestartTool calls restart_server, which takes a server name and
// nothing else: a restart acts on a whole language server, not on a file.
func callRestartTool(
	ctx context.Context, session *mcp.ClientSession, server string,
) *mcp.CallToolResult {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "restart_server",
		Arguments: map[string]any{"server": server},
	})
	Expect(err).NotTo(HaveOccurred(), "a tool-level failure is not a protocol error")
	return result
}

// syncedDocuments reads the didOpen/didChange notifications fakelsp's
// -sync-log recorded, as "method@version". That pair is what tells a fresh
// server session, which learns a document through didOpen at version 1,
// from a session that already held the document and only saw it change.
func syncedDocuments(path string) []string {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())

	var synced []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event struct {
			Method  string `json:"method"`
			Version int32  `json:"version"`
		}
		Expect(json.Unmarshal([]byte(line), &event)).To(Succeed())
		synced = append(synced, event.Method+"@"+strconv.Itoa(int(event.Version)))
	}
	return synced
}

var _ = Describe("restart_server", func() {
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

	When("an agent restarts a language server it is already using", func() {
		It("answers the next question from a new server session, not the old one", func() {
			writeFile(root, "main.fake", "hello world")
			syncLog := filepath.Join(GinkgoT().TempDir(), "sync.log")

			cfg := fakeConfig("-definition-line=4", "-definition-column=2", "-sync-log="+syncLog)
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			before := callTool(ctx, session, "get_definition", "main.fake", 1, 1)
			Expect(before.IsError).To(BeFalse(), func() string { return errorText(before) })

			restart := callRestartTool(ctx, session, "fake")
			Expect(restart.IsError).To(BeFalse(), func() string { return errorText(restart) })
			Expect(decodeToolOutput[restartToolOutput](restart)).To(Equal(restartToolOutput{
				Server: "fake",
				Status: "ready",
			}), "the call must not return until the replacement can answer")

			after := callTool(ctx, session, "get_definition", "main.fake", 1, 1)
			Expect(after.IsError).To(BeFalse(), func() string { return errorText(after) })
			Expect(decodeToolOutput[toolOutput](after).Locations).To(Equal([]toolLocation{
				{File: filepath.Join(root, "main.fake"), Line: 5, Column: 3},
			}))

			// fakelsp refuses a question about a document it was never told
			// about, so a second didOpen at version 1 is the reading that
			// says a different session answered: the replacement learned the
			// file from scratch, rather than being sent a didChange on top of
			// a version only the retired session ever held.
			Expect(syncedDocuments(syncLog)).To(Equal([]string{
				"textDocument/didOpen@1",
				"textDocument/didOpen@1",
			}))
		})
	})

	When("an agent names a language server that is not configured", func() {
		It("says which names it could have used, so the next call can work", func() {
			cfg := fakeConfig()
			manager := lsp.NewManager(root, cfg.LanguageServers)
			session := connect(ctx, manager, cfg)

			result := callRestartTool(ctx, session, "gopls")
			Expect(result.IsError).To(BeTrue())
			Expect(errorText(result)).To(ContainSubstring(`"gopls"`))
			Expect(errorText(result)).To(ContainSubstring("fake"))
		})
	})
})
