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
	workspace.writeConfig("first")
	return workspace
}

// writeConfig writes a configuration whose one server is fakelsp, run with
// any extra flags. comment changes the file's bytes, and so its workspace
// key, without changing what it configures.
func (w sharedWorkspace) writeConfig(comment string, extraFlags ...string) {
	flags := []string{
		"-instance-log=" + w.instanceLog, "-definition-line=4", "-definition-column=2",
	}
	flags = append(flags, extraFlags...)
	quoted, err := json.Marshal(flags)
	Expect(err).NotTo(HaveOccurred())
	content := fmt.Sprintf(`# %s
language_servers:
  - name: fake
    command: %s
    args: %s
    readiness: handshake
    filetypes:
      .fake: fake
`, comment, fakelspPath, quoted)
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
	return w.startServe("--shared", "--linger="+linger.String())
}

// startServe runs `waythrough serve` with serveArgs as an agent would, and
// connects to it.
func (w sharedWorkspace) startServe(serveArgs ...string) agentSession {
	command := w.serveCommand(serveArgs...)
	command.Stderr = GinkgoWriter

	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0"}, nil)
	session, err := client.Connect(
		context.Background(), &mcp.CommandTransport{Command: command}, nil)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = session.Close() })
	return agentSession{command: command, session: session}
}

// serveCommand is `waythrough serve` with serveArgs, in the workspace and
// with this user's environment, not yet started.
func (w sharedWorkspace) serveCommand(serveArgs ...string) *exec.Cmd {
	command := exec.CommandContext(context.Background(), waythroughPath,
		append([]string{"serve"}, serveArgs...)...)
	command.Dir = w.root
	command.Env = []string{
		"HOME=" + w.home,
		"XDG_RUNTIME_DIR=" + w.runtimeDir,
		"PATH=" + os.Getenv("PATH"),
	}
	return command
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

// daemonPIDs reads the pid each daemon recorded in its lock file.
func (w sharedWorkspace) daemonPIDs() []int {
	locks, err := filepath.Glob(filepath.Join(w.runtimeDir, "waythrough", "*.lock"))
	Expect(err).NotTo(HaveOccurred())
	var pids []int
	for _, lock := range locks {
		data, err := os.ReadFile(lock)
		Expect(err).NotTo(HaveOccurred())
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		Expect(err).NotTo(HaveOccurred())
		pids = append(pids, pid)
	}
	return pids
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

	It("serves a session that arrives during a slow drain from the next daemon", func() {
		workspace := newSharedWorkspace()
		// A server that ignores exit holds the drain for the whole kill
		// grace, which is the longest a drain takes.
		workspace.writeConfig("slow drain", "-ignore-exit")
		first := workspace.startSession(0)
		Expect(first.definitionLine(workspace.file)).To(Equal(5))
		draining := workspace.languageServerPIDs()[0]

		Expect(first.session.Close()).To(Succeed())
		Eventually(workspace.sockets, 5*time.Second).Should(BeEmpty())
		Expect(processAlive(draining)).To(BeTrue(), "the drain should still be running")

		second := workspace.startSession(0)
		Expect(second.definitionLine(workspace.file)).To(Equal(5))
		Expect(processAlive(draining)).To(BeFalse(),
			"the next daemon may start its server only after its predecessor drained")
		Expect(workspace.languageServerPIDs()).To(HaveLen(2))
	})

	It("recovers from a daemon that was killed and left its socket behind", func() {
		workspace := newSharedWorkspace()
		first := workspace.startSession(time.Hour)
		Expect(first.definitionLine(workspace.file)).To(Equal(5))
		daemons := workspace.daemonPIDs()
		Expect(daemons).To(HaveLen(1))

		Expect(syscall.Kill(daemons[0], syscall.SIGKILL)).To(Succeed())
		Eventually(func() bool { return processAlive(daemons[0]) }, 5*time.Second).
			Should(BeFalse())
		Expect(workspace.sockets()).To(HaveLen(1), "SIGKILL leaves the socket file")

		second := workspace.startSession(0)
		Expect(second.definitionLine(workspace.file)).To(Equal(5))
		Expect(workspace.daemonPIDs()).NotTo(ContainElement(daemons[0]))
	})

	It("gives an edited configuration its own daemon, and retires the old one", func() {
		workspace := newSharedWorkspace()
		before := workspace.startSession(0)
		Expect(before.definitionLine(workspace.file)).To(Equal(5))
		oldServer := workspace.languageServerPIDs()[0]

		workspace.writeConfig("edited")
		after := workspace.startSession(0)
		Expect(after.definitionLine(workspace.file)).To(Equal(5))
		pids := workspace.languageServerPIDs()
		Expect(pids).To(HaveLen(2), "no session may attach to servers from a stale configuration")
		Expect(workspace.sockets()).To(HaveLen(2))

		Expect(before.session.Close()).To(Succeed())
		Eventually(func() bool { return processAlive(oldServer) }, 15*time.Second).
			Should(BeFalse())
		Expect(processAlive(pids[1])).To(BeTrue())
		Expect(after.definitionLine(workspace.file)).To(Equal(5))
	})
})
