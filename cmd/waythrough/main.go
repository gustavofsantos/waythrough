// Command waythrough starts the MCP server that manages LSP servers.
package main

import (
	"os"

	"github.com/gustavofsantos/waythrough/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
