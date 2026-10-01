package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// The daemon writes exactly one line on each connection before any MCP
// byte: a greeting once it has counted the session, or a busy line when it
// refuses it. A client that reads neither, only EOF or a reset, reached a
// daemon that was draining, and attaches again.
const (
	protocolVersion   = "1"
	greetingPrefix    = "waythrough-daemon " + protocolVersion + " "
	busyPrefix        = "busy "
	maxLineBytes      = 64
	lineWriteDeadline = 5 * time.Second
	// GreetingTimeout bounds how long a client waits for the first line. A
	// live daemon writes it at once, so a timeout means the daemon is hung.
	GreetingTimeout = 5 * time.Second
)

// ErrBusy reports that the daemon refused a session because it already
// serves its maximum. Attaching again would meet the same refusal.
var ErrBusy = errors.New("daemon serves its maximum number of sessions")

// ErrNoGreeting reports a connection that closed before its first line,
// which is what a client of a draining daemon sees.
var ErrNoGreeting = errors.New("daemon closed the connection before greeting")

func greetingLine(key string) string { return greetingPrefix + key + "\n" }

// ReadGreeting reads the daemon's first line from reader and checks that it
// greets this key. reader must be the only reader of conn from here on,
// because it may already hold MCP bytes that follow the greeting.
func ReadGreeting(conn net.Conn, reader *bufio.Reader, key string) error {
	if err := conn.SetReadDeadline(time.Now().Add(GreetingTimeout)); err != nil {
		return fmt.Errorf("set greeting deadline: %w", err)
	}
	line, err := readLine(reader)
	if err != nil {
		return err
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear greeting deadline: %w", err)
	}

	switch {
	case line == strings.TrimSuffix(greetingLine(key), "\n"):
		return nil
	case strings.HasPrefix(line, busyPrefix):
		return fmt.Errorf("%w: %s", ErrBusy, strings.TrimPrefix(line, busyPrefix))
	default:
		return fmt.Errorf("unexpected daemon greeting %q", line)
	}
}

// readLine reads one line of at most maxLineBytes. The bound holds even
// when the peer sends no newline at all.
func readLine(reader *bufio.Reader) (string, error) {
	var line strings.Builder
	for line.Len() <= maxLineBytes {
		character, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return "", fmt.Errorf("read daemon greeting: %w", err)
			}
			if line.Len() == 0 {
				return "", ErrNoGreeting
			}
			return "", fmt.Errorf("read daemon greeting: %w", err)
		}
		if character == '\n' {
			return line.String(), nil
		}
		line.WriteByte(character)
	}
	return "", fmt.Errorf("daemon greeting exceeds %d bytes", maxLineBytes)
}

// writeLine writes one protocol line under a deadline, so a client that
// never reads cannot hold a session slot open.
func writeLine(conn net.Conn, line string) error {
	if err := conn.SetWriteDeadline(time.Now().Add(lineWriteDeadline)); err != nil {
		return fmt.Errorf("set line deadline: %w", err)
	}
	if _, err := conn.Write([]byte(line)); err != nil {
		return fmt.Errorf("write %q: %w", strings.TrimSpace(line), err)
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear line deadline: %w", err)
	}
	return nil
}
