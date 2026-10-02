package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// repositoryFile reads a file by its path from the repository root. Go runs
// a package's tests in the package's own directory, two levels below it.
func repositoryFile(path string) string {
	content, err := os.ReadFile(filepath.Join("..", "..", path))
	Expect(err).NotTo(HaveOccurred())
	return string(content)
}

// skillFrontmatter splits a SKILL.md into its frontmatter fields and its
// body. The frontmatter here is flat "key: value" lines, which is all the
// skill uses.
func skillFrontmatter(skill string) (map[string]string, string) {
	rest, found := strings.CutPrefix(skill, "---\n")
	Expect(found).To(BeTrue(), "a skill opens with its frontmatter")
	header, body, found := strings.Cut(rest, "\n---\n")
	Expect(found).To(BeTrue(), "a skill's frontmatter closes with ---")

	fields := make(map[string]string)
	for _, line := range strings.Split(header, "\n") {
		key, value, found := strings.Cut(line, ": ")
		Expect(found).To(BeTrue(), "frontmatter line %q", line)
		fields[key] = value
	}
	return fields, body
}

// The skill is what steers Claude to the tools, so it drifts the same way
// the instructions block can: a tool it omits stays unused, and a tool it
// grants that no server registers is a call that can only fail.
var _ = Describe("the Claude Code plugin's skill", func() {
	It("grants and names every registered MCP tool, and no other", func() {
		ctx, cancel := context.WithTimeout(context.Background(), listToolsTimeout)
		DeferCleanup(cancel)
		fields, body := skillFrontmatter(repositoryFile("skills/waythrough/SKILL.md"))
		Expect(fields).To(HaveKeyWithValue("name", "waythrough"))

		expected := make([]string, 0)
		for _, tool := range advertisedTools(ctx) {
			expected = append(expected, "mcp__waythrough__"+tool.name)
			Expect(body).To(ContainSubstring("`"+tool.name+"`"),
				"the skill never names %s", tool.name)
		}
		Expect(strings.Split(fields["allowed-tools"], ", ")).To(ConsistOf(expected))
	})

	It("is published under one name by the plugin and its marketplace", func() {
		var plugin struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		Expect(json.Unmarshal([]byte(repositoryFile(".claude-plugin/plugin.json")), &plugin)).
			To(Succeed())
		var marketplace struct {
			Plugins []struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			} `json:"plugins"`
		}
		Expect(json.Unmarshal([]byte(repositoryFile(".claude-plugin/marketplace.json")),
			&marketplace)).To(Succeed())

		Expect(plugin.Name).To(Equal("waythrough"))
		Expect(plugin.Version).NotTo(BeEmpty())
		Expect(marketplace.Plugins).To(HaveLen(1))
		Expect(marketplace.Plugins[0].Name).To(Equal(plugin.Name))
		Expect(marketplace.Plugins[0].Source).To(Equal("./"))
	})
})
