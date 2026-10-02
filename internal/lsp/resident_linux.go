package lsp

import (
	"bytes"
	"os"
	"strconv"
)

// statmBytesMax bounds the read of /proc/<pid>/statm, which holds seven
// decimal numbers on one line.
const statmBytesMax = 256

// residentBytes reads a process's resident set size from /proc, and
// returns zero when pid is zero or the process is gone. The second field of
// statm counts resident pages.
func residentBytes(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	file, err := os.Open("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0
	}
	defer func() { _ = file.Close() }()

	buffer := make([]byte, statmBytesMax)
	count, err := file.Read(buffer)
	if err != nil {
		return 0
	}
	fields := bytes.Fields(buffer[:count])
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
