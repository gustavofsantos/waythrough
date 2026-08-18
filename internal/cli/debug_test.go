package cli

import (
	"bytes"
	"context"
	"log/slog"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs live inside package cli because the rule they pin is not
// reachable from outside it: serve blocks on the process's own stdin, so
// the writer a run would have logged through can only be inspected here.
var _ = Describe("the logger serve builds", func() {
	When("--debug is not set", func() {
		It("records nothing, whatever it is handed", func() {
			var written bytes.Buffer

			logger := newLogger(&written, false)
			logger.Debug("a lifecycle change")
			logger.Warn("a language server gave up")

			Expect(written.String()).To(BeEmpty())
			Expect(logger.Enabled(context.Background(), slog.LevelDebug)).To(BeFalse(),
				"the components downstream skip their own work on this answer")
		})
	})

	When("--debug is set", func() {
		It("records debug-level output to the writer it was given", func() {
			var written bytes.Buffer

			newLogger(&written, true).Debug("a lifecycle change")

			Expect(written.String()).To(ContainSubstring("a lifecycle change"))
		})
	})

	// serve speaks MCP over stdio, so stdout carries JSON-RPC frames and
	// nothing else. One log byte written there desynchronizes the framing
	// and the coding agent on the other end loses the session, which is a
	// failure no error return could recover — so it is refused outright.
	When("asked to record to stdout", func() {
		It("refuses, because stdout carries the MCP frames", func() {
			Expect(func() { newLogger(os.Stdout, true) }).To(PanicWith(
				ContainSubstring("must not go to stdout")))
		})
	})
})
