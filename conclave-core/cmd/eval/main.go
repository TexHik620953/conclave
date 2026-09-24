// Command eval runs the golden eval suite and reports metrics. It exits
// non-zero when a case fails or when a baseline regression is detected.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/texhik/conclave/conclave-core/internal/eval"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
)

func main() {
	var (
		outPath      = flag.String("out", "", "write the JSON report to this path")
		baselinePath = flag.String("baseline", "", "compare against a baseline (JSON) and fail on regressions")
		writeBase    = flag.String("write-baseline", "", "write the current results as a baseline")
	)
	flag.Parse()

	registry, err := playbook.LoadBuiltin()
	if err != nil {
		fmt.Fprintln(os.Stderr, "eval:", err)
		os.Exit(1)
	}
	report := eval.Run(context.Background(), registry, eval.GoldenCases())

	fmt.Printf("eval: %d passed, %d failed in %s\n", report.Passed, report.Failed, report.Duration)
	for _, r := range report.Results {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		fmt.Printf("  [%s] %-32s %6s  events=%d artifacts=%d %s\n",
			status, r.Case, r.Duration, r.Events, r.Artifacts, r.Err)
	}

	if *outPath != "" {
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(*outPath, data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "eval:", err)
			os.Exit(1)
		}
	}
	if *writeBase != "" {
		if err := eval.SaveBaseline(*writeBase, report); err != nil {
			fmt.Fprintln(os.Stderr, "eval:", err)
			os.Exit(1)
		}
		fmt.Printf("baseline written to %s\n", *writeBase)
	}
	if *baselinePath != "" {
		base, err := eval.LoadBaseline(*baselinePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "eval:", err)
			os.Exit(1)
		}
		regs := eval.Compare(base, report)
		if len(regs) > 0 {
			fmt.Printf("regressions: %v\n", regs)
			os.Exit(1)
		}
		fmt.Println("no regressions against baseline")
	}
	if report.Failed > 0 {
		os.Exit(1)
	}
}
