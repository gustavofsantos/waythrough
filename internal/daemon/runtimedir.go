package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// RuntimeDir returns the private directory that holds every daemon's
// socket, locks, and log for this user, creating it when it is missing.
//
// This directory is the security boundary: a process that can connect to a
// daemon's socket can have its language servers read any file this user can
// read. So it must be a real directory, owned by this user, that no other
// user can enter. The peer credential check on each connection guards the
// same boundary a second time, against a swap after this check.
func RuntimeDir() (string, error) {
	dir := runtimeDirFor(os.Getenv("XDG_RUNTIME_DIR"), os.TempDir(), os.Geteuid())
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("create daemon runtime directory %s: %w", dir, err)
	}
	if err := checkPrivateDir(dir, os.Geteuid()); err != nil {
		return "", err
	}
	return dir, nil
}

// runtimeDirFor prefers XDG_RUNTIME_DIR, which systemd creates private and
// on tmpfs. Without it, the directory name carries the uid, because the
// temporary directory may be shared by every user, as /tmp is on Linux.
func runtimeDirFor(xdgRuntimeDir, tempDir string, uid int) string {
	if xdgRuntimeDir != "" {
		return filepath.Join(xdgRuntimeDir, "waythrough")
	}
	return filepath.Join(tempDir, "waythrough-"+strconv.Itoa(uid))
}

// checkPrivateDir fails unless dir is a directory, not a symlink, owned by
// uid, with no permission bits for group or others.
func checkPrivateDir(dir string, uid int) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect daemon runtime directory: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("daemon runtime directory %s is a symlink", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("daemon runtime directory %s is not a directory", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("daemon runtime directory %s has no owner information", dir)
	}
	if int(stat.Uid) != uid {
		return fmt.Errorf("daemon runtime directory %s is owned by uid %d, not %d",
			dir, stat.Uid, uid)
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		return fmt.Errorf(
			"daemon runtime directory %s has mode %#o; group and others must have no access",
			dir, permissions)
	}
	return nil
}
