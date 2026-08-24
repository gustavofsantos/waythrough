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
	"reflect"
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
	writeMu                   sync.Mutex
	out                       = bufio.NewWriter(os.Stdout)
	nextID                    = 1000
	nextIDMu                  sync.Mutex
	progress                  = flag.Bool("progress", false, "report a workDoneProgress cycle for the initial load, if the client advertised support")
	progressDelay             = flag.Duration("progress-delay", 20*time.Millisecond, "time between the progress begin and end notifications")
	crash                     = flag.Bool("crash", false, "exit(1) immediately instead of speaking the protocol, to simulate a server that fails to start")
	crashMarker               = flag.String("crash-marker", "", "path to a marker file: crash once if it does not exist yet, then behave normally on every later run")
	ignoreExit                = flag.Bool("ignore-exit", false, "acknowledge shutdown but never act on exit, to simulate a server that will not stop on its own")
	definitionLine            = flag.Int("definition-line", -1, "0-based line to answer textDocument/definition with, in the requested document; negative means no definition")
	definitionColumn          = flag.Int("definition-column", 0, "0-based character to answer textDocument/definition with")
	referencesLine            = flag.Int("references-line", -1, "0-based line of the first canned textDocument/references location; negative means no references")
	referencesColumn          = flag.Int("references-column", 0, "0-based character of every canned textDocument/references location")
	referencesCount           = flag.Int("references-count", 1, "how many canned locations to answer textDocument/references with, at consecutive lines from -references-line")
	renameLine                = flag.Int("rename-line", -1, "0-based line of the canned textDocument/rename edit in the requested document; negative means no rename")
	renameColumn              = flag.Int("rename-column", 0, "0-based character where the canned edit in the requested document starts")
	renameLength              = flag.Int("rename-length", 0, "number of characters the canned edit in the requested document replaces")
	renameOtherFile           = flag.String("rename-other-file", "", "path to a second file to include a canned edit for, proving a rename can span files; empty means one edit only")
	renameOtherLine           = flag.Int("rename-other-line", 0, "0-based line of the canned edit in -rename-other-file")
	renameOtherCol            = flag.Int("rename-other-column", 0, "0-based character where the canned edit in -rename-other-file starts")
	renameOtherLen            = flag.Int("rename-other-length", 0, "number of characters the canned edit in -rename-other-file replaces")
	signatureHelp             = flag.String("signature-help", "", "raw JSON result to answer textDocument/signatureHelp with; empty means no signature help (null). A signature help result nests signatures, per-parameter labels, and two levels of active-index, so a test states the whole canned payload here rather than through one scalar flag per field")
	callHierarchy             = flag.Bool("call-hierarchy", false, "advertise callHierarchyProvider in the initialize result")
	callHierarchyFalse        = flag.Bool("call-hierarchy-false", false, "advertise callHierarchyProvider=false in the initialize result")
	callHierarchyOptions      = flag.Bool("call-hierarchy-options", false, "advertise callHierarchyProvider as an options object in the initialize result")
	callHierarchyRoots        = flag.String("call-hierarchy-roots", "", "raw JSON result to answer textDocument/prepareCallHierarchy with; empty means no prepared roots")
	incomingCalls             = flag.String("incoming-calls", "", "raw JSON result to answer callHierarchy/incomingCalls with; empty means no incoming calls")
	incomingCallsCount        = flag.Int("incoming-calls-count", 0, "number of generated incoming calls; zero uses -incoming-calls")
	incomingCallSitesCount    = flag.Int("incoming-call-sites-count", 0, "number of generated sites on each incoming call")
	outgoingCalls             = flag.String("outgoing-calls", "", "raw JSON result to answer callHierarchy/outgoingCalls with; empty means no outgoing calls")
	outgoingCallsCount        = flag.Int("outgoing-calls-count", 0, "number of generated outgoing calls; zero uses -outgoing-calls")
	outgoingCallSitesCount    = flag.Int("outgoing-call-sites-count", 0, "number of generated sites on each outgoing call")
	callHierarchyErrorAfter   = flag.Int("call-hierarchy-error-after", -1, "0-based directed call request index to fail; negative means every directed request succeeds")
	callHierarchyRequiredData = flag.String("call-hierarchy-required-data", "", "raw JSON data that every directed call item must preserve; empty skips the check")
	callHierarchyDelay        = flag.Duration("call-hierarchy-delay", 0, "delay every directed call response, to test the operation deadline")
	pullDiagnostics           = flag.Bool("pull-diagnostics", false, "advertise diagnosticProvider in the initialize result, the capability a client must see before it may ask for diagnostics")
	diagnostics               = flag.String("diagnostics", "", "raw JSON result to answer textDocument/diagnostic with, in the same spirit as -signature-help; empty means a full report holding no diagnostics, which is what a clean file gets")
	requestLog                = flag.String("request-log", "", "path to a file that receives the method name of every request and notification handled, one per line — lets a test assert Waythrough never sent a request at all, not merely that it disliked the answer")
	syncLog                   = flag.String("sync-log", "", "path to a file that receives one JSON line per didOpen/didChange notification, recording the method, uri, version, and full text sent — lets a test assert Waythrough actually synced current content, not just that some document is open")
	instanceLog               = flag.String("instance-log", "", "path to a file that receives this process's pid on every process start, one per line — lets a restart test read that the old process ended and that a different one now serves, which no LSP message reports")
	stderrLine                = flag.String("stderr-line", "", "text to write to this process's own stderr at startup, followed by a newline — lets a test assert Waythrough surfaces what a language server says about itself, which no LSP message carries")
	stderrPartial             = flag.String("stderr-partial", "", "text to write to this process's own stderr at startup with no trailing newline — the tail a server that dies mid-sentence leaves behind")

	openDocs       = map[string]bool{}
	syncLogFile    *os.File
	requestLogFile *os.File
	logMu          sync.Mutex
	directedCalls  int
)

var oversizedHierarchyResponse = flag.String(
	"oversized-call-hierarchy-response", "",
	"call hierarchy phase that declares a 64 MiB response without sending its body: prepare or directed")

func main() {
	flag.Parse()

	// The pid is recorded before any flag can end this run, so the log
	// counts every process start, including the ones that crash on purpose.
	logInstance()

	// Written before any flag can end this run, for the same reason: a
	// server that crashes on purpose is exactly the one whose stderr a
	// test wants to read.
	if *stderrLine != "" {
		fmt.Fprintln(os.Stderr, *stderrLine)
	}
	if *stderrPartial != "" {
		fmt.Fprint(os.Stderr, *stderrPartial)
	}

	if *crash {
		os.Exit(1)
	}

	if *crashMarker != "" {
		if _, err := os.Stat(*crashMarker); err != nil {
			_ = os.WriteFile(*crashMarker, []byte("crashed"), 0o644)
			os.Exit(1)
		}
	}

	syncLogFile = openLog("sync-log", *syncLog)
	requestLogFile = openLog("request-log", *requestLog)

	r := bufio.NewReader(os.Stdin)
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		logRequest(msg.Method)

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
			logSyncEvent(msg.Method, msg.Params)
		case "textDocument/definition":
			handleDefinition(msg)
		case "textDocument/references":
			handleReferences(msg)
		case "textDocument/rename":
			handleRename(msg)
		case "textDocument/signatureHelp":
			handleSignatureHelp(msg)
		case "textDocument/prepareCallHierarchy":
			handlePrepareCallHierarchy(msg)
		case "callHierarchy/incomingCalls":
			handleIncomingCalls(msg)
		case "callHierarchy/outgoingCalls":
			handleOutgoingCalls(msg)
		case "textDocument/diagnostic":
			handleDiagnostic(msg)
		}
	}
}

var lastInitializeParams json.RawMessage

func handleInitialize(msg message) {
	lastInitializeParams = msg.Params

	var capabilities []string
	if *callHierarchyFalse {
		capabilities = append(capabilities, `"callHierarchyProvider":false`)
	} else if *callHierarchyOptions {
		capabilities = append(capabilities, `"callHierarchyProvider":{}`)
	} else if *callHierarchy {
		capabilities = append(capabilities, `"callHierarchyProvider":true`)
	}
	if *pullDiagnostics {
		capabilities = append(capabilities, `"diagnosticProvider":`+
			`{"interFileDependencies":false,"workspaceDiagnostics":false}`)
	}
	respond(msg.ID, fmt.Sprintf(`{"capabilities":{%s}}`, strings.Join(capabilities, ",")))
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

// openLog opens one of the append-only log files a test reads afterwards.
// An empty path means the test asked for no such log.
func openLog(name, path string) *os.File {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, name+":", err)
		os.Exit(1)
	}
	return f
}

// logInstance appends this process's pid to -instance-log, if set. It opens
// and closes the file rather than holding it, because every instance of this
// fake appends to the same path, and a restart test reads that file while a
// later instance still runs.
func logInstance() {
	if *instanceLog == "" {
		return
	}
	f := openLog("instance-log", *instanceLog)
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintln(f, os.Getpid())
}

// logRequest appends one method name to -request-log, if set, for every
// message handled, whether or not this fake answers it.
func logRequest(method string) {
	if requestLogFile == nil || method == "" {
		return
	}
	logMu.Lock()
	defer logMu.Unlock()
	_, _ = fmt.Fprintln(requestLogFile, method)
}

// logSyncEvent appends one JSON line to -sync-log, if set, recording the
// document text a didOpen/didChange notification actually carried — a
// didOpen's own textDocument.text, or a didChange's first contentChanges
// entry, since Waythrough only ever sends whole-document changes.
func logSyncEvent(method string, params json.RawMessage) {
	if syncLogFile == nil {
		return
	}

	var v struct {
		TextDocument struct {
			URI     string `json:"uri"`
			Version int32  `json:"version"`
			Text    string `json:"text"`
		} `json:"textDocument"`
		ContentChanges []struct {
			Text string `json:"text"`
		} `json:"contentChanges"`
	}
	if err := json.Unmarshal(params, &v); err != nil {
		return
	}

	text := v.TextDocument.Text
	if len(v.ContentChanges) > 0 {
		text = v.ContentChanges[0].Text
	}

	line, err := json.Marshal(struct {
		Method  string `json:"method"`
		URI     string `json:"uri"`
		Version int32  `json:"version"`
		Text    string `json:"text"`
	}{Method: method, URI: v.TextDocument.URI, Version: v.TextDocument.Version, Text: text})
	if err != nil {
		return
	}

	logMu.Lock()
	defer logMu.Unlock()
	_, _ = syncLogFile.Write(append(line, '\n'))
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

// handleSignatureHelp answers textDocument/signatureHelp. Like
// handleDefinition, it errors if the document was never opened, and
// otherwise echoes the -signature-help payload back verbatim, or null when
// that flag is empty.
func handleSignatureHelp(msg message) {
	docURI := documentURI(msg.Params)
	if !openDocs[docURI] {
		respondError(msg.ID, -32000, fmt.Sprintf("document not open: %s", docURI))
		return
	}
	if *signatureHelp == "" {
		respond(msg.ID, "null")
		return
	}
	respond(msg.ID, *signatureHelp)
}

// handlePrepareCallHierarchy answers the first half of a call-hierarchy
// exchange. Like every position request, it requires the document to have
// been opened first so tests detect a missing synchronization step.
func handlePrepareCallHierarchy(msg message) {
	if *oversizedHierarchyResponse == "prepare" {
		writeOversizedHierarchyHeader()
		time.Sleep(time.Hour)
	}
	docURI := documentURI(msg.Params)
	if !openDocs[docURI] {
		respondError(msg.ID, -32000, fmt.Sprintf("document not open: %s", docURI))
		return
	}
	if *callHierarchyRoots == "" {
		respond(msg.ID, "[]")
		return
	}
	respond(msg.ID, *callHierarchyRoots)
}

func handleIncomingCalls(msg message) {
	if *oversizedHierarchyResponse == "directed" {
		writeOversizedHierarchyHeader()
		time.Sleep(time.Hour)
	}
	if !acceptDirectedCall(msg) {
		return
	}
	if *incomingCallsCount > 0 {
		respond(msg.ID, generatedDirectedCalls("from", *incomingCallsCount, *incomingCallSitesCount))
		return
	}
	if *incomingCalls == "" {
		respond(msg.ID, "[]")
		return
	}
	respond(msg.ID, *incomingCalls)
}

func writeOversizedHierarchyHeader() {
	const declaredBytes = 64<<20 + 1
	writeMu.Lock()
	defer writeMu.Unlock()
	_, _ = fmt.Fprintf(out, "Content-Length: %d\r\n\r\n", declaredBytes)
	_ = out.Flush()
}

func generatedDirectedCalls(targetField string, callCount, sitesPerCall int) string {
	call := fmt.Sprintf(`{"%s":{"name":"called","kind":12,"uri":"file:///root/called.fake",`,
		targetField) +
		`"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},` +
		`"selectionRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}}},` +
		`"fromRanges":[]}`
	const site = `{"start":{"line":0,"character":0},"end":{"line":0,"character":0}}`

	var out strings.Builder
	out.WriteByte('[')
	for callIndex := 0; callIndex < callCount; callIndex++ {
		if callIndex > 0 {
			out.WriteByte(',')
		}
		if sitesPerCall == 0 {
			out.WriteString(call)
			continue
		}
		out.WriteString(strings.TrimSuffix(call, "[]}"))
		out.WriteByte('[')
		for siteIndex := 0; siteIndex < sitesPerCall; siteIndex++ {
			if siteIndex > 0 {
				out.WriteByte(',')
			}
			out.WriteString(site)
		}
		out.WriteString("]}")
	}
	out.WriteByte(']')
	return out.String()
}

func handleOutgoingCalls(msg message) {
	if !acceptDirectedCall(msg) {
		return
	}
	if *outgoingCallsCount > 0 {
		respond(msg.ID, generatedDirectedCalls("to", *outgoingCallsCount, *outgoingCallSitesCount))
		return
	}
	if *outgoingCalls == "" {
		respond(msg.ID, "[]")
		return
	}
	respond(msg.ID, *outgoingCalls)
}

func acceptDirectedCall(msg message) bool {
	time.Sleep(*callHierarchyDelay)

	var params struct {
		Item struct {
			Data json.RawMessage `json:"data"`
		} `json:"item"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		respondError(msg.ID, -32602, fmt.Sprintf("invalid params: %s", err))
		return false
	}
	if *callHierarchyRequiredData != "" &&
		!jsonEqual(params.Item.Data, json.RawMessage(*callHierarchyRequiredData)) {
		respondError(msg.ID, -32602, "prepared item data was not preserved")
		return false
	}

	requestIndex := directedCalls
	directedCalls++
	if requestIndex == *callHierarchyErrorAfter {
		respondError(msg.ID, -32000, "directed call failed on purpose")
		return false
	}
	return true
}

func jsonEqual(left, right json.RawMessage) bool {
	var leftValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	var rightValue any
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

// handleDiagnostic answers textDocument/diagnostic. Like handleDefinition,
// it errors if the document was never opened, and otherwise echoes the
// -diagnostics payload back verbatim, or a full report holding nothing when
// that flag is empty, which is the answer a clean file gets.
func handleDiagnostic(msg message) {
	docURI := documentURI(msg.Params)
	if !openDocs[docURI] {
		respondError(msg.ID, -32000, fmt.Sprintf("document not open: %s", docURI))
		return
	}
	if *diagnostics == "" {
		respond(msg.ID, `{"kind":"full","items":[]}`)
		return
	}
	respond(msg.ID, *diagnostics)
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
			return message{}, fmt.Errorf("read header line: %w", err)
		}
		if line == "\r\n" {
			break
		}
		_, _ = fmt.Sscanf(line, "Content-Length: %d", &length)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return message{}, fmt.Errorf("read body of %d bytes: %w", length, err)
	}

	var msg message
	if err := json.Unmarshal(body, &msg); err != nil {
		return message{}, fmt.Errorf("decode body: %w", err)
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
