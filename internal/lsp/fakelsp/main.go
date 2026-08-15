// Command fakelsp is a minimal, scriptable language server used only by
// internal/lsp's tests. It speaks real LSP Content-Length framing so the
// tests exercise the same transport a real language server uses, without
// requiring clojure-lsp or lua-language-server to be installed.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"go.lsp.dev/uri"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

var (
	writeMu          sync.Mutex
	out              = bufio.NewWriter(os.Stdout)
	nextID           = 1000
	nextIDMu         sync.Mutex
	progress         = flag.Bool("progress", false, "report a workDoneProgress cycle for the initial load, if the client advertised support")
	progressDelay    = flag.Duration("progress-delay", 20*time.Millisecond, "time between the progress begin and end notifications")
	crash            = flag.Bool("crash", false, "exit(1) immediately instead of speaking the protocol, to simulate a server that fails to start")
	crashMarker      = flag.String("crash-marker", "", "path to a marker file: crash once if it does not exist yet, then behave normally on every later run")
	ignoreExit       = flag.Bool("ignore-exit", false, "acknowledge shutdown but never act on exit, to simulate a server that will not stop on its own")
	definitionLine   = flag.Int("definition-line", -1, "0-based line to answer textDocument/definition with, in the requested document; negative means no definition")
	definitionColumn = flag.Int("definition-column", 0, "0-based character to answer textDocument/definition with")
	referencesLine   = flag.Int("references-line", -1, "0-based line of the first canned textDocument/references location; negative means no references")
	referencesColumn = flag.Int("references-column", 0, "0-based character of every canned textDocument/references location")
	referencesCount  = flag.Int("references-count", 1, "how many canned locations to answer textDocument/references with, at consecutive lines from -references-line")
	renameLine       = flag.Int("rename-line", -1, "0-based line of the canned textDocument/rename edit in the requested document; negative means no rename")
	renameColumn     = flag.Int("rename-column", 0, "0-based character where the canned edit in the requested document starts")
	renameLength     = flag.Int("rename-length", 0, "number of characters the canned edit in the requested document replaces")
	renameOtherFile  = flag.String("rename-other-file", "", "path to a second file to include a canned edit for, proving a rename can span files; empty means one edit only")
	renameOtherLine  = flag.Int("rename-other-line", 0, "0-based line of the canned edit in -rename-other-file")
	renameOtherCol   = flag.Int("rename-other-column", 0, "0-based character where the canned edit in -rename-other-file starts")
	renameOtherLen   = flag.Int("rename-other-length", 0, "number of characters the canned edit in -rename-other-file replaces")

	openDocs = map[string]bool{}
)

func main() {
	flag.Parse()

	if *crash {
		os.Exit(1)
	}

	if *crashMarker != "" {
		if _, err := os.Stat(*crashMarker); err != nil {
			_ = os.WriteFile(*crashMarker, []byte("crashed"), 0o644)
			os.Exit(1)
		}
	}

	r := bufio.NewReader(os.Stdin)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}

		switch msg.Method {
		case "initialize":
			handleInitialize(msg)
		case "initialized":
			if *progress {
				go runProgressCycle(workDoneProgressAdvertised(msg))
			}
		case "shutdown":
			respond(msg.ID, "null")
		case "exit":
			if *ignoreExit {
				continue
			}
			_ = out.Flush()
			os.Exit(0)
		case "textDocument/didOpen", "textDocument/didChange":
			openDocs[documentURI(msg.Params)] = true
		case "textDocument/definition":
			handleDefinition(msg)
		case "textDocument/references":
			handleReferences(msg)
		case "textDocument/rename":
			handleRename(msg)
		}
	}
}

var lastInitializeParams json.RawMessage

func handleInitialize(msg message) {
	lastInitializeParams = msg.Params
	respond(msg.ID, `{"capabilities":{}}`)
}

func respond(id json.RawMessage, result string) {
	writeMessage(message{JSONRPC: "2.0", ID: id, Result: json.RawMessage(result)})
}

func respondError(id json.RawMessage, code int, msg string) {
	writeMessage(message{
		JSONRPC: "2.0",
		ID:      id,
		Error:   json.RawMessage(fmt.Sprintf(`{"code":%d,"message":%q}`, code, msg)),
	})
}

// documentURI reads the textDocument.uri field common to didOpen, didChange,
// and definition params.
func documentURI(params json.RawMessage) string {
	var v struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &v); err != nil {
		return ""
	}
	return v.TextDocument.URI
}

// handleDefinition answers textDocument/definition. It errors if the
// document was never opened, so a test can tell whether Waythrough synced
// the file before asking about it, and otherwise returns the location the
// -definition-line/-definition-column flags describe, or null if
// -definition-line is negative.
func handleDefinition(msg message) {
	docURI := documentURI(msg.Params)
	if !openDocs[docURI] {
		respondError(msg.ID, -32000, fmt.Sprintf("document not open: %s", docURI))
		return
	}
	if *definitionLine < 0 {
		respond(msg.ID, "null")
		return
	}
	respond(msg.ID, fmt.Sprintf(
		`{"uri":%q,"range":{"start":{"line":%d,"character":%d},"end":{"line":%d,"character":%d}}}`,
		docURI, *definitionLine, *definitionColumn, *definitionLine, *definitionColumn,
	))
}

// handleReferences answers textDocument/references. Like handleDefinition,
// it errors if the document was never opened, and otherwise returns
// -references-count canned locations starting at -references-line, one per
// consecutive line, or an empty array if -references-line is negative.
func handleReferences(msg message) {
	docURI := documentURI(msg.Params)
	if !openDocs[docURI] {
		respondError(msg.ID, -32000, fmt.Sprintf("document not open: %s", docURI))
		return
	}
	if *referencesLine < 0 {
		respond(msg.ID, "[]")
		return
	}

	locations := make([]string, *referencesCount)
	for i := range locations {
		line := *referencesLine + i
		locations[i] = fmt.Sprintf(
			`{"uri":%q,"range":{"start":{"line":%d,"character":%d},"end":{"line":%d,"character":%d}}}`,
			docURI, line, *referencesColumn, line, *referencesColumn,
		)
	}
	respond(msg.ID, "["+strings.Join(locations, ",")+"]")
}

// handleRename answers textDocument/rename. Like handleDefinition, it errors
// if the requested document was never opened, and otherwise returns a
// WorkspaceEdit built from the -rename-* flags: one edit at the requested
// document's canned position, plus a second edit in -rename-other-file when
// set, proving a rename can span files. Every edit's newText is the
// request's own newName, echoed back the way a real rename would substitute
// it at each location. -rename-line negative means the server declines to
// rename (null result).
func handleRename(msg message) {
	docURI := documentURI(msg.Params)
	if !openDocs[docURI] {
		respondError(msg.ID, -32000, fmt.Sprintf("document not open: %s", docURI))
		return
	}
	if *renameLine < 0 {
		respond(msg.ID, "null")
		return
	}

	var params struct {
		NewName string `json:"newName"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		respondError(msg.ID, -32602, fmt.Sprintf("invalid params: %s", err))
		return
	}

	changes := map[string][]string{
		docURI: {canonEdit(*renameLine, *renameColumn, *renameLength, params.NewName)},
	}
	if *renameOtherFile != "" {
		otherURI := string(uri.File(*renameOtherFile))
		changes[otherURI] = append(changes[otherURI], canonEdit(*renameOtherLine, *renameOtherCol, *renameOtherLen, params.NewName))
	}

	entries := make([]string, 0, len(changes))
	for fileURI, edits := range changes {
		entries = append(entries, fmt.Sprintf("%q:[%s]", fileURI, strings.Join(edits, ",")))
	}
	respond(msg.ID, fmt.Sprintf(`{"changes":{%s}}`, strings.Join(entries, ",")))
}

func canonEdit(line, column, length int, newText string) string {
	newTextJSON, _ := json.Marshal(newText)
	return fmt.Sprintf(
		`{"range":{"start":{"line":%d,"character":%d},"end":{"line":%d,"character":%d}},"newText":%s}`,
		line, column, line, column+length, newTextJSON,
	)
}

// workDoneProgressAdvertised reports whether the client's initialize params
// set capabilities.window.workDoneProgress, the flag a spec-compliant
// server must see before it may report progress at all.
func workDoneProgressAdvertised(message) bool {
	var params struct {
		Capabilities struct {
			Window struct {
				WorkDoneProgress bool `json:"workDoneProgress"`
			} `json:"window"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(lastInitializeParams, &params); err != nil {
		return false
	}
	return params.Capabilities.Window.WorkDoneProgress
}

func runProgressCycle(advertised bool) {
	if !advertised {
		return
	}

	token := fmt.Sprintf("%q", "fakelsp-startup")
	writeMessage(message{
		JSONRPC: "2.0",
		ID:      nextRequestID(),
		Method:  "window/workDoneProgress/create",
		Params:  json.RawMessage(fmt.Sprintf(`{"token":%s}`, token)),
	})

	sendProgress(token, `{"kind":"begin","title":"Indexing"}`)
	time.Sleep(*progressDelay)
	sendProgress(token, `{"kind":"end"}`)
}

func sendProgress(token, value string) {
	writeMessage(message{
		JSONRPC: "2.0",
		Method:  "$/progress",
		Params:  json.RawMessage(fmt.Sprintf(`{"token":%s,"value":%s}`, token, value)),
	})
}

func nextRequestID() json.RawMessage {
	nextIDMu.Lock()
	defer nextIDMu.Unlock()
	nextID++
	return json.RawMessage(fmt.Sprintf("%d", nextID))
}

func readMessage(r *bufio.Reader) (message, error) {
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return message{}, err
		}
		if line == "\r\n" {
			break
		}
		_, _ = fmt.Sscanf(line, "Content-Length: %d", &length)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return message{}, err
	}

	var msg message
	if err := json.Unmarshal(body, &msg); err != nil {
		return message{}, err
	}
	return msg, nil
}

func writeMessage(msg message) {
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}

	writeMu.Lock()
	defer writeMu.Unlock()
	_, _ = fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(body), body)
	_ = out.Flush()
}
