package daemon

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	lockPollFirst = 10 * time.Millisecond
	lockPollMax   = 250 * time.Millisecond
)

// ErrLockTimeout reports that another process still held a lock when the
// caller's deadline passed.
var ErrLockTimeout = errors.New("lock still held by another process")

// Lock is an exclusive flock on one file. The kernel releases it when the
// holding process exits, whatever the cause, so a crash never leaves a lock
// behind.
//
// A Lock must stay referenced for as long as it is meant to be held. If the
// *os.File became unreachable, its finalizer would close the descriptor and
// release the lock while the holder still relies on it. Go opens the file
// with O_CLOEXEC, and no caller passes it in ExtraFiles, so a child process
// never inherits it and an orphaned child cannot keep it held.
type Lock struct {
	file *os.File
}

// AcquireLock takes an exclusive lock on path, polling until deadline. It
// polls rather than blocking in flock because a blocked flock cannot be
// given a deadline.
func AcquireLock(path string, deadline time.Time) (*Lock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}

	wait := lockPollFirst
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			_ = file.Close()
			return nil, fmt.Errorf("lock %s: %w", path, ErrLockTimeout)
		}
		time.Sleep(min(wait, remaining))
		wait = min(2*wait, lockPollMax)
	}
}

// RecordPID writes the holder's pid into the lock file, so a client that
// finds a hung daemon can name it. Readers must hold no lock, so the pid is
// advisory: it names the process that held the lock last.
func (l *Lock) RecordPID() error {
	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("record pid in %s: %w", l.file.Name(), err)
	}
	if _, err := l.file.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return fmt.Errorf("record pid in %s: %w", l.file.Name(), err)
	}
	return nil
}

// Release closes the lock file, which releases the lock. The file stays on
// disk: removing it would let a waiter lock the old inode while a newcomer
// creates and locks a new one, and two processes would both hold "the"
// lock.
func (l *Lock) Release() error {
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("release lock %s: %w", l.file.Name(), err)
	}
	return nil
}

// RecordedPID reads the pid a lock holder recorded, or 0 when none can be
// read. It is for error messages only.
func RecordedPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}
