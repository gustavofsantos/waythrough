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
	})
})
