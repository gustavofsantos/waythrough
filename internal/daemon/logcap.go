package daemon

import (
	"fmt"
	"io"
	"sync"
)

// LogBytesMax bounds what one daemon life writes to its log. A daemon can
// live for days, and debug records carry tool results, so the log needs a
// bound that does not depend on either.
const LogBytesMax = 16 << 20

// CappedWriter passes writes through until its limit have been written,
// then writes one marker line and drops the rest. It reports every write as
// complete, because a logger has nothing useful to do with a short write.
type CappedWriter struct {
	mu      sync.Mutex
	out     io.Writer
	limit   int
	left    int
	stopped bool
}

// NewCappedWriter returns a CappedWriter over out that passes limitBytes.
func NewCappedWriter(out io.Writer, limitBytes int) *CappedWriter {
	return &CappedWriter{out: out, limit: limitBytes, left: limitBytes}
}

func (w *CappedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return len(data), nil
	}
	if len(data) <= w.left {
		w.left -= len(data)
		if _, err := w.out.Write(data); err != nil {
			return 0, fmt.Errorf("write daemon log: %w", err)
		}
		return len(data), nil
	}
	w.stopped = true
	_, _ = fmt.Fprintf(w.out,
		"\nwaythrough: log reached its %d-byte limit; later records dropped\n", w.limit)
	return len(data), nil
}
