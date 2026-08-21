package config

// Default returns Waythrough's built-in configuration for common language
// servers. Each call owns its maps and slices, so a caller may customize the
// result without changing a later call's defaults.
func Default() Config {
	return Config{LanguageServers: []LanguageServer{
		{
			Name:    "clojure-lsp",
			Command: "clojure-lsp",
			Filetypes: map[string]string{
				".clj":  "clojure",
				".cljc": "clojure",
				".cljs": "clojurescript",
				".edn":  "clojure",
			},
		},
		{
			Name:      "gopls",
			Command:   "gopls",
			Filetypes: map[string]string{".go": "go"},
		},
		{
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
		{
			Name:      "rust-analyzer",
			Command:   "rust-analyzer",
			Filetypes: map[string]string{".rs": "rust"},
		},
		{
			Name:    "pyright-langserver",
			Command: "pyright-langserver",
			Args:    []string{"--stdio"},
			Filetypes: map[string]string{
				".py":  "python",
				".pyi": "python",
			},
		},
	}}
}
