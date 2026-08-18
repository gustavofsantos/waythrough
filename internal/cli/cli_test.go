package cli_test

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.yaml.in/yaml/v3"

	"github.com/gustavofsantos/waythrough/internal/cli"
	"github.com/gustavofsantos/waythrough/internal/config"
)

func writeConfigFile(path, content string) {
	Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
}

// twoServersOneExtension breaks the one-server-per-extension rule, the
// invariant every command that reads a config must refuse to run against.
// The two entry names are deliberately not prefixes of one another, so an
// assertion naming one cannot pass on the other.
const twoServersOneExtension = `
language_servers:
  - name: clojure-lsp
    command: clojure-lsp
    filetypes:
      .clj: clojure
  - name: clj-kondo-lsp
    command: clj-kondo-lsp
    filetypes:
      .clj: clojure
`

var _ = Describe("waythrough CLI", func() {
	var (
		configPath string
		stdout     *bytes.Buffer
		stderr     *bytes.Buffer
	)

	BeforeEach(func() {
		configPath = filepath.Join(GinkgoT().TempDir(), "waythrough.yaml")
		stdout = &bytes.Buffer{}
		stderr = &bytes.Buffer{}
	})

	run := func(args ...string) int {
		return cli.Execute(args, stdout, stderr)
	}

	Describe("init", func() {
		When("no config file exists at the target path", func() {
			It("creates a config file with one example language server", func() {
				code := run("init", "--config", configPath)
				Expect(code).To(Equal(0))

				data, err := os.ReadFile(configPath)
				Expect(err).NotTo(HaveOccurred())

				var cfg config.Config
				Expect(yaml.Unmarshal(data, &cfg)).To(Succeed())
				Expect(cfg.LanguageServers).To(HaveLen(1))
			})
		})

		When("a config file already exists at the target path", func() {
			BeforeEach(func() {
				writeConfigFile(configPath, "language_servers: []\n")
			})

			It("refuses to overwrite the file and exits non-zero", func() {
				code := run("init", "--config", configPath)
				Expect(code).NotTo(Equal(0))
				Expect(stderr.String()).To(ContainSubstring(configPath))

				data, err := os.ReadFile(configPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal("language_servers: []\n"))
			})
		})
	})

	Describe("validate", func() {
		When("every entry has a name, a command, and a filetype mapping", func() {
			BeforeEach(func() {
				writeConfigFile(configPath, `
language_servers:
  - name: clojure-lsp
    command: clojure-lsp
    filetypes:
      .clj: clojure
`)
			})

			It("exits zero", func() {
				Expect(run("validate", "--config", configPath)).To(Equal(0))
			})
		})

		When("an entry is missing its command", func() {
			BeforeEach(func() {
				writeConfigFile(configPath, `
language_servers:
  - name: clojure-lsp
    filetypes:
      .clj: clojure
`)
			})

			It("exits non-zero and names the missing field and the entry", func() {
				code := run("validate", "--config", configPath)
				Expect(code).NotTo(Equal(0))
				Expect(stderr.String()).To(ContainSubstring("command"))
				Expect(stderr.String()).To(ContainSubstring("clojure-lsp"))
			})
		})

		When("an entry sets an unknown readiness value", func() {
			BeforeEach(func() {
				writeConfigFile(configPath, `
language_servers:
  - name: clojure-lsp
    command: clojure-lsp
    readiness: eager
    filetypes:
      .clj: clojure
`)
			})

			It("exits non-zero and names the invalid value", func() {
				code := run("validate", "--config", configPath)
				Expect(code).NotTo(Equal(0))
				Expect(stderr.String()).To(ContainSubstring("eager"))
			})
		})

		When("the file is not valid YAML", func() {
			BeforeEach(func() {
				writeConfigFile(configPath, "language_servers: [\n")
			})

			It("exits non-zero and reports a parse error", func() {
				code := run("validate", "--config", configPath)
				Expect(code).NotTo(Equal(0))
				Expect(stderr.String()).NotTo(BeEmpty())
			})
		})

		When("two entries claim the same file extension", func() {
			BeforeEach(func() { writeConfigFile(configPath, twoServersOneExtension) })

			It("exits non-zero and names the extension and both entries", func() {
				code := run("validate", "--config", configPath)
				Expect(code).NotTo(Equal(0))
				Expect(stderr.String()).To(ContainSubstring("already claimed"))
				Expect(stderr.String()).To(ContainSubstring(".clj"))
				Expect(stderr.String()).To(ContainSubstring("clojure-lsp"))
				Expect(stderr.String()).To(ContainSubstring("clj-kondo-lsp"))
			})
		})
	})

	// serve is covered here only for configs it must reject. A config it
	// accepts sends serve into mcp.Server.Run on a stdio transport, which
	// reads the test binary's own stdin. What that returns depends on how
	// the suite was started, so there is nothing stable to assert on.
	Describe("serve", func() {
		When("two entries claim the same file extension", func() {
			BeforeEach(func() { writeConfigFile(configPath, twoServersOneExtension) })

			// One extension routes to exactly one language server. Waythrough
			// resolves a file to a server by its extension alone, so a second
			// claim on .clj leaves the routing table to pick a winner in
			// silence, and an agent gets definitions from whichever entry came
			// last. serve must refuse before it spawns a single subprocess.
			It("refuses to start, and names the extension and both entries", func() {
				code := run("serve", "--config", configPath)
				Expect(code).NotTo(Equal(0))
				Expect(stderr.String()).To(ContainSubstring("already claimed"))
				Expect(stderr.String()).To(ContainSubstring(".clj"))
				Expect(stderr.String()).To(ContainSubstring("clojure-lsp"))
				Expect(stderr.String()).To(ContainSubstring("clj-kondo-lsp"))
			})
		})
	})
})

var _ = Describe("serve --debug", func() {
	var (
		configPath string
		stderr     *bytes.Buffer
	)

	BeforeEach(func() {
		configPath = filepath.Join(GinkgoT().TempDir(), "waythrough.yaml")
		stderr = &bytes.Buffer{}
	})

	// serve on a config it accepts blocks on the test binary's own stdin,
	// so a config it must reject is the one way to run the flag through
	// cobra and see that the command still refuses for the right reason.
	When("the config claims one extension twice", func() {
		BeforeEach(func() { writeConfigFile(configPath, twoServersOneExtension) })

		It("accepts the flag and still refuses to start", func() {
			code := cli.Execute(
				[]string{"serve", "--config", configPath, "--debug"},
				&bytes.Buffer{}, stderr)

			Expect(code).NotTo(Equal(0))
			Expect(stderr.String()).To(ContainSubstring("already claimed"))
			Expect(stderr.String()).NotTo(ContainSubstring("unknown flag"))
		})
	})
})
