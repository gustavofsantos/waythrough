//go:build !linux

package lsp

// residentBytes reports zero, meaning unknown. Reading another process's
// memory on macOS needs libproc through cgo, which this build does not use.
func residentBytes(int) uint64 { return 0 }
