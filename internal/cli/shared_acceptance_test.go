package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// sharedWorkspace is one user's setup for --shared specs: a home holding the
// configuration, a private runtime directory, and a workspace with one file
// the fake language server answers questions about.
type sharedWorkspace struct {
	home        string
	runtimeDir  string
	root        string
	file        string
	instanceLog string
}

func newSharedWorkspace() sharedWorkspace {
	// The runtime directory is made with MkdirTemp rather than Ginkgo's
	// TempDir, whose spec-named paths can push a socket past sun_path.
	runtimeDir, err := os.MkdirTemp("", "wtx")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = os.RemoveAll(runtimeDir) })

	workspace := sharedWorkspace{
		home:        GinkgoT().TempDir(),
		runtimeDir:  runtimeDir,
		root:        GinkgoT().TempDir(),
		instanceLog: filepath.Join(GinkgoT().TempDir(), "instances.log"),
	}
	workspace.file = filepath.Join(workspace.root, "main.fake")
	Expect(os.WriteFile(workspace.file, []byte("hello world"), 0o644)).To(Succeed())
	workspace.writeConfig("")
	return workspace
}

// writeConfig writes a configuration whose one server is fakelsp. comment
// changes the file's bytes, and so its workspace key, without changing
// what it configures.
func (w sharedWorkspace) writeConfig(comment string) {
	content := fmt.Sprintf(`# %s
language_servers:
  - name: fake
    command: %s
    args: ["-instance-log=%s", "-definition-line=4", "-definition-column=2"]
    readiness: handshake
    filetypes:
      .fake: fake
`, comment, fakelspPath, w.instanceLog)
	Expect(os.WriteFile(filepath.Join(w.home, ".waythrough.yaml"), []byte(content), 0o600)).
		To(Succeed())
}

// agentSession is one `waythrough serve --shared`, run the way a coding
// agent runs it: as a child process speaking MCP over its stdio.
type agentSession struct {
	command *exec.Cmd
	session *mcp.ClientSession
}

func (w sharedWorkspace) startSession(linger time.Duration) agentSession {
	command := exec.CommandContext(context.Background(), waythroughPath,
		"serve", "--shared", "--linger="+linger.String())
	command.Dir = w.root
	command.Env = []string{
		"HOME=" + w.home,
		"XDG_RUNTIME_DIR=" + w.runtimeDir,
		"PATH=" + os.Getenv("PATH"),
	}
	command.Stderr = GinkgoWriter

	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0"}, nil)
	session, err := client.Connect(
		context.Background(), &mcp.CommandTransport{Command: command}, nil)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = session.Close() })
	return agentSession{command: command, session: session}
}

// definitionLine asks for the definition at the start of the workspace file
// and returns the 1-based line of the answer.
func (s agentSession) definitionLine(file string) int {
	result, err := s.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_definition",
		Arguments: map[string]any{"file": file, "line": 1, "column": 1},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(result.IsError).To(BeFalse(), func() string {
		return result.Content[0].(*mcp.TextContent).Text
	})

	var output struct {
		Locations []struct {
			Line int `json:"line"`
		} `json:"locations"`
	}
	text := result.Content[0].(*mcp.TextContent).Text
	Expect(json.Unmarshal([]byte(text), &output)).To(Succeed())
	Expect(output.Locations).To(HaveLen(1))
	return output.Locations[0].Line
}

// languageServerPIDs reads the pid of every fakelsp process ever started for
// this workspace, in start order.
func (w sharedWorkspace) languageServerPIDs() []int {
	data, err := os.ReadFile(w.instanceLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	Expect(err).NotTo(HaveOccurred())
	var pids []int
	for _, line := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(line)
		Expect(err).NotTo(HaveOccurred())
		pids = append(pids, pid)
	}
	return pids
}

func (w sharedWorkspace) sockets() []string {
	sockets, err := filepath.Glob(filepath.Join(w.runtimeDir, "waythrough", "*.sock"))
	Expect(err).NotTo(HaveOccurred())
	return sockets
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

var _ = Describe("serve --shared", func() {
	It("answers every session in a workspace from one language server", func() {
		workspace := newSharedWorkspace()
		first := workspace.startSession(200 * time.Millisecond)
		second := workspace.startSession(200 * time.Millisecond)

		Expect(first.definitionLine(workspace.file)).To(Equal(5))
		Expect(second.definitionLine(workspace.file)).To(Equal(5))
		Expect(workspace.languageServerPIDs()).To(HaveLen(1))
	})

	It("starts one daemon and one language server for concurrent cold sessions", func() {
		workspace := newSharedWorkspace()
		const sessionCount = 8

		var started sync.WaitGroup
		sessions := make([]agentSession, sessionCount)
		for index := range sessions {
			started.Add(1)
			go func() {
				defer GinkgoRecover()
				defer started.Done()
				sessions[index] = workspace.startSession(200 * time.Millisecond)
			}()
		}
		started.Wait()

		for _, session := range sessions {
			Expect(session.definitionLine(workspace.file)).To(Equal(5))
		}
		Expect(workspace.languageServerPIDs()).To(HaveLen(1))
		Expect(workspace.sockets()).To(HaveLen(1))
	})

	It("stops the language server once the last session has been gone for the linger", func() {
		workspace := newSharedWorkspace()
		first := workspace.startSession(300 * time.Millisecond)
		second := workspace.startSession(300 * time.Millisecond)
		Expect(first.definitionLine(workspace.file)).To(Equal(5))
		server := workspace.languageServerPIDs()[0]

		Expect(first.session.Close()).To(Succeed())
		Consistently(func() bool { return processAlive(server) }, time.Second).
			Should(BeTrue(), "a session is still connected")
		Expect(second.definitionLine(workspace.file)).To(Equal(5))

		Expect(second.session.Close()).To(Succeed())
		Eventually(func() bool { return processAlive(server) }, 15*time.Second).
			Should(BeFalse())
		Eventually(workspace.sockets, 5*time.Second).Should(BeEmpty())
	})

	It("counts a killed session as gone", func() {
		workspace := newSharedWorkspace()
		session := workspace.startSession(100 * time.Millisecond)
		Expect(session.definitionLine(workspace.file)).To(Equal(5))
		server := workspace.languageServerPIDs()[0]

		Expect(session.command.Process.Kill()).To(Succeed())
		Eventually(func() bool { return processAlive(server) }, 15*time.Second).
			Should(BeFalse())
	})

	It("starts a new daemon for a session after the previous one stopped", func() {
		workspace := newSharedWorkspace()
		first := workspace.startSession(0)
		Expect(first.definitionLine(workspace.file)).To(Equal(5))
		Expect(first.session.Close()).To(Succeed())
		Eventually(workspace.sockets, 15*time.Second).Should(BeEmpty())

		second := workspace.startSession(0)
		Expect(second.definitionLine(workspace.file)).To(Equal(5))
		Expect(workspace.languageServerPIDs()).To(HaveLen(2))
	})
})
