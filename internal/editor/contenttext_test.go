package editor

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// textResult is a tool answer of one text block, the shape the SDK renders
// every structured answer into.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

var _ = Describe("the answer a record carries", func() {
	It("carries a small answer whole and unmarked", func() {
		recorded := truncateForLog(contentText(textResult(`{"locations":[]}`)), toolResultBytesMax)

		Expect(recorded).To(Equal(`{"locations":[]}`))
	})

	It("carries an answer of exactly one record whole and unmarked", func() {
		answer := strings.Repeat("a", toolResultBytesMax)

		recorded := truncateForLog(contentText(textResult(answer)), toolResultBytesMax)

		Expect(recorded).To(Equal(answer), "nothing was lost, so nothing should claim it was")
	})

	It("marks an answer one byte past a record as cut", func() {
		answer := strings.Repeat("a", toolResultBytesMax+1)

		recorded := truncateForLog(contentText(textResult(answer)), toolResultBytesMax)

		Expect(recorded).To(HaveSuffix("[truncated]"))
	})

	// A language server decides how much a references or rename answer
	// holds, so reading the block whole and truncating afterwards would let
	// it decide how much Waythrough copies on every call in debug mode.
	It("reads no more than one record past the cap, however large the answer", func() {
		answer := strings.Repeat("b", toolResultBytesMax*500)

		read := contentText(textResult(answer))

		Expect(len(read)).To(Equal(toolResultBytesMax+1),
			"one byte past the cap: enough to know the answer was cut, and no more")
	})

	It("still marks that answer as cut once it reaches a record", func() {
		answer := strings.Repeat("b", toolResultBytesMax*500)

		recorded := truncateForLog(contentText(textResult(answer)), toolResultBytesMax)

		Expect(recorded).To(HaveSuffix("[truncated]"))
		Expect(len(recorded)).To(BeNumerically("<", toolResultBytesMax+len("…[truncated]")+1))
	})

	It("keeps a cut inside a multi-byte rune from reaching the record", func() {
		answer := strings.Repeat("é", toolResultBytesMax)

		recorded := truncateForLog(contentText(textResult(answer)), toolResultBytesMax)

		Expect(recorded).To(HaveSuffix("[truncated]"))
		Expect(strings.ToValidUTF8(recorded, "?")).To(Equal(recorded),
			"a record cut mid-rune would be invalid UTF-8 in the log")
	})
})
