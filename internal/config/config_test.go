package config_test

import (
	"os"
	"path/filepath"
	"strings"

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
		path = filepath.Join(GinkgoT().TempDir(), ".waythrough.yaml")
	})

	When("the file holds a well-formed language-server entry", func() {
		BeforeEach(func() {
			writeFile(path, `
language_servers:
  - name: clojure-lsp
    command: clojure-lsp
    args: ["--verbose"]
    root_markers:
      - [deps.edn, project.clj]
      - .git
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
			Expect(entry.RootMarkers).To(Equal(config.RootMarkers{
				{"deps.edn", "project.clj"},
				{".git"},
			}))
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

	When("the file contains an unknown field", func() {
		BeforeEach(func() {
			writeFile(path, `
language_server:
  - name: gopls
    command: gopls
    filetypes:
      .go: go
`)
		})

		It("returns a parse error naming the field", func() {
			_, err := config.Load(path)
			Expect(err).To(MatchError(ContainSubstring("language_server")))
		})
	})

	When("a root marker is not a string", func() {
		BeforeEach(func() {
			writeFile(path, `
language_servers:
  - name: gopls
    command: gopls
    root_markers: [123]
    filetypes:
      .go: go
`)
		})

		It("rejects the value instead of converting it to a filename", func() {
			_, err := config.Load(path)
			Expect(err).To(MatchError(ContainSubstring("must be a string")))
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

	When("no language servers are configured", func() {
		It("rejects the unusable configuration", func() {
			err := config.Validate(config.Config{})
			Expect(err).To(MatchError(ContainSubstring("no language servers")))
		})
	})

	When("an entry is missing its name", func() {
		It("names the missing field and identifies the entry by index", func() {
			cfg := valid()
			cfg.LanguageServers[0].Name = ""

			err := config.Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("missing name"))
			// The entry's own name is what's missing, so unlike every other
			// validation error here, it must identify the entry by index.
			Expect(err.Error()).To(ContainSubstring("entry #0"))
		})
	})

	When("two entries share the same name", func() {
		It("names the duplicate", func() {
			cfg := valid()
			cfg.LanguageServers = append(cfg.LanguageServers, config.LanguageServer{
				Name:      "clojure-lsp",
				Command:   "clojure-lsp-alt",
				Filetypes: map[string]string{".cljc": "clojure"},
			})

			err := config.Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("clojure-lsp"))
			Expect(err.Error()).To(ContainSubstring("duplicate"))
		})
	})

	When("two entries claim the same filetype", func() {
		It("names the extension and both entries", func() {
			cfg := valid()
			cfg.LanguageServers = append(cfg.LanguageServers, config.LanguageServer{
				Name:      "clojure-lsp-alt",
				Command:   "clojure-lsp-alt",
				Filetypes: map[string]string{".clj": "clojure"},
			})

			err := config.Validate(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(".clj"))
			Expect(err.Error()).To(ContainSubstring("clojure-lsp"))
			Expect(err.Error()).To(ContainSubstring("clojure-lsp-alt"))
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

	DescribeTable("rejects unsafe root markers before a server starts",
		func(markers config.RootMarkers, message string) {
			cfg := valid()
			cfg.LanguageServers[0].RootMarkers = markers

			err := config.Validate(cfg)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("an empty priority group", config.RootMarkers{{}}, "empty group"),
		Entry("an empty marker", config.RootMarkers{{""}}, "empty marker"),
		Entry("an absolute marker", config.RootMarkers{{filepath.Join(
			string(os.PathSeparator), "project")}}, "absolute"),
		Entry("parent traversal", config.RootMarkers{{"project/../other"}}, "parent traversal"),
		Entry("too many markers", config.RootMarkers{make([]string, 65)}, "maximum is 64"),
		Entry("an overlong marker", config.RootMarkers{{strings.Repeat("a", 256)}},
			"maximum length is 255"),
	)
})
