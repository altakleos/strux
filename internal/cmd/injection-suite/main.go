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
// Usage: go run ./internal/cmd/injection-suite -suite FILE -cases DIR -out DIR
// [-max-cost DOLLARS] [-max-calls N] [-max-wait DURATION]
// [-stage all|confirm|baseline|attack|denial|verdict] [-workers N] [-dev]
//
// Each model of the suite is reached through its backend: an adapter program the suite names, such
// as one signed in with the user's own plan, or the Anthropic API with the credentials its SDK
// finds, which bills by use. -max-cost stops the run at a spend, and -max-calls at a number of model
// calls, give or take the calls in flight; at least one limit is required. A call to which no response
// comes back, because the service is busy or a limit of the plan holds, is asked again for up to
// -max-wait, 30 minutes by default, so that the cases in flight keep their work. Run it from the
// repository's root, where the suite's adapter commands find the adapters project.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

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
	maxCost := flag.Float64("max-cost", 0, "the most, in dollars, the run may spend on billed backends; 0 sets no limit")
	maxCalls := flag.Int("max-calls", 0, "the most model calls the run may make; 0 sets no limit")
	maxWait := flag.Duration("max-wait", 30*time.Minute, "how long a call keeps asking again while no response comes back; 0 asks once")
	dev := flag.Bool("dev", false, "allow a build with changes git does not hold, whose summary then decides nothing")
	flag.Parse()
	if *suite == "" || *cases == "" || *out == "" || (*maxCost <= 0 && *maxCalls <= 0) || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*suite, *cases, *out, *stage, *workers, *maxCost, *maxCalls, *maxWait, *dev); err != nil {
		fmt.Fprintln(os.Stderr, "injection-suite:", err)
		os.Exit(1)
	}
}

func run(suitePath, casesDir, out, stage string, workers int, maxCost float64, maxCalls int, maxWait time.Duration, dev bool) error {
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
		Model:   backends(s),
		Program: v.Version + "@" + v.Commit,
		Dev:     dev || v.Modified,
		Workers: workers,
		Meter:   newMeter(s.prices(), maxCost, maxCalls),
		Log:     os.Stderr,
		Wait:    maxWait,
		Pause:   30 * time.Second,
	}
	summary, err := r.Run(context.Background(), stage)
	fmt.Fprintf(os.Stderr, "spent $%.2f in %d model calls\n", r.Meter.Spent, r.Meter.Calls)
	if err != nil {
		return err
	}
	if summary != nil {
		b, _ := json.MarshalIndent(summary, "", "  ")
		fmt.Println(string(b))
	}
	return nil
}

// router sends each call to the model of the backend its configuration names.
type router map[string]evaluate.Model

func (r router) Judge(ctx context.Context, c evaluate.Config, p evaluate.Prompt) (evaluate.Response, error) {
	m, ok := r[c.Backend]
	if !ok {
		return evaluate.Response{}, fmt.Errorf("no backend %q", c.Backend)
	}
	return m.Judge(ctx, c, p)
}

// backends is the suite's backends: each adapter it names, and the Anthropic API only when a role
// uses it, so that a suite of adapters needs no API key.
func backends(s Suite) router {
	r := router{}
	for name, cmd := range s.Adapters {
		r[name] = evaluate.Adapter{Command: cmd}
	}
	for _, m := range s.roles() {
		if m.Backend == anthropicAPI {
			r[anthropicAPI] = evaluate.Anthropic{Client: anthropic.NewClient(option.WithMaxRetries(6))}
		}
	}
	return r
}
