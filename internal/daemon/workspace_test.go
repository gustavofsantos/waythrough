package daemon_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/daemon"
)

var _ = Describe("Key", func() {
	base := daemon.KeyInputs{
		BinaryIdentity: "v1.0.0 size=1 mtime=1",
		Root:           "/work/project",
		Config:         []byte("language_servers: []\n"),
		Path:           "/usr/bin:/bin",
	}

	It("is the same for the same inputs, and short enough for a socket name", func() {
		key := daemon.Key(base)
		Expect(daemon.Key(base)).To(Equal(key))
		Expect(key).To(MatchRegexp(`^[0-9a-f]{32}$`))
	})

	DescribeTable("differs when any one input differs",
		func(change func(*daemon.KeyInputs)) {
			changed := base
			changed.Config = append([]byte(nil), base.Config...)
			change(&changed)
			Expect(daemon.Key(changed)).NotTo(Equal(daemon.Key(base)))
		},
		Entry("another build", func(in *daemon.KeyInputs) { in.BinaryIdentity = "dev" }),
		Entry("another root", func(in *daemon.KeyInputs) { in.Root = "/work/other" }),
		Entry("an edited configuration", func(in *daemon.KeyInputs) {
			in.Config = []byte("language_servers: [] # edited\n")
		}),
		Entry("another PATH", func(in *daemon.KeyInputs) { in.Path = "/opt/bin:/usr/bin" }),
	)

	// Without a length prefix, bytes moved from the end of one field to the
	// start of the next would hash the same stream.
	It("does not let bytes move between adjacent fields", func() {
		left := daemon.KeyInputs{Root: "/work/ab", Config: []byte("c")}
		right := daemon.KeyInputs{Root: "/work/a", Config: []byte("bc")}
		Expect(daemon.Key(left)).NotTo(Equal(daemon.Key(right)))
	})
})

var _ = Describe("PathsFor", func() {
	It("names one socket, lock, spawn lock, log, and status socket per key", func() {
		paths, err := daemon.PathsFor("/run/user/1000/waythrough", "0123abcd")
		Expect(err).NotTo(HaveOccurred())
		Expect(paths).To(Equal(daemon.Paths{
			Socket: "/run/user/1000/waythrough/0123abcd.sock",
			Lock:   "/run/user/1000/waythrough/0123abcd.lock",
			Spawn:  "/run/user/1000/waythrough/0123abcd.spawn",
			Log:    "/run/user/1000/waythrough/0123abcd.log",
			Status: "/run/user/1000/waythrough/0123abcd.status",
		}))
	})

	// The status socket's name is two bytes longer than the session
	// socket's, so a directory exactly long enough for the session socket
	// must still be refused: the daemon could not listen for status there.
	It("refuses a directory where the session socket fits but the status socket does not", func() {
		maxBytes := 107
		if runtime.GOOS != "linux" {
			maxBytes = 103
		}
		key := strings.Repeat("k", 32)
		dirBytes := maxBytes - len("/"+key+".sock")
		dir := "/" + strings.Repeat("d", dirBytes-1)

		_, err := daemon.PathsFor(dir, key)
		Expect(err).To(MatchError(ContainSubstring(".status")))
	})

	It("refuses a socket path that does not fit in sun_path", func() {
		longDir := "/" + strings.Repeat("d", 100)
		_, err := daemon.PathsFor(longDir, strings.Repeat("k", 32))
		Expect(err).To(MatchError(ContainSubstring("maximum is")))
	})
})

var _ = Describe("RuntimeDir", func() {
	It("creates a private directory under XDG_RUNTIME_DIR", func() {
		xdg := GinkgoT().TempDir()
		GinkgoT().Setenv("XDG_RUNTIME_DIR", xdg)

		dir, err := daemon.RuntimeDir()
		Expect(err).NotTo(HaveOccurred())
		Expect(dir).To(Equal(filepath.Join(xdg, "waythrough")))

		info, err := os.Stat(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})

	It("refuses an existing directory that other users can enter", func() {
		xdg := GinkgoT().TempDir()
		GinkgoT().Setenv("XDG_RUNTIME_DIR", xdg)
		Expect(os.Mkdir(filepath.Join(xdg, "waythrough"), 0o700)).To(Succeed())
		// Chmod, since Mkdir's mode passes through the umask.
		Expect(os.Chmod(filepath.Join(xdg, "waythrough"), 0o755)).To(Succeed())

		_, err := daemon.RuntimeDir()
		Expect(err).To(MatchError(ContainSubstring("group and others must have no access")))
	})
})
