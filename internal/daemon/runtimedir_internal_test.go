package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeDirForPrefersXDGRuntimeDir(t *testing.T) {
	if got := runtimeDirFor("/run/user/1000", "/tmp", 1000); got != "/run/user/1000/waythrough" {
		t.Fatalf("runtimeDirFor with XDG_RUNTIME_DIR = %q", got)
	}
	if got := runtimeDirFor("", "/tmp", 1000); got != "/tmp/waythrough-1000" {
		t.Fatalf("runtimeDirFor without XDG_RUNTIME_DIR = %q", got)
	}
}

// Each case builds one way a directory can fail to be private, and checks
// that the error names that way rather than some other.
func TestCheckPrivateDirRejectsEveryUnsafeShape(t *testing.T) {
	uid := os.Geteuid()
	cases := []struct {
		name    string
		build   func(t *testing.T) string
		uid     int
		message string
	}{
		{
			name: "a symlink to a private directory",
			build: func(t *testing.T) string {
				target := privateDir(t)
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			uid:     uid,
			message: "is a symlink",
		},
		{
			name: "a regular file",
			build: func(t *testing.T) string {
				file := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return file
			},
			uid:     uid,
			message: "is not a directory",
		},
		{
			name: "a group-writable directory",
			build: func(t *testing.T) string {
				dir := privateDir(t)
				if err := os.Chmod(dir, 0o770); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			uid:     uid,
			message: "group and others must have no access",
		},
		{
			name:    "a directory another user owns",
			build:   privateDir,
			uid:     uid + 1,
			message: "is owned by uid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkPrivateDir(tc.build(t), tc.uid)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("checkPrivateDir error = %v, want one containing %q", err, tc.message)
			}
		})
	}

	if err := checkPrivateDir(privateDir(t), uid); err != nil {
		t.Fatalf("checkPrivateDir rejected a private directory: %v", err)
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
