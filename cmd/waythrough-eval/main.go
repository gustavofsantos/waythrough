// Command waythrough-eval runs bounded Waythrough-versus-text evaluation cases.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gustavofsantos/waythrough/internal/eval"
)

func main() {
	fixtureDirectory := flag.String("fixture", "", "fixture directory containing manifest.json")
	scenarioName := flag.String("scenario", "", "scenario name from manifest.json")
	repeat := flag.Int("repeat", 1, "number of cold/warm measurements to run")
	format := flag.String("format", "json", "output format; only json is supported")
	flag.Parse()

	if *format != "json" {
		fail(fmt.Errorf("unsupported format %q", *format))
	}
	if *fixtureDirectory == "" || *scenarioName == "" {
		fail(fmt.Errorf("--fixture and --scenario are required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	report, err := eval.Run(ctx, eval.Options{
		FixtureDirectory: *fixtureDirectory,
		ScenarioName:     *scenarioName,
		Repeat:           *repeat,
	})
	if err != nil {
		fail(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fail(fmt.Errorf("encode report: %w", err))
	}
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
