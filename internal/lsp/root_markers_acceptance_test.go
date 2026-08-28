package lsp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.lsp.dev/uri"

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
		configPath := filepath.Join(GinkgoT().TempDir(), ".waythrough.yaml")
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

	It("stops root discovery when the request is canceled", func() {
		workspace := GinkgoT().TempDir()
		sourceFile := filepath.Join(workspace, "Main.fake")
		Expect(os.WriteFile(sourceFile, []byte("main"), 0o644)).To(Succeed())

		entry := fakeEntry()
		entry.RootMarkers = config.RootMarkers{{"missing.project-marker"}}
		manager := lsp.NewManager(workspace, []config.LanguageServer{entry})
		Expect(manager.Start(context.Background())).To(Succeed())
		DeferCleanup(func() {
			Expect(manager.Shutdown(context.Background())).To(Succeed())
		})

		requestContext, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := manager.Definition(requestContext, "fake", sourceFile, 1, 1)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		Expect(manager.Status("fake")).To(Equal(lsp.StatusIdle))
	})

	It("keeps one configured root through concurrent start and restart", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		projectOne := filepath.Join(workspace, "one")
		projectTwo := filepath.Join(workspace, "two")
		Expect(os.MkdirAll(projectOne, 0o755)).To(Succeed())
		Expect(os.MkdirAll(projectTwo, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(projectOne, "go.mod"), nil, 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(projectTwo, "go.mod"), nil, 0o644)).To(Succeed())
		fileOne := filepath.Join(projectOne, "one.go")
		fileTwo := filepath.Join(projectTwo, "two.go")
		Expect(os.WriteFile(fileOne, []byte("package one"), 0o644)).To(Succeed())
		Expect(os.WriteFile(fileTwo, []byte("package two"), 0o644)).To(Succeed())

		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
		entry := config.Presets()[1]
		Expect(entry.Name).To(Equal("gopls"))
		entry.Command = fakelspPath
		entry.Args = []string{
			"-initialize-log=" + initializeLog,
			"-instance-log=" + instanceLog,
		}
		entry.Readiness = config.ReadinessHandshake
		manager := lsp.NewManager(workspace, []config.LanguageServer{entry}, lsp.WithDemandStart())
		Expect(manager.Start(context.Background())).To(Succeed())
		DeferCleanup(func() {
			Expect(manager.Shutdown(context.Background())).To(Succeed())
		})

		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := manager.Definition(ctx, "gopls", fileOne, 1, 1)
			results <- err
		}()
		go func() {
			<-start
			_, err := manager.Definition(ctx, "gopls", fileTwo, 1, 1)
			results <- err
		}()
		close(start)
		Expect(<-results).NotTo(HaveOccurred())
		Expect(<-results).NotTo(HaveOccurred())
		Expect(logLines(instanceLog)).To(HaveLen(1))

		initializeRecords := logLines(initializeLog)
		Expect(initializeRecords).To(HaveLen(1))
		var first struct {
			RootURI string `json:"rootUri"`
		}
		Expect(json.Unmarshal([]byte(initializeRecords[0]), &first)).To(Succeed())
		Expect(first.RootURI).To(Or(
			Equal(string(uri.File(projectOne))),
			Equal(string(uri.File(projectTwo))),
		))

		Expect(manager.Restart(ctx, "gopls")).To(Succeed())

		initializeRecords = logLines(initializeLog)
		Expect(initializeRecords).To(HaveLen(2))
		var restarted struct {
			RootURI string `json:"rootUri"`
		}
		Expect(json.Unmarshal([]byte(initializeRecords[1]), &restarted)).To(Succeed())
		Expect(restarted.RootURI).To(Equal(first.RootURI))
	})
})
