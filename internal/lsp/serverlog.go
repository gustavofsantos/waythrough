package lsp

import (
	"bytes"
	"log/slog"
	"unicode/utf8"
)

// serverLogLineBytesMax bounds what one log record carries of a language
// server's stderr. A server that writes a very long line, or no newline at
// all, is split across records at this boundary rather than buffered
// without limit: stderr is the server's to fill, so nothing about its
// volume may be left to the server to decide.
const serverLogLineBytesMax = 4096

// serverLog turns a language server's stderr into one log record per line.
// Waythrough discards that stream by default, which hides the one place a
// language server explains itself when it will not start or will not
// answer; --debug routes it here instead.
//
// The os/exec copy goroutine for the server's process is its only writer,
// and exec.Cmd.Wait returns only after that goroutine has finished, so a
// flush after Wait races with nothing. serverLog is not otherwise safe for
// concurrent use.
type serverLog struct {
	logger *slog.Logger
	// pending holds the bytes written since the last newline. It is shorter
	// than serverLogLineBytesMax whenever a method here returns.
	pending []byte
}

func newServerLog(logger *slog.Logger, name string) *serverLog {
	return &serverLog{logger: logger.With(slog.String("server", name))}
}

// Write splits p into lines and logs each one. It always reports every byte
// written, as io.Writer requires and as the exec copy goroutine checks.
func (l *serverLog) Write(p []byte) (int, error) {
	written := len(p)

	for {
		newline := bytes.IndexByte(p, '\n')
		if newline < 0 {
			break
		}
		l.pending = append(l.pending, p[:newline]...)
		l.emit(l.pending, false)
		l.pending = l.pending[:0]
		p = p[newline+1:]
	}
	l.pending = append(l.pending, p...)

	// Everything past the cap is emitted as its own record, so a server
	// that never writes a newline cannot grow pending without limit.
	for len(l.pending) >= serverLogLineBytesMax {
		l.emit(l.pending[:serverLogLineBytesMax], true)
		// Copying the remainder to the front keeps pending's capacity from
		// growing with the length of one unbroken line.
		l.pending = append(l.pending[:0], l.pending[serverLogLineBytesMax:]...)
	}

	return written, nil
}

// flush logs whatever the server wrote after its last newline. A server
// that dies mid-line still said something, and that last partial line is
// often the one worth reading.
func (l *serverLog) flush() {
	if len(l.pending) == 0 {
		return
	}
	l.emit(l.pending, false)
	l.pending = l.pending[:0]
}

// emit logs one line. truncated separates a line the server ended from one
// serverLogLineBytesMax ended, so a reader is never left to guess which.
// It does not clear pending, because one caller emits only a prefix of it.
func (l *serverLog) emit(line []byte, truncated bool) {
	l.logger.Debug("language server stderr",
		slog.String("line", string(bytes.TrimSuffix(line, []byte("\r")))),
		slog.Bool("truncated", truncated))
}

// errorTextBytesMax bounds what one record carries of an error message. A
// handshake failure quotes what the language server replied, and a server
// chooses the length of its own replies.
const errorTextBytesMax = 1024

// truncateForLog caps text at errorTextBytesMax, cutting on a rune boundary
// so the record stays valid UTF-8, and says that it cut rather than leaving
// a reader to mistake the cut for the whole message.
func truncateForLog(text string) string {
	if len(text) <= errorTextBytesMax {
		return text
	}

	cut := errorTextBytesMax
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…[truncated]"
}
