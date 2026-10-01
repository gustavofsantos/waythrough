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

	// A worktree is a second copy of a project beside the first, so a session
	// that reads both must reach two processes, each indexing its own copy.
	// One process answering for both would answer for the copy it loaded,
	// and say nothing about the other.
	It("runs one process per project root, and restarts every one", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		projectOne := filepath.Join(workspace, "one")
		projectTwo := filepath.Join(workspace, "two")
		fileOne := goProjectFile(projectOne, "one")
		fileTwo := goProjectFile(projectTwo, "two")

		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
		manager := goplsManager(workspace, initializeLog, instanceLog)

		// Two concurrent first requests per root prove that a root's first
		// use is single-flight: four requests, two processes.
		start := make(chan struct{})
		results := make(chan error, 4)
		for _, file := range []string{fileOne, fileTwo, fileOne, fileTwo} {
			go func() {
				<-start
				_, err := manager.Definition(ctx, "gopls", file, 1, 1)
				results <- err
			}()
		}
		close(start)
		for range 4 {
			Expect(<-results).NotTo(HaveOccurred())
		}
		Expect(logLines(instanceLog)).To(HaveLen(2))
		Expect(initializeRoots(initializeLog)).To(ConsistOf(
			string(uri.File(projectOne)), string(uri.File(projectTwo))))

		Expect(manager.Restart(ctx, "gopls")).To(Succeed())

		Expect(logLines(instanceLog)).To(HaveLen(4))
		Expect(initializeRoots(initializeLog)).To(ConsistOf(
			string(uri.File(projectOne)), string(uri.File(projectTwo)),
			string(uri.File(projectOne)), string(uri.File(projectTwo))))
	})

	It("refuses a file outside the workspace that no root marker claims", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		outside := filepath.Join(GinkgoT().TempDir(), "Main.fake")
		Expect(os.WriteFile(outside, []byte("main"), 0o644)).To(Succeed())

		instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
		entry := fakeEntry("-instance-log=" + instanceLog)
		entry.RootMarkers = config.RootMarkers{{"missing.project-marker"}}
		manager := lsp.NewManager(workspace, []config.LanguageServer{entry}, lsp.WithDemandStart())
		Expect(manager.Start(context.Background())).To(Succeed())
		DeferCleanup(func() {
			Expect(manager.Shutdown(context.Background())).To(Succeed())
		})

		_, err := manager.Definition(ctx, "fake", outside, 1, 1)
		Expect(err).To(MatchError(ContainSubstring("outside the workspace")))
		Expect(err).To(MatchError(ContainSubstring(filepath.Dir(outside))))
		Expect(instanceLog).NotTo(BeAnExistingFile(),
			"a refused file must not start a server anywhere")
	})

	It("starts a worktree nested inside a repository on its own", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		repositoryFile := goProjectFile(workspace, "main")
		worktree := filepath.Join(workspace, ".claude", "worktrees", "feature")
		worktreeFile := goProjectFile(worktree, "feature")

		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
		manager := goplsManager(workspace, initializeLog, instanceLog)

		for _, file := range []string{repositoryFile, worktreeFile} {
			_, err := manager.Definition(ctx, "gopls", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(initializeRoots(initializeLog)).To(ConsistOf(
			string(uri.File(workspace)), string(uri.File(worktree))))
	})

	It("serves a nested module from its repository's process", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		repositoryFile := goProjectFile(workspace, "main")
		nested := filepath.Join(workspace, "tools")
		Expect(os.MkdirAll(nested, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(nested, "go.mod"), nil, 0o644)).To(Succeed())
		nestedFile := filepath.Join(nested, "tools.go")
		Expect(os.WriteFile(nestedFile, []byte("package tools"), 0o644)).To(Succeed())

		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
		manager := goplsManager(workspace, initializeLog, instanceLog)

		for _, file := range []string{repositoryFile, nestedFile} {
			_, err := manager.Definition(ctx, "gopls", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(initializeRoots(initializeLog)).To(ConsistOf(string(uri.File(workspace))))
	})

	// A module cache holds go.mod files but no checkout. An agent reaches it
	// by following a definition out of its project, so the process it came
	// from answers, rather than a new one that knows nothing of the project.
	It("serves a dependency outside every checkout from the last process", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		projectFile := goProjectFile(workspace, "main")
		dependency := filepath.Join(GinkgoT().TempDir(), "mod", "example.com", "lib@v1.0.0")
		Expect(os.MkdirAll(dependency, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dependency, "go.mod"), nil, 0o644)).To(Succeed())
		dependencyFile := filepath.Join(dependency, "lib.go")
		Expect(os.WriteFile(dependencyFile, []byte("package lib"), 0o644)).To(Succeed())

		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
		manager := goplsManager(workspace, initializeLog, instanceLog)

		for _, file := range []string{projectFile, dependencyFile} {
			_, err := manager.Definition(ctx, "gopls", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(initializeRoots(initializeLog)).To(ConsistOf(string(uri.File(workspace))))
	})

	It("refuses a project root past the per-server limit", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
		manager := goplsManager(workspace, initializeLog, instanceLog)

		const rootsMax = 4
		for index := range rootsMax {
			name := "project" + strconv.Itoa(index)
			file := goProjectFile(filepath.Join(workspace, name), name)
			_, err := manager.Definition(ctx, "gopls", file, 1, 1)
			Expect(err).NotTo(HaveOccurred())
		}

		extra := goProjectFile(filepath.Join(workspace, "extra"), "extra")
		_, err := manager.Definition(ctx, "gopls", extra, 1, 1)
		Expect(err).To(MatchError(ContainSubstring("the maximum")))
		Expect(err).To(MatchError(ContainSubstring(filepath.Join(workspace, "extra"))))
		Expect(logLines(instanceLog)).To(HaveLen(rootsMax))
	})

	It("starts nothing for a request that arrives after shutdown", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		initializeLog := filepath.Join(GinkgoT().TempDir(), "initialize.jsonl")
		instanceLog := filepath.Join(GinkgoT().TempDir(), "instances.log")
		manager := goplsManager(workspace, initializeLog, instanceLog)
		Expect(manager.Shutdown(context.Background())).To(Succeed())

		file := goProjectFile(filepath.Join(workspace, "late"), "late")
		_, err := manager.Definition(ctx, "gopls", file, 1, 1)
		Expect(err).To(MatchError(ContainSubstring("shutting down")))
		Expect(instanceLog).NotTo(BeAnExistingFile())
	})
})

// goProjectFile creates a git checkout at directory holding a Go module with
// one source file, and returns that file's path. The .git is a file, as in a
// git worktree, which marks a checkout as well as a directory does.
func goProjectFile(directory, packageName string) string {
	Expect(os.MkdirAll(directory, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(directory, ".git"), nil, 0o644)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(directory, "go.mod"), nil, 0o644)).To(Succeed())
	file := filepath.Join(directory, packageName+".go")
	Expect(os.WriteFile(file, []byte("package "+packageName), 0o644)).To(Succeed())
	return file
}

// goplsManager starts a demand-start manager whose gopls preset runs the
// fake server, which records each initialize and each process it starts.
func goplsManager(workspace, initializeLog, instanceLog string) *lsp.Manager {
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
	return manager
}

// initializeRoots lists the rootUri of every initialize the fake recorded.
func initializeRoots(initializeLog string) []string {
	records := logLines(initializeLog)
	roots := make([]string, len(records))
	for index, record := range records {
		var params struct {
			RootURI string `json:"rootUri"`
		}
		Expect(json.Unmarshal([]byte(record), &params)).To(Succeed())
		roots[index] = params.RootURI
	}
	return roots
}
