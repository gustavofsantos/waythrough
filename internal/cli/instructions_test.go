package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gustavofsantos/waythrough/internal/cli"
	"github.com/gustavofsantos/waythrough/internal/config"
	"github.com/gustavofsantos/waythrough/internal/editor"
	"github.com/gustavofsantos/waythrough/internal/lsp"
)

// listToolsTimeout bounds the in-memory MCP handshake below, so a session
// that never answers fails this spec instead of hanging the suite.
const listToolsTimeout = 10 * time.Second

const (
	startMarker = "<!-- waythrough:start -->"
	endMarker   = "<!-- waythrough:end -->"
)

// backtickedName matches one `name` in the printed block, which backticks
// tool names and argument names and nothing else. Reading the block back
// this way is what lets a spec check it against the live server instead of
// against a second copy of the same list, which would drift with it.
var backtickedName = regexp.MustCompile("`([a-z_]+)`")

// toolSchema is the part of an advertised tool a spec asserts on: its name,
// and the arguments it accepts.
type toolSchema struct {
	name      string
	arguments []string
}

// advertisedTools asks a live editor MCP server which tools it registers
// and what each one accepts, over an in-memory transport. It starts no
// language server: listing tools never reaches one, so the manager here is
// built and left unstarted on purpose.
func advertisedTools(ctx context.Context) []toolSchema {
	cfg := config.Config{LanguageServers: config.Presets()}
	manager := lsp.NewManager(GinkgoT().TempDir(), cfg.LanguageServers)
	server := editor.New(manager, cfg, slog.New(slog.DiscardHandler))

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := server.Connect(ctx, serverTransport, nil)
	Expect(err).NotTo(HaveOccurred())

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = session.Close() })

	result, err := session.ListTools(ctx, nil)
	Expect(err).NotTo(HaveOccurred())

	tools := make([]toolSchema, 0, len(result.Tools))
	for _, tool := range result.Tools {
		tools = append(tools, toolSchema{
			name:      tool.Name,
			arguments: schemaProperties(tool.InputSchema),
		})
	}
	return tools
}

// schemaProperties reads the property names out of a tool's input schema.
// It goes through JSON rather than through a concrete schema type because
// the schema crosses the MCP wire as one, and this is the same view of it
// that the coding agent on the other end gets.
func schemaProperties(schema any) []string {
	encoded, err := json.Marshal(schema)
	Expect(err).NotTo(HaveOccurred())

	var decoded struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())

	names := make([]string, 0, len(decoded.Properties))
	for name := range decoded.Properties {
		names = append(names, name)
	}
	return names
}

// claimedArguments reads the block back the way an agent does: a bullet
// names one tool, and the arguments that tool takes are the ones the bullet
// adds to the ones its section header declared. It returns tool name to
// argument set.
//
// The two shapes it recognizes are the block's whole grammar: a line ending
// in a colon opens a section and declares the arguments shared by the tools
// under it, and a line starting with a dash names one tool in the section
// it falls under, first backticked name first.
func claimedArguments(instructions string) map[string][]string {
	claimed := make(map[string][]string)
	var sectionArguments []string

	for _, line := range strings.Split(instructions, "\n") {
		names := backtickedNames(line)
		switch {
		case strings.HasSuffix(line, ":"):
			sectionArguments = names
		case strings.HasPrefix(line, "- ") && len(names) > 0:
			claimed[names[0]] = append(append([]string{}, sectionArguments...), names[1:]...)
		}
	}
	return claimed
}

func backtickedNames(line string) []string {
	matches := backtickedName.FindAllStringSubmatch(line, -1)

	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

// printInstructions runs the command the way a user reads it, and returns
// the block it printed.
func printInstructions() string {
	stdout := &bytes.Buffer{}
	Expect(cli.Execute([]string{"instructions"}, stdout, &bytes.Buffer{})).To(Equal(0))

	return stdout.String()
}

var _ = Describe("waythrough instructions", func() {
	var (
		stdout *bytes.Buffer
		stderr *bytes.Buffer
	)

	BeforeEach(func() {
		stdout = &bytes.Buffer{}
		stderr = &bytes.Buffer{}
	})

	It("prints the block on stdout and exits zero", func() {
		Expect(cli.Execute([]string{"instructions"}, stdout, stderr)).To(Equal(0))
		Expect(stderr.String()).To(BeEmpty())
		Expect(stdout.String()).To(HaveSuffix("\n"))
	})

	It("tells the agent to prefer the tools over searching text", func() {
		Expect(cli.Execute([]string{"instructions"}, stdout, stderr)).To(Equal(0))
		Expect(stdout.String()).To(ContainSubstring("grep"))
	})

	It("takes no config flag, so it works before a project has a config file", func() {
		Expect(cli.Execute([]string{"instructions", "--config", "/nonexistent.yaml"},
			stdout, stderr)).NotTo(Equal(0))
		Expect(cli.Execute([]string{"instructions"}, stdout, stderr)).To(Equal(0))
		Expect(stdout.String()).NotTo(BeEmpty())
	})

	It("rejects arguments rather than ignoring them", func() {
		Expect(cli.Execute([]string{"instructions", "extra"}, stdout, stderr)).NotTo(Equal(0))
		Expect(stdout.String()).To(BeEmpty())
	})

	// The block is what a coding agent acts on, so a name in it that no
	// server registers sends the agent after a call that can only fail, and
	// a tool missing from it stays invisible.
	It("names every registered MCP tool, and no other tool", func() {
		ctx, cancel := context.WithTimeout(context.Background(), listToolsTimeout)
		DeferCleanup(cancel)

		claimed := claimedArguments(printInstructions())
		advertised := advertisedTools(ctx)
		Expect(advertised).NotTo(BeEmpty())

		advertisedNames := make([]string, 0, len(advertised))
		for _, tool := range advertised {
			advertisedNames = append(advertisedNames, tool.name)
			Expect(claimed).To(HaveKey(tool.name), "tool missing from the instructions")
		}
		for name := range claimed {
			Expect(advertisedNames).To(ContainElement(name), "instructions name an unknown tool")
		}
	})

	// An agent follows an argument list literally. A list that is right for
	// the position tools and wrong for the rest is what teaches a call that
	// the server can only reject, so each tool is pinned to its own schema
	// rather than to a shared sentence.
	It("lists each tool with exactly the arguments its schema accepts", func() {
		ctx, cancel := context.WithTimeout(context.Background(), listToolsTimeout)
		DeferCleanup(cancel)

		claimed := claimedArguments(printInstructions())
		for _, tool := range advertisedTools(ctx) {
			Expect(claimed[tool.name]).To(ConsistOf(tool.arguments), tool.name)
		}
	})
})

var _ = Describe("waythrough instructions --write", func() {
	var (
		rulesPath string
		stdout    *bytes.Buffer
		stderr    *bytes.Buffer
	)

	BeforeEach(func() {
		rulesPath = filepath.Join(GinkgoT().TempDir(), "AGENTS.md")
		stdout = &bytes.Buffer{}
		stderr = &bytes.Buffer{}
	})

	write := func() int {
		return cli.Execute([]string{"instructions", "--write", rulesPath}, stdout, stderr)
	}

	readRules := func() string {
		content, err := os.ReadFile(rulesPath)
		Expect(err).NotTo(HaveOccurred())

		return string(content)
	}

	When("the file does not exist yet", func() {
		It("creates it holding exactly the block", func() {
			Expect(write()).To(Equal(0))
			Expect(readRules()).To(Equal(printInstructions()))
			Expect(stdout.String()).To(BeEmpty())
		})
	})

	When("the file exists and has no block", func() {
		BeforeEach(func() {
			writeConfigFile(rulesPath, "# House rules\n\nRun the tests.\n")
		})

		It("appends the block and keeps what was already there", func() {
			Expect(write()).To(Equal(0))

			rules := readRules()
			Expect(rules).To(HavePrefix("# House rules\n\nRun the tests.\n\n"))
			Expect(rules).To(HaveSuffix(printInstructions()))
		})
	})

	When("the file already holds a block from an earlier run", func() {
		BeforeEach(func() {
			writeConfigFile(rulesPath, "# House rules\n")
			Expect(write()).To(Equal(0))
		})

		// This is the upgrade path: appending a second copy would leave the
		// tools of the older version advertised alongside the new ones.
		It("replaces that block instead of appending another", func() {
			before := readRules()

			Expect(write()).To(Equal(0))
			Expect(readRules()).To(Equal(before))
			Expect(strings.Count(readRules(), startMarker)).To(Equal(1))
		})

		It("keeps the file's own permissions", func() {
			Expect(os.Chmod(rulesPath, 0o600)).To(Succeed())

			Expect(write()).To(Equal(0))

			info, err := os.Stat(rulesPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
		})
	})

	// Every one of these shapes has more than one plausible repair, so the
	// command refuses it and says so rather than editing a hand-maintained
	// file into a shape its owner did not choose.
	DescribeTable("refusing a file whose markers are not one well-ordered pair",
		func(content string) {
			writeConfigFile(rulesPath, content)

			Expect(write()).NotTo(Equal(0))
			Expect(stderr.String()).To(ContainSubstring(rulesPath))
			Expect(readRules()).To(Equal(content), "the file must be left untouched")
		},
		Entry("two blocks, as an earlier append-only upgrade left behind",
			startMarker+"\nold\n"+endMarker+"\n\n"+startMarker+"\nnewer\n"+endMarker+"\n"),
		Entry("a start marker with no end", "# Rules\n\n"+startMarker+"\nhalf a block\n"),
		Entry("an end marker with no start", "# Rules\n\nhalf a block\n"+endMarker+"\n"),
		Entry("the pair in the wrong order", endMarker+"\nbackwards\n"+startMarker+"\n"),
	)

	When("the path is a directory", func() {
		It("refuses it rather than trying to splice one", func() {
			Expect(cli.Execute([]string{"instructions", "--write", filepath.Dir(rulesPath)},
				stdout, stderr)).NotTo(Equal(0))
		})
	})

	When("the file is larger than any rules file", func() {
		BeforeEach(func() {
			writeConfigFile(rulesPath, "# House rules\n")
			// Sparse: the bound is checked against the reported size, before
			// any read, so this costs no real bytes on disk.
			Expect(os.Truncate(rulesPath, (1<<20)+1)).To(Succeed())
		})

		It("refuses it rather than reading it into memory", func() {
			Expect(write()).NotTo(Equal(0))
			Expect(stderr.String()).To(ContainSubstring(rulesPath))
		})
	})
})
