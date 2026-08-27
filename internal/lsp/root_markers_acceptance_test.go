package lsp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

var _ = Describe("root markers", func() {
	It("starts at the requested file's highest-priority project root", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		project := filepath.Join(workspace, "application")
		sourceDirectory := filepath.Join(project, "src")
		Expect(os.MkdirAll(filepath.Join(sourceDirectory, ".git"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(workspace, "settings.gradle"), nil, 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(project, "build.gradle"), nil, 0o644)).To(Succeed())
		sourceFile := filepath.Join(sourceDirectory, "Main.fake")
		Expect(os.WriteFile(sourceFile, []byte("main"), 0o644)).To(Succeed())

		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		configPath := filepath.Join(GinkgoT().TempDir(), "waythrough.yaml")
		configuration := fmt.Sprintf(`language_servers:
  - name: fake
    command: %s
    args: [%s]
    readiness: handshake
    root_markers:
      - [settings.gradle, build.gradle]
      - .git
    filetypes:
      .fake: fake
`, strconv.Quote(fakelspPath), strconv.Quote("-initialize-log="+initializeLog))
		Expect(os.WriteFile(configPath, []byte(configuration), 0o644)).To(Succeed())

		cfg, err := config.Load(configPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(config.Validate(cfg)).To(Succeed())

		manager := lsp.NewManager(workspace, cfg.LanguageServers)
		Expect(manager.Start(context.Background())).To(Succeed())
		DeferCleanup(func() {
			Expect(manager.Shutdown(context.Background())).To(Succeed())
		})
		Expect(manager.Status("fake")).To(Equal(lsp.StatusIdle))

		_, err = manager.Definition(ctx, "fake", sourceFile, 1, 1)
		Expect(err).NotTo(HaveOccurred())

		data, err := os.ReadFile(initializeLog)
		Expect(err).NotTo(HaveOccurred())
		var params struct {
			RootURI string `json:"rootUri"`
		}
		Expect(json.Unmarshal(bytes.TrimSpace(data), &params)).To(Succeed())
		Expect(params.RootURI).To(Equal("file://" + filepath.ToSlash(project)))
	})

	It("falls back to the manager workspace when no marker matches", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		sourceDirectory := filepath.Join(workspace, "src")
		Expect(os.MkdirAll(sourceDirectory, 0o755)).To(Succeed())
		sourceFile := filepath.Join(sourceDirectory, "Main.fake")
		Expect(os.WriteFile(sourceFile, []byte("main"), 0o644)).To(Succeed())

		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		entry := fakeEntry("-initialize-log=" + initializeLog)
		entry.RootMarkers = config.RootMarkers{{"missing.project-marker"}}
		manager := lsp.NewManager(workspace, []config.LanguageServer{entry})
		Expect(manager.Start(context.Background())).To(Succeed())
		DeferCleanup(func() {
			Expect(manager.Shutdown(context.Background())).To(Succeed())
		})

		_, err := manager.Definition(ctx, "fake", sourceFile, 1, 1)
		Expect(err).NotTo(HaveOccurred())

		data, err := os.ReadFile(initializeLog)
		Expect(err).NotTo(HaveOccurred())
		var params struct {
			RootURI string `json:"rootUri"`
		}
		Expect(json.Unmarshal(bytes.TrimSpace(data), &params)).To(Succeed())
		Expect(params.RootURI).To(Equal("file://" + filepath.ToSlash(workspace)))
	})
})
