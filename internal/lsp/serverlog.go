package lsp

import (
	"bytes"
	"log/slog"
	"unicode/utf8"
)

// serverLogLineBytesMax bounds both what one log record carries of a
// language server's stderr and what serverLog holds between writes. A line
// longer than this is split across records, whether or not the server ever
// ends it: stderr is the server's to fill, so nothing about its volume may
// be left to the server to decide.
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

// Write splits p into lines and logs each one, emitting whenever a line
// ends or reaches serverLogLineBytesMax, whichever comes first. It always
// reports every byte written, as io.Writer requires and as the exec copy
// goroutine checks.
func (l *serverLog) Write(p []byte) (int, error) {
	written := len(p)

	for len(p) > 0 {
		newline := bytes.IndexByte(p, '\n')
		if newline < 0 {
			l.appendCapped(p)
			break
		}

		l.appendCapped(p[:newline])
		// pending is empty here only for a line the server left blank,
		// because appendCapped emits a full record only when more of the
		// line is still to come. A blank line stays one record, so the log
		// keeps line-for-line correspondence with the server's stderr.
		l.emit(l.pending, false)
		l.pending = l.pending[:0]
		p = p[newline+1:]
	}

	return written, nil
}

// appendCapped adds one line, or one piece of a line, to pending, emitting
// a record each time pending fills. It leaves pending no longer than
// serverLogLineBytesMax and copies no more than that in one step, so
// neither the size of a record nor the memory held between writes is the
// language server's to choose.
//
// It emits only when bytes of the same line still follow, so a line that
// ends exactly at the cap is reported as the whole line it is.
//
// The loop terminates because every pass either empties pending, which
// makes room for a whole cap, or takes at least one byte from segment.
func (l *serverLog) appendCapped(segment []byte) {
	for len(segment) > 0 {
		if len(l.pending) == serverLogLineBytesMax {
			l.emit(l.pending, true)
			l.pending = l.pending[:0]
		}

		room := serverLogLineBytesMax - len(l.pending)
		take := min(room, len(segment))
		l.pending = append(l.pending, segment[:take]...)
		segment = segment[take:]
	}
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

// emit logs one record. truncated says this record was cut at the cap and
// the rest of the same line follows in the next one, so a reader never has
// to guess whether a line ended where the record did.
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
