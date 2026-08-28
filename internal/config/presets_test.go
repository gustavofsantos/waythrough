package config_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
)

var _ = Describe("Presets", func() {
	It("provides the supported common language-server presets", func() {
		cfg := config.Config{LanguageServers: config.Presets()}

		Expect(config.Validate(cfg)).To(Succeed())
		Expect(cfg.LanguageServers).To(ConsistOf(
			config.LanguageServer{
				Name:    "clojure-lsp",
				Command: "clojure-lsp",
				RootMarkers: config.RootMarkers{
					{"project.clj"}, {"deps.edn"}, {"build.boot"},
					{"shadow-cljs.edn"}, {".git"}, {"bb.edn"},
				},
				Filetypes: map[string]string{
					".clj":  "clojure",
					".cljc": "clojure",
					".cljs": "clojurescript",
					".edn":  "clojure",
				},
			},
			config.LanguageServer{
				Name:        "gopls",
				Command:     "gopls",
				RootMarkers: config.RootMarkers{{"go.work"}, {"go.mod"}, {".git"}},
				Filetypes:   map[string]string{".go": "go"},
			},
			config.LanguageServer{
				Name:    "typescript-language-server",
				Command: "typescript-language-server",
				Args:    []string{"--stdio"},
				RootMarkers: config.RootMarkers{
					{"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb", "bun.lock"},
					{".git"},
				},
				Filetypes: map[string]string{
					".cjs": "javascript",
					".cts": "typescript",
					".js":  "javascript",
					".jsx": "javascriptreact",
					".mjs": "javascript",
					".mts": "typescript",
					".ts":  "typescript",
					".tsx": "typescriptreact",
				},
			},
			config.LanguageServer{
				Name:        "rust-analyzer",
				Command:     "rust-analyzer",
				RootMarkers: config.RootMarkers{{"Cargo.toml"}, {"rust-project.json"}, {".git"}},
				Filetypes:   map[string]string{".rs": "rust"},
			},
			config.LanguageServer{
				Name:    "pyright-langserver",
				Command: "pyright-langserver",
				Args:    []string{"--stdio"},
				RootMarkers: config.RootMarkers{
					{"pyrightconfig.json"}, {"pyproject.toml"}, {"setup.py"},
					{"setup.cfg"}, {"requirements.txt"}, {"Pipfile"}, {".git"},
				},
				Filetypes: map[string]string{
					".py":  "python",
					".pyi": "python",
				},
			},
		))
	})

	It("returns presets the caller can mutate without changing later presets", func() {
		first := config.Presets()
		first[0].Command = "company-clojure-lsp"
		first[0].Filetypes[".clj"] = "company-clojure"
		first[1].RootMarkers[0][0] = "company.go.work"
		first[2].Args[0] = "--socket"

		second := config.Presets()
		Expect(second[0].Command).To(Equal("clojure-lsp"))
		Expect(second[0].Filetypes[".clj"]).To(Equal("clojure"))
		Expect(second[1].RootMarkers[0][0]).To(Equal("go.work"))
		Expect(second[2].Args).To(Equal([]string{"--stdio"}))
	})
})
