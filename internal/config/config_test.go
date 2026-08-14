package config_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
)

func writeFile(path, content string) {
	Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
}

var _ = Describe("Load", func() {
	var path string

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "waythrough.yaml")
	})

	When("the file holds a well-formed language-server entry", func() {
		BeforeEach(func() {
			writeFile(path, `
language_servers:
  - name: clojure-lsp
    command: clojure-lsp
    args: ["--verbose"]
    filetypes:
      .clj: clojure
`)
		})

		It("parses the entry's fields", func() {
			cfg, err := config.Load(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.LanguageServers).To(HaveLen(1))

			entry := cfg.LanguageServers[0]
			Expect(entry.Name).To(Equal("clojure-lsp"))
			Expect(entry.Command).To(Equal("clojure-lsp"))
			Expect(entry.Args).To(Equal([]string{"--verbose"}))
			Expect(entry.Filetypes).To(Equal(map[string]string{".clj": "clojure"}))
		})
	})

	When("the file is not valid YAML", func() {
		BeforeEach(func() {
			writeFile(path, "language_servers: [\n")
		})

		It("returns a parse error", func() {
			_, err := config.Load(path)
			Expect(err).To(HaveOccurred())
		})
	})

	When("the file does not exist", func() {
		It("returns an error", func() {
			_, err := config.Load(filepath.Join(GinkgoT().TempDir(), "missing.yaml"))
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("Validate", func() {
	valid := func() config.Config {
		return config.Config{
			LanguageServers: []config.LanguageServer{
				{
					Name:      "clojure-lsp",
					Command:   "clojure-lsp",
					Filetypes: map[string]string{".clj": "clojure"},
				},
			},
		}
	}

	When("every entry has a name, a command, and at least one filetype mapping", func() {
		It("returns no error", func() {
			Expect(config.Validate(valid())).To(Succeed())
		})
	})

	When("an entry is missing its command", func() {
		It("names the missing field and the entry", func() {
			cfg := valid()
			cfg.LanguageServers[0].Command = ""

			err := config.Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("command"))
			Expect(err.Error()).To(ContainSubstring("clojure-lsp"))
		})
	})

	When("an entry is missing its filetypes", func() {
		It("names the missing field and the entry", func() {
			cfg := valid()
			cfg.LanguageServers[0].Filetypes = nil

			err := config.Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("filetypes"))
			Expect(err.Error()).To(ContainSubstring("clojure-lsp"))
		})
	})

	When("an entry sets an unknown readiness value", func() {
		It("names the invalid value", func() {
			cfg := valid()
			cfg.LanguageServers[0].Readiness = "eager"

			err := config.Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("eager"))
		})
	})

	DescribeTable("accepts every valid readiness value, including unset",
		func(readiness config.Readiness) {
			cfg := valid()
			cfg.LanguageServers[0].Readiness = readiness
			Expect(config.Validate(cfg)).To(Succeed())
		},
		Entry("unset", config.Readiness("")),
		Entry("progress", config.ReadinessProgress),
		Entry("handshake", config.ReadinessHandshake),
	)
})
