package lsp

import (
	"bytes"
	"log/slog"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingLog builds a serverLog writing to a buffer the caller can read,
// which is the only way to see what a record said: slog offers no way to
// ask a handler what it received.
func recordingLog() (*serverLog, *bytes.Buffer) {
	var recorded bytes.Buffer
	handler := slog.NewTextHandler(&recorded, &slog.HandlerOptions{Level: slog.LevelDebug})
	return newServerLog(slog.New(handler), "fake"), &recorded
}

var _ = Describe("a language server's stderr", func() {
	It("becomes one record per line, naming the server that wrote it", func() {
		log, recorded := recordingLog()

		written, err := log.Write([]byte("first problem\nsecond problem\n"))

		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal(len("first problem\nsecond problem\n")),
			"an io.Writer must report every byte it was given, and the exec "+
				"goroutine copying this stream checks that it did")
		Expect(recorded.String()).To(ContainSubstring("first problem"))
		Expect(recorded.String()).To(ContainSubstring("second problem"))
		Expect(recorded.String()).To(ContainSubstring("server=fake"))
		Expect(strings.Count(recorded.String(), "language server stderr")).To(Equal(2))
	})

	It("keeps a line split across writes whole", func() {
		log, recorded := recordingLog()

		_, err := log.Write([]byte("cannot find "))
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded.String()).To(BeEmpty(),
			"a line the server has not finished is not a line yet")

		_, err = log.Write([]byte("go.mod\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded.String()).To(ContainSubstring("cannot find go.mod"))
	})

	// A server that dies mid-sentence leaves its last words unterminated,
	// and those are often the words worth reading. wait flushes after the
	// process exits so they are not lost.
	It("gives up its unterminated tail on flush", func() {
		log, recorded := recordingLog()

		_, err := log.Write([]byte("panic: nil map"))
		Expect(err).NotTo(HaveOccurred())
		Expect(recorded.String()).To(BeEmpty())

		log.flush()
		Expect(recorded.String()).To(ContainSubstring("panic: nil map"))
		Expect(recorded.String()).To(ContainSubstring("truncated=false"))
	})

	It("stays silent when a flush finds nothing pending", func() {
		log, recorded := recordingLog()

		log.flush()
		log.flush()

		Expect(recorded.String()).To(BeEmpty())
	})

	It("drops the carriage return of a line a server ended with CRLF", func() {
		log, recorded := recordingLog()

		_, err := log.Write([]byte("windows line\r\n"))

		Expect(err).NotTo(HaveOccurred())
		Expect(recorded.String()).To(ContainSubstring(`line="windows line"`))
	})

	// The bound this pins is the point of the type: stderr belongs to the
	// language server, so a server that never writes a newline must not
	// decide how much memory Waythrough holds on its behalf.
	It("cuts a line no newline ever ends, rather than buffering it", func() {
		log, recorded := recordingLog()
		unbroken := strings.Repeat("x", serverLogLineBytesMax*3+11)

		written, err := log.Write([]byte(unbroken))

		Expect(err).NotTo(HaveOccurred())
		Expect(written).To(Equal(len(unbroken)))
		Expect(strings.Count(recorded.String(), "truncated=true")).To(Equal(3),
			"three full caps' worth was emitted, and the remainder is still pending")
		Expect(len(log.pending)).To(Equal(11),
			"what is held is the remainder alone, never the whole line")
	})

	It("holds less than one cap between writes, however large each write is", func() {
		log, recorded := recordingLog()

		for range 8 {
			_, err := log.Write([]byte(strings.Repeat("y", serverLogLineBytesMax)))
			Expect(err).NotTo(HaveOccurred())
			Expect(len(log.pending)).To(BeNumerically("<", serverLogLineBytesMax))
		}

		Expect(recorded.String()).To(ContainSubstring("truncated=true"))
	})
})
