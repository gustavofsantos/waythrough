//go:build !(aix || android || darwin || dragonfly || freebsd || hurd || illumos || ios || linux || netbsd || openbsd || solaris)

package lsp

import "os"

func openSourceFile(path string) (*os.File, error) {
	return os.Open(path)
}
