package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// The markers delimit the block inside a rules file that this command owns,
// so a later run replaces the block it wrote instead of appending a second
// copy. They are HTML comments because every rules file is Markdown, where
// a comment renders as nothing and costs the agent almost no context.
const (
	instructionsStartMarker = "<!-- waythrough:start -->"
	instructionsEndMarker   = "<!-- waythrough:end -->"
)

// agentInstructions is the block a user pastes into the rules file their
// coding agent already reads: AGENTS.md, CLAUDE.md, or the equivalent.
//
// Every line here costs context on every request that rules file is loaded
// for, so this is a tool-selection table rather than documentation: the
// editor commands a model already knows, mapped to the tool that answers
// each one, plus the one habit it has to unlearn. README.md carries the
// prose.
//
// Two invariants hold, both checked in instructions_test.go against a live
// server rather than against a copied list, which would drift alongside the
// block it is meant to pin:
//
//   - It names every tool internal/editor registers, and no other name. A
//     block that omits a tool hides that tool; a block that names one that
//     does not exist sends an agent after a call that can only fail.
//   - Each tool is listed with exactly the arguments it accepts. An agent
//     follows this literally, so an argument list that is right for most
//     tools and wrong for the rest is what teaches the invalid call.
const agentInstructions = instructionsStartMarker + "\n" +
	"## Code navigation: use Waythrough MCP, not grep\n" +
	"\n" +
	"Waythrough runs this project's language servers. Its MCP tools answer from\n" +
	"the symbol graph, the way an IDE does. Text search matches strings; these\n" +
	"resolve symbols. Reach for them first, and never trace a symbol by reading\n" +
	"files.\n" +
	"\n" +
	"These take `file`, `line`, `column` — 1-based, on the symbol itself:\n" +
	"\n" +
	"- Go to definition → `get_definition`\n" +
	"- Find all references → `list_references`\n" +
	"- Which argument goes here → `signature_help`\n" +
	"- Rename across the project → `rename_symbol`, plus `new_name`\n" +
	"  (returns edits; you apply them)\n" +
	"\n" +
	"These do not:\n" +
	"\n" +
	"- Errors and warnings in a file → `get_diagnostics` (`file`)\n" +
	"- Answers stopped matching the code → `restart_server` (`server`)\n" +
	"\n" +
	"Search for a name when you need one; resolve it with these. A file type\n" +
	"with no configured language server has no answers here.\n" +
	instructionsEndMarker + "\n"

// instructionsFileSizeMax bounds what --write reads into memory. A rules
// file is prose measured in kilobytes, so a file past this bound is a
// mistyped path rather than a target, and saying so beats splicing a block
// into whatever it actually is.
const instructionsFileSizeMax = 1 << 20

// instructionsFileMode is the permission a rules file gets when --write
// creates it. An existing file keeps its own.
const instructionsFileMode = 0o644

func newInstructionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "instructions",
		Short: "Print agent instructions to paste into your coding tool",
		Long: "Print a short block of instructions for a coding agent, for the " +
			"rules file it already reads — AGENTS.md, CLAUDE.md, or your tool's " +
			"equivalent. It tells the agent to navigate this codebase with " +
			"Waythrough's MCP tools rather than by searching text.\n\n" +
			"With --write, update that file in place instead of printing: the " +
			"block is appended once, and replaced on every run after that, so an " +
			"upgrade never leaves a stale copy behind.",
		Args: cobra.NoArgs,
	}

	writePath := cmd.Flags().String("write", "",
		"update this rules file in place instead of printing to stdout")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if *writePath == "" {
			return runInstructions(cmd.OutOrStdout())
		}
		return runInstructionsWrite(*writePath)
	}

	return cmd
}

// runInstructions writes the block to out, which is stdout so that a user
// can pipe or read it before deciding where it goes.
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

// runInstructionsWrite puts the block into the rules file at path: appended
// the first time, and replaced in place on every run after that.
//
// It is idempotent by construction — two runs of the same build leave the
// same bytes — because the alternative a user reaches for otherwise is an
// append that duplicates the block, and a duplicate keeps advertising the
// tools of the older version after an upgrade renames or removes one.
func runInstructionsWrite(path string) error {
	existing, mode, err := readInstructionsTarget(path)
	if err != nil {
		return err
	}

	updated, err := spliceInstructions(existing)
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}

	return writeFileAtomically(path, updated, mode)
}

// readInstructionsTarget reads the rules file to update, and reports the
// permission its rewrite must carry. A path that does not exist yet is not
// an error: it is the first run, and reads as an empty file to append to.
func readInstructionsTarget(path string) (string, os.FileMode, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", instructionsFileMode, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("check %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > instructionsFileSizeMax {
		return "", 0, fmt.Errorf("%s is larger than %d bytes, which no rules file is",
			path, instructionsFileSizeMax)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}
	return string(content), info.Mode().Perm(), nil
}

// spliceInstructions returns existing with the block put in place: replacing
// the marked region when there is one, appended when there is none.
//
// A file whose markers do not form exactly one well-ordered pair is refused
// rather than repaired. Every such shape — a lone marker, a reversed pair,
// two blocks from an earlier append — has more than one plausible fix, and
// guessing at one edits a file the user maintains by hand.
func spliceInstructions(existing string) (string, error) {
	starts := strings.Count(existing, instructionsStartMarker)
	ends := strings.Count(existing, instructionsEndMarker)
	if starts == 0 && ends == 0 {
		return appendInstructions(existing), nil
	}
	if starts != 1 || ends != 1 {
		return "", fmt.Errorf(
			"found %d %q and %d %q, want one of each: remove the extra blocks first",
			starts, instructionsStartMarker, ends, instructionsEndMarker)
	}

	start := strings.Index(existing, instructionsStartMarker)
	end := strings.Index(existing, instructionsEndMarker)
	if end < start {
		return "", fmt.Errorf("%q comes before %q", instructionsEndMarker, instructionsStartMarker)
	}

	// The block carries its own trailing newline; whatever followed the end
	// marker already carries the file's, so keeping both would grow the file
	// by one line per run and break idempotence.
	return existing[:start] +
		strings.TrimSuffix(agentInstructions, "\n") +
		existing[end+len(instructionsEndMarker):], nil
}

// appendInstructions puts the block at the end of a file that has none,
// separated by one blank line from whatever the user wrote above it.
func appendInstructions(existing string) string {
	if strings.TrimSpace(existing) == "" {
		return agentInstructions
	}

	return strings.TrimRight(existing, "\n") + "\n\n" + agentInstructions
}

// writeFileAtomically replaces path's contents through a temporary file in
// the same directory and a rename.
//
// The rename is the reason: this command edits a file the user maintains by
// hand, often their only copy, so a failure partway through must leave the
// original untouched rather than truncated.
func writeFileAtomically(path, content string, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".waythrough-instructions-*")
	if err != nil {
		return fmt.Errorf("create a temporary file next to %s: %w", path, err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := finishTempFile(temp, content, mode); err != nil {
		return fmt.Errorf("write %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}

	return nil
}

// finishTempFile writes the new contents, gives the file its final
// permission, and closes it, so the rename that follows publishes a file
// that is complete on disk rather than one still buffered.
func finishTempFile(temp *os.File, content string, mode os.FileMode) error {
	if _, err := io.WriteString(temp, content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}

	return temp.Close()
}
