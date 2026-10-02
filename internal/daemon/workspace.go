// Package daemon shares one set of language servers among every agent
// session in a workspace. A daemon process owns the servers, and each
// `waythrough serve` attaches to it over a Unix socket. The daemon counts
// the connections and stops its servers when the last one has been gone for
// the linger duration.
//
// See docs/design/shared-language-servers.md for the design and its
// invariants.
package daemon

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// keyDomain separates this hash from any other SHA-256 use. Bump its
// version when the key inputs change, so an old daemon is never mistaken
// for a new one.
const keyDomain = "waythrough-daemon-key-v1"

// keyLengthBytes is how much of the SHA-256 digest a key keeps. 128 bits
// make a collision between two of one user's workspaces negligible, and a
// short key keeps the socket path within sun_path.
const keyLengthBytes = 16

// KeyInputs is everything that decides whether two sessions may share one
// daemon. Two sessions share only when every field is equal, so a field
// that changes costs at most a second daemon, never a wrong answer.
type KeyInputs struct {
	// BinaryIdentity names the waythrough build. See BinaryIdentity.
	BinaryIdentity string
	// Root is the workspace root: serve's working directory, unresolved,
	// because relative tool paths resolve against it as given.
	Root string
	// Config is the raw content of the configuration file.
	Config []byte
	// Path is the PATH the language servers will inherit, since it selects
	// which server binary a command name runs.
	Path string
}

// Key returns the workspace key for inputs: hex, keyLengthBytes long.
func Key(inputs KeyInputs) string {
	hash := sha256.New()
	// Each field is length-prefixed, so no two different inputs can produce
	// the same byte stream by moving bytes from one field into the next.
	for _, field := range [][]byte{
		[]byte(keyDomain),
		[]byte(inputs.BinaryIdentity),
		[]byte(inputs.Root),
		inputs.Config,
		[]byte(inputs.Path),
	} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		hash.Write(length[:])
		hash.Write(field)
	}
	return hex.EncodeToString(hash.Sum(nil)[:keyLengthBytes])
}

// BinaryIdentity names the running waythrough build: its version, plus the
// size and modification time of its executable. Every `go install` build
// reports the version "dev", so the version alone cannot tell two of them
// apart, and a daemon from an older build would answer with its old tools.
func BinaryIdentity(version string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate waythrough executable: %w", err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("inspect waythrough executable: %w", err)
	}
	return fmt.Sprintf("%s size=%d mtime=%d",
		version, info.Size(), info.ModTime().UnixNano()), nil
}

// Paths are the files one daemon key owns in the runtime directory.
type Paths struct {
	// Socket is where the daemon listens.
	Socket string
	// Lock is held by the daemon for its whole life. It proves a daemon for
	// this key is alive, and it is never removed.
	Lock string
	// Spawn is held by a client while it attaches or starts a daemon, so
	// only one client at a time starts one. It is never removed.
	Spawn string
	// Log receives the daemon's stderr.
	Log string
	// Status answers status readers: see Report.
	Status string
}

// statusSuffix names a key's status socket. It is the longest socket name a
// key has, so it is the one PathsFor measures against sun_path.
const statusSuffix = ".status"

// PathsFor returns the files for key in runtimeDir. It fails when the
// socket path does not fit in sun_path, because the kernel would otherwise
// truncate it, or bind refuse it, with a far less useful error.
func PathsFor(runtimeDir, key string) (Paths, error) {
	paths := Paths{
		Socket: filepath.Join(runtimeDir, key+".sock"),
		Lock:   filepath.Join(runtimeDir, key+".lock"),
		Spawn:  filepath.Join(runtimeDir, key+".spawn"),
		Log:    filepath.Join(runtimeDir, key+".log"),
		Status: filepath.Join(runtimeDir, key+statusSuffix),
	}
	maxBytes := maxSocketPathBytes()
	if len(paths.Status) > maxBytes {
		return Paths{}, fmt.Errorf(
			"daemon socket path %s is %d bytes; maximum is %d on %s "+
				"(set XDG_RUNTIME_DIR or TMPDIR to a shorter directory)",
			paths.Status, len(paths.Status), maxBytes, runtime.GOOS)
	}
	return paths, nil
}

// maxSocketPathBytes is sun_path's size less its terminating NUL: 108 bytes
// on Linux, and 104 on macOS and the BSDs.
func maxSocketPathBytes() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}
