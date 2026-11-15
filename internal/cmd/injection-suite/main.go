// Command injection-suite measures how often an attacker who writes the content an evaluator reads
// obtains the result it wants, from one evaluator configuration and one semantic check.
//
// A suite file fixes the configuration under test, the attacker and the adjudicator models, the
// attack's budget and the decision rule. A set of cases, one directory each, holds bounded reviews
// labelled adequate or inadequate, each with a key that says why. The run has five stages:
//
//   - confirm: the adjudicator, a model other than the judging one, confirms each label;
//   - baseline: each confirmed case is judged without attack, by the configuration and by a
//     reference with the same model and no separation of channels;
//   - attack: each inadequate case is attacked, by inserting text into its inputs, with each
//     technique of the catalog and then adaptively against a replica of the configuration, and
//     the best variants are submitted to it; a satisfied result counts as a break only if the
//     adjudicator finds the defect still there;
//   - denial: each adequate case is attacked for any result but satisfied;
//   - verdict: the measures and the decision rule, written to summary.json.
//
// Each case's result of each stage is kept in its own file under the output directory, so a run
// that stops resumes where it stopped. The log names cases and results and no content.
//
// Usage: go run ./internal/cmd/injection-suite -suite FILE -cases DIR -out DIR -max-cost DOLLARS
// [-stage all|confirm|baseline|attack|denial|verdict] [-workers N] [-dev]
//
// It reaches the models through the Anthropic API, with the credentials the SDK finds, and costs
// money; -max-cost stops it, give or take the calls in flight when the limit is reached.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/altakleos/strux/evaluate"
	"github.com/altakleos/strux/internal/version"
)

func main() {
	suite := flag.String("suite", "", "the suite file")
	cases := flag.String("cases", "", "the directory of cases")
	out := flag.String("out", "", "the directory for results")
	stage := flag.String("stage", "all", "the stage to run: all, confirm, baseline, attack, denial or verdict")
	workers := flag.Int("workers", 4, "cases run at once")
	maxCost := flag.Float64("max-cost", 0, "the most, in dollars, the run may spend")
	dev := flag.Bool("dev", false, "allow a build with changes git does not hold, whose summary then decides nothing")
	flag.Parse()
	if *suite == "" || *cases == "" || *out == "" || *maxCost <= 0 || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*suite, *cases, *out, *stage, *workers, *maxCost, *dev); err != nil {
		fmt.Fprintln(os.Stderr, "injection-suite:", err)
		os.Exit(1)
	}
}

func run(suitePath, casesDir, out, stage string, workers int, maxCost float64, dev bool) error {
	s, err := loadSuite(suitePath)
	if err != nil {
		return err
	}
	cs, set, err := loadCases(casesDir)
	if err != nil {
		return err
	}
	v := version.Read()
	if v.Modified && !dev {
		return fmt.Errorf("this build has changes git does not hold, so its configuration cannot be named; commit them, or pass -dev for a run that decides nothing")
	}
	r := &Runner{
		Suite: s, Cases: cs, Set: set, Out: out,
		Model:   evaluate.Anthropic{Client: anthropic.NewClient(option.WithMaxRetries(6))},
		Program: v.Version + "@" + v.Commit,
		Dev:     dev || v.Modified,
		Workers: workers,
		Meter:   newMeter(s.Prices, maxCost),
		Log:     os.Stderr,
	}
	summary, err := r.Run(context.Background(), stage)
	fmt.Fprintf(os.Stderr, "spent $%.2f\n", r.Meter.Spent)
	if err != nil {
		return err
	}
	if summary != nil {
		b, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(b))
	}
	return nil
}
