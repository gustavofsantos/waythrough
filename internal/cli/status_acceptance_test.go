package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// statusJSON is the part of `waythrough status --json` these specs read.
type statusJSON struct {
	Daemons []struct {
		PID      int    `json:"pid"`
		Root     string `json:"root"`
		Health   string `json:"health"`
		Sessions struct {
			State         string `json:"state"`
			Active        int    `json:"active"`
			AdmittedTotal int    `json:"admitted_total"`
		} `json:"sessions"`
		LanguageServers []struct {
			Name     string `json:"name"`
			Root     string `json:"root"`
			Status   string `json:"status"`
			PID      int    `json:"pid"`
			Requests struct {
				Total int `json:"total"`
			} `json:"requests"`
		} `json:"language_servers"`
	} `json:"daemons"`
	Unreachable []struct {
		Stale bool `json:"stale"`
	} `json:"unreachable"`
}

// status runs `waythrough status` as the workspace's user would, from any
// directory, and returns its stdout.
func (w sharedWorkspace) status(args ...string) string {
	command := exec.CommandContext(context.Background(), waythroughPath,
		append([]string{"status"}, args...)...)
	command.Env = []string{
		"HOME=" + w.home,
		"XDG_RUNTIME_DIR=" + w.runtimeDir,
		"PATH=" + os.Getenv("PATH"),
	}
	command.Stderr = GinkgoWriter
	output, err := command.Output()
	Expect(err).NotTo(HaveOccurred())
	return string(output)
}

func (w sharedWorkspace) statusJSON() statusJSON {
	var decoded statusJSON
	Expect(json.Unmarshal([]byte(w.status("--json")), &decoded)).To(Succeed())
	return decoded
}

var _ = Describe("status", func() {
	It("says so when no daemon runs", func() {
		workspace := newSharedWorkspace()
		Expect(workspace.status()).To(ContainSubstring("No waythrough daemon is running"))
		Expect(workspace.statusJSON().Daemons).To(BeEmpty())
	})

	It("shows each daemon's sessions and the language servers they used", func() {
		workspace := newSharedWorkspace()
		session := workspace.startSession(time.Hour)
		Expect(session.definitionLine(workspace.file)).To(Equal(5))

		report := workspace.statusJSON()
		Expect(report.Daemons).To(HaveLen(1))
		running := report.Daemons[0]
		Expect(running.PID).To(Equal(workspace.daemonPIDs()[0]))
		Expect(running.Root).To(Equal(workspace.root))
		Expect(running.Health).To(Equal("healthy"))
		Expect(running.Sessions.State).To(Equal("serving"))
		Expect(running.Sessions.Active).To(Equal(1))
		Expect(running.LanguageServers).To(HaveLen(1))
		server := running.LanguageServers[0]
		Expect(server.Name).To(Equal("fake"))
		Expect(server.Root).To(Equal(workspace.root))
		Expect(server.Status).To(Equal("ready"))
		Expect(server.PID).To(Equal(workspace.languageServerPIDs()[0]))
		Expect(server.Requests.Total).To(Equal(1))

		text := workspace.status()
		Expect(text).To(ContainSubstring(workspace.root + "  [healthy]"))
		Expect(text).To(ContainSubstring("1 active of 64"))
		Expect(text).To(MatchRegexp(`fake\s+\.\s+ready\s+healthy`))
	})

	// The agent's view and the person's view are one report: through the
	// daemon, get_status sees the sessions the agent shares servers with.
	It("gives a shared session's agent the same report through get_status", func() {
		workspace := newSharedWorkspace()
		first := workspace.startSession(time.Hour)
		second := workspace.startSession(time.Hour)
		Expect(first.definitionLine(workspace.file)).To(Equal(5))

		result, err := second.session.CallTool(context.Background(),
			&mcp.CallToolParams{Name: "get_status"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeFalse())
		var report struct {
			Mode     string `json:"mode"`
			PID      int    `json:"pid"`
			Sessions struct {
				Active int `json:"active"`
			} `json:"sessions"`
			LanguageServers []struct {
				Status   string `json:"status"`
				Requests struct {
					Total int `json:"total"`
				} `json:"requests"`
			} `json:"language_servers"`
		}
		text := result.Content[0].(*mcp.TextContent).Text
		Expect(json.Unmarshal([]byte(text), &report)).To(Succeed())
		Expect(report.Mode).To(Equal("shared"))
		Expect(report.PID).To(Equal(workspace.daemonPIDs()[0]))
		Expect(report.Sessions.Active).To(Equal(2))
		Expect(report.LanguageServers).To(HaveLen(1))
		Expect(report.LanguageServers[0].Status).To(Equal("ready"))
		Expect(report.LanguageServers[0].Requests.Total).To(Equal(1),
			"the other session's request reached the same server")
	})

	It("removes the sockets of a daemon that was killed", func() {
		workspace := newSharedWorkspace()
		session := workspace.startSession(time.Hour)
		Expect(session.definitionLine(workspace.file)).To(Equal(5))
		killed := workspace.daemonPIDs()[0]
		Expect(syscall.Kill(killed, syscall.SIGKILL)).To(Succeed())
		Eventually(func() bool { return processAlive(killed) }, 5*time.Second).
			Should(BeFalse())

		report := workspace.statusJSON()
		Expect(report.Daemons).To(BeEmpty())
		Expect(report.Unreachable).To(HaveLen(1))
		Expect(report.Unreachable[0].Stale).To(BeTrue())
		Expect(workspace.sockets()).To(BeEmpty())
		Expect(workspace.status()).To(ContainSubstring("No waythrough daemon is running"),
			"once removed, a killed daemon is not reported again")
	})

	It("never counts as a session, so it never delays a drain", func() {
		workspace := newSharedWorkspace()
		session := workspace.startSession(500 * time.Millisecond)
		Expect(session.definitionLine(workspace.file)).To(Equal(5))
		Expect(session.session.Close()).To(Succeed())

		Eventually(func() int {
			return len(workspace.statusJSON().Daemons)
		}, 15*time.Second, 100*time.Millisecond).Should(BeZero())
	})
})
