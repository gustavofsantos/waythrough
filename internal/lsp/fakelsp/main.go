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
	"sync"
	"time"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

var (
	writeMu       sync.Mutex
	out           = bufio.NewWriter(os.Stdout)
	nextID        = 1000
	nextIDMu      sync.Mutex
	progress      = flag.Bool("progress", false, "report a workDoneProgress cycle for the initial load, if the client advertised support")
	progressDelay = flag.Duration("progress-delay", 20*time.Millisecond, "time between the progress begin and end notifications")
	crash         = flag.Bool("crash", false, "exit(1) immediately instead of speaking the protocol, to simulate a server that fails to start")
	crashMarker   = flag.String("crash-marker", "", "path to a marker file: crash once if it does not exist yet, then behave normally on every later run")
	ignoreExit    = flag.Bool("ignore-exit", false, "acknowledge shutdown but never act on exit, to simulate a server that will not stop on its own")
)

func main() {
	flag.Parse()

	if *crash {
		os.Exit(1)
	}

	if *crashMarker != "" {
		if _, err := os.Stat(*crashMarker); err != nil {
			os.WriteFile(*crashMarker, []byte("crashed"), 0o644)
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
			writeMessage(message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage("null")})
		case "exit":
			if *ignoreExit {
				continue
			}
			out.Flush()
			os.Exit(0)
		}
	}
}

var lastInitializeParams json.RawMessage

func handleInitialize(msg message) {
	lastInitializeParams = msg.Params
	writeMessage(message{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  json.RawMessage(`{"capabilities":{}}`),
	})
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

	writeMessage(message{
		JSONRPC: "2.0",
		Method:  "$/progress",
		Params:  json.RawMessage(fmt.Sprintf(`{"token":%s,"value":{"kind":"begin","title":"Indexing"}}`, token)),
	})

	time.Sleep(*progressDelay)

	writeMessage(message{
		JSONRPC: "2.0",
		Method:  "$/progress",
		Params:  json.RawMessage(fmt.Sprintf(`{"token":%s,"value":{"kind":"end"}}`, token)),
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
		fmt.Sscanf(line, "Content-Length: %d", &length)
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
	fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(body), body)
	out.Flush()
}
