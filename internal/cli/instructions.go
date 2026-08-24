package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// agentInstructions is the block a user pastes into the rules file their
// coding agent already reads: AGENTS.md, CLAUDE.md, or the equivalent.
//
// Every line here costs context on every request that file is loaded for,
// so this is a tool-selection table rather than documentation: the editor
// commands a model already knows, mapped to the tool that answers each one,
// plus the one habit it has to unlearn. README.md carries the prose.
//
// Invariant: it names every tool internal/editor registers, and no other
// name. A block that omits a tool hides that tool; a block that names one
// that does not exist sends an agent after a call that can only fail.
// instructions_test.go checks both directions against a live server.
const agentInstructions = "## Code navigation: use Waythrough MCP, not grep\n" +
	"\n" +
	"Waythrough runs this project's language servers. Its MCP tools answer from\n" +
	"the symbol graph, the way an IDE does. Text search matches strings; these\n" +
	"resolve symbols. Reach for them first, and never trace a symbol by reading\n" +
	"files.\n" +
	"\n" +
	"- Go to definition → `get_definition`\n" +
	"- Find all references → `list_references`\n" +
	"- Rename across the project → `rename_symbol` (returns edits; you apply them)\n" +
	"- Which argument goes here → `signature_help`\n" +
	"- Errors and warnings in a file → `get_diagnostics`\n" +
	"- Answers stopped matching the code → `restart_server`\n" +
	"\n" +
	"Each takes `file`, `line`, `column` — 1-based, on the symbol itself. Search\n" +
	"for a name when you need one; resolve it with these. A file type with no\n" +
	"configured language server has no answers here.\n"

func newInstructionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "instructions",
		Short: "Print agent instructions to paste into your coding tool",
		Long: "Print a short block of instructions for a coding agent, to paste " +
			"into the rules file it already reads — AGENTS.md, CLAUDE.md, or your " +
			"tool's equivalent. It tells the agent to navigate this codebase with " +
			"Waythrough's MCP tools rather than by searching text.",
		Args: cobra.NoArgs,
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runInstructions(cmd.OutOrStdout())
	}

	return cmd
}

// runInstructions writes the block to out, which is stdout so that a user
// can pipe or append it straight into a rules file.
//
// The write is the whole command, so its error is the whole failure path:
// a closed or full destination must exit non-zero rather than report
// success on output that nobody received.
func runInstructions(out io.Writer) error {
	if _, err := io.WriteString(out, agentInstructions); err != nil {
		return fmt.Errorf("write instructions: %w", err)
	}

	return nil
}
