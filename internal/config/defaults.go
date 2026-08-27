package config

// Default returns Waythrough's built-in configuration for common language
// servers. Each call owns its maps and slices, so a caller may customize the
// result without changing a later call's defaults.
func Default() Config {
	return Config{LanguageServers: []LanguageServer{
		{
			Name:    "clojure-lsp",
			Command: "clojure-lsp",
			RootMarkers: RootMarkers{
				{"project.clj"},
				{"deps.edn"},
				{"build.boot"},
				{"shadow-cljs.edn"},
				{".git"},
				{"bb.edn"},
			},
			Filetypes: map[string]string{
				".clj":  "clojure",
				".cljc": "clojure",
				".cljs": "clojurescript",
				".edn":  "clojure",
			},
		},
		{
			Name:        "gopls",
			Command:     "gopls",
			RootMarkers: RootMarkers{{"go.work"}, {"go.mod"}, {".git"}},
			Filetypes:   map[string]string{".go": "go"},
		},
		{
			Name:    "typescript-language-server",
			Command: "typescript-language-server",
			Args:    []string{"--stdio"},
			RootMarkers: RootMarkers{
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
		{
			Name:        "rust-analyzer",
			Command:     "rust-analyzer",
			RootMarkers: RootMarkers{{"Cargo.toml"}, {"rust-project.json"}, {".git"}},
			Filetypes:   map[string]string{".rs": "rust"},
		},
		{
			Name:    "pyright-langserver",
			Command: "pyright-langserver",
			Args:    []string{"--stdio"},
			RootMarkers: RootMarkers{
				{"pyrightconfig.json"},
				{"pyproject.toml"},
				{"setup.py"},
				{"setup.cfg"},
				{"requirements.txt"},
				{"Pipfile"},
				{".git"},
			},
			Filetypes: map[string]string{
				".py":  "python",
				".pyi": "python",
			},
		},
	}}
}
