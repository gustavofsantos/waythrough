package config_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
)

var _ = Describe("Default", func() {
	It("configures the supported common language servers", func() {
		cfg := config.Default()

		Expect(config.Validate(cfg)).To(Succeed())
		Expect(cfg.LanguageServers).To(ConsistOf(
			config.LanguageServer{
				Name:    "clojure-lsp",
				Command: "clojure-lsp",
				Filetypes: map[string]string{
					".clj":  "clojure",
					".cljc": "clojure",
					".cljs": "clojurescript",
					".edn":  "clojure",
				},
			},
			config.LanguageServer{
				Name:      "gopls",
				Command:   "gopls",
				Filetypes: map[string]string{".go": "go"},
			},
			config.LanguageServer{
				Name:    "typescript-language-server",
				Command: "typescript-language-server",
				Args:    []string{"--stdio"},
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
				Name:      "rust-analyzer",
				Command:   "rust-analyzer",
				Filetypes: map[string]string{".rs": "rust"},
			},
			config.LanguageServer{
				Name:    "pyright-langserver",
				Command: "pyright-langserver",
				Args:    []string{"--stdio"},
				Filetypes: map[string]string{
					".py":  "python",
					".pyi": "python",
				},
			},
		))
	})

	It("returns configuration the caller can mutate without changing later defaults", func() {
		first := config.Default()
		first.LanguageServers[0].Command = "company-clojure-lsp"
		first.LanguageServers[0].Filetypes[".clj"] = "company-clojure"
		first.LanguageServers[2].Args[0] = "--socket"

		second := config.Default()
		Expect(second.LanguageServers[0].Command).To(Equal("clojure-lsp"))
		Expect(second.LanguageServers[0].Filetypes[".clj"]).To(Equal("clojure"))
		Expect(second.LanguageServers[2].Args).To(Equal([]string{"--stdio"}))
	})
})
