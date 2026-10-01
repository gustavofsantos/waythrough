package cli

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gustavofsantos/waythrough/internal/daemon"
)

const sharedTestConfig = `language_servers:
  - name: fake
    command: fake-language-server
    filetypes:
      .fake: fake
`

// exitingStarter stands in for daemonStarter: every daemon it "starts"
// exits at once. Before the first one exits, it runs edit, the way a user
// saving the configuration mid-attach would.
func exitingStarter(starts *[]string, edit func()) func(string, daemon.Paths) daemon.StartFunc {
	return func(key string, _ daemon.Paths) daemon.StartFunc {
		return func(time.Time) (<-chan error, error) {
			*starts = append(*starts, key)
			if len(*starts) == 1 && edit != nil {
				edit()
			}
			exited := make(chan error, 1)
			exited <- errors.New("exit status 1")
			return exited, nil
		}
	}
}

func sharedTestSetup(t *testing.T) (root, runtimeDir, configPath string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	runtimeParent, err := os.MkdirTemp("", "wtc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeParent) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeParent)
	runtimeDir, err = daemon.RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(home, ".waythrough.yaml")
	if err := os.WriteFile(configPath, []byte(sharedTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return t.TempDir(), runtimeDir, configPath
}

// A daemon that exits early for any reason but a changed key would exit
// the same way again, so the session must fail rather than start another.
func TestAttachToWorkspaceDoesNotRetryAnUnchangedKey(t *testing.T) {
	root, runtimeDir, _ := sharedTestSetup(t)
	var starts []string

	_, err := attachToWorkspace(root, runtimeDir, time.Now().Add(10*time.Second),
		slog.New(slog.DiscardHandler), exitingStarter(&starts, nil))
	if !errors.Is(err, daemon.ErrDaemonExited) {
		t.Fatalf("error = %v, want ErrDaemonExited", err)
	}
	if len(starts) != 1 {
		t.Fatalf("started %d daemons, want 1", len(starts))
	}
}

// The configuration changed after this session read it, so the daemon it
// started refused the stale key. One more attach, with the new key, is the
// whole remedy.
func TestAttachToWorkspaceRetriesOnceWithAChangedKey(t *testing.T) {
	root, runtimeDir, configPath := sharedTestSetup(t)
	var starts []string
	edit := func() {
		edited := "# edited\n" + sharedTestConfig
		if err := os.WriteFile(configPath, []byte(edited), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := attachToWorkspace(root, runtimeDir, time.Now().Add(10*time.Second),
		slog.New(slog.DiscardHandler), exitingStarter(&starts, edit))
	if !errors.Is(err, daemon.ErrDaemonExited) {
		t.Fatalf("error = %v, want ErrDaemonExited", err)
	}
	if len(starts) != 2 || starts[0] == starts[1] {
		t.Fatalf("started daemons for keys %q, want two different keys", starts)
	}
}
