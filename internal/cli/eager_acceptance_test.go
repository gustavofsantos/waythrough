package cli_test

import (
	"bytes"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs watch fakelsp's instance log, which gains a line each time the
// language server starts, to tell whether serve started it before any tool
// call asked for it.
var _ = Describe("serve --eager", func() {
	It("leaves an unnamed server idle until a tool call needs it", func() {
		workspace := newSharedWorkspace()
		session := workspace.startServe()

		Consistently(workspace.languageServerPIDs, 200*time.Millisecond).
			Should(BeEmpty())
		Expect(session.definitionLine(workspace.file)).To(Equal(5))
		Expect(workspace.languageServerPIDs()).To(HaveLen(1))
	})

	It("starts a named server before any tool call, and answers from it", func() {
		workspace := newSharedWorkspace()
		session := workspace.startServe("--eager=fake")

		Eventually(workspace.languageServerPIDs, 5*time.Second).Should(HaveLen(1))
		Expect(session.definitionLine(workspace.file)).To(Equal(5))
		Expect(workspace.languageServerPIDs()).To(HaveLen(1),
			"the tool call must reach the server started eagerly, not start another")
	})

	It("starts a named server in the daemon a --shared session starts", func() {
		workspace := newSharedWorkspace()
		session := workspace.startServe("--shared", "--linger=200ms", "--eager=fake")

		Eventually(workspace.languageServerPIDs, 5*time.Second).Should(HaveLen(1))
		Expect(session.definitionLine(workspace.file)).To(Equal(5))
		Expect(workspace.languageServerPIDs()).To(HaveLen(1))
	})

	DescribeTable("refuses a name the configuration does not have",
		func(serveArgs ...string) {
			workspace := newSharedWorkspace()
			command := workspace.serveCommand(serveArgs...)
			var stderr bytes.Buffer
			command.Stderr = &stderr

			Expect(command.Run()).NotTo(Succeed())
			Expect(stderr.String()).To(ContainSubstring(`"missing"`))
			Expect(workspace.languageServerPIDs()).To(BeEmpty())
		},
		Entry("alone", "--eager=fake,missing"),
		Entry("with --shared", "--shared", "--eager=missing"),
	)
})
