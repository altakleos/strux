// Package evaluate answers an evaluation request with the finding an evaluator would sign.
//
// It keeps a request's governing inputs apart from the content it judges. The configuration's
// template, the check's expectation and the governing inputs travel in the model's system
// channel; the candidate and every input to examine or consult travel as data in the user turn,
// each line marked with a tag drawn at random for the request, and a system message after the data
// closes it. The judging model holds no credential and returns a bounded result, which a separate
// step checks against the request before a finding is made from it. A result that fails the check
// becomes an unknown finding, so nothing the model writes reaches a finding outside its request.
package evaluate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"unicode/utf8"
)

// Treatment says how an evaluation may use an input: governing inputs may direct it, and examined
// content and supporting references are data.
type Treatment string

const (
	Governing  Treatment = "governing"
	Examined   Treatment = "examined"
	Supporting Treatment = "supporting"
)

// Input is one input to an evaluation, resolved from the request and never from references inside
// the content judged.
type Input struct {
	Name      string    `json:"name"`   // a short label, such as "report" or "target"
	Digest    string    `json:"digest"` // the digest of the revision it is
	Treatment Treatment `json:"treatment"`
	Text      string    `json:"text"`
}

// Request is an evaluation request with its inputs resolved: one check of one candidate, with the
// context the request names. The candidate is examined.
type Request struct {
	ID          string  `json:"id"`           // the request's identity, which the finding names
	Check       string  `json:"check"`        // the check, as an element path qualified by package
	Expectation string  `json:"expectation"`  // the check's expectation, from the candidate's definition
	IntendedUse string  `json:"intended_use"` // the use the evaluation is requested for, if any
	Candidate   Input   `json:"candidate"`
	Context     []Input `json:"context"`
}

// Finding is the content of the finding an evaluator would sign for a request.
type Finding struct {
	Check     string   `json:"check"`
	Result    string   `json:"result"`
	BoundTo   []string `json:"bound_to"`
	Request   string   `json:"request"`
	Evidence  string   `json:"evidence,omitempty"`
	Reasoning string   `json:"reasoning,omitempty"`
}

// Results a finding can carry.
const (
	Satisfied   = "satisfied"
	Unsatisfied = "unsatisfied"
	Unknown     = "unknown"
)

// Config is what decides what the evaluator signs, besides the template and the program that
// assembles the inputs and checks the result. Backend names how the model is reached, such as
// "anthropic-api" for the Anthropic Messages API or the name of an adapter, since the same model
// reached another way runs under other instructions around the prompt.
type Config struct {
	Backend   string `json:"backend"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	MaxTokens int64  `json:"max_tokens"`
	Program   string `json:"program"` // the revision of the program that runs it
}

//go:embed template.txt
var template string

// Template is the template this program gives the judging model, ahead of each request.
func Template() string { return template }

// TemplateDigest is the digest of the template this program gives the judging model.
var TemplateDigest = digest([]byte(template))

// valid refuses a configuration the model's provider would reject, so that such a rejection is
// never mistaken for one of the request.
func (c Config) valid() error {
	switch {
	case c.Backend == "":
		return errors.New("the configuration names no backend")
	case c.Model == "":
		return errors.New("the configuration names no model")
	case c.MaxTokens < 1:
		return errors.New("the configuration's output limit is not positive")
	case !slices.Contains([]string{"", "low", "medium", "high", "xhigh", "max"}, c.Effort):
		return fmt.Errorf("the configuration's effort %q is not low, medium, high, xhigh or max", c.Effort)
	}
	return nil
}

// Digest identifies the configuration: its fields and the template.
func (c Config) Digest() string {
	b, err := json.Marshal(struct {
		Config
		Template string `json:"template"`
	}{c, TemplateDigest})
	if err != nil {
		panic(err) // a struct of strings and an integer always marshals
	}
	return digest(b)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Model is the judging model. Judge returns an error only when asking again could succeed or the
// configuration must be fixed first; a request the model's provider rejects for what it holds is a
// response that stopped as rejected, and the checking step refuses it.
type Model interface {
	Judge(ctx context.Context, c Config, p Prompt) (Response, error)
}

// Response is what the judging model returned.
type Response struct {
	Text  string
	Stop  Stop
	Usage Usage
}

// Stop says why the judging model stopped.
type Stop string

const (
	StopDone     Stop = "done"
	StopRefused  Stop = "refused"  // the model declined to answer
	StopLength   Stop = "length"   // the output or the context ran out
	StopRejected Stop = "rejected" // the provider refused the request for what it holds
	StopOther    Stop = "other"
)

// Usage counts the tokens a judgment took.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// Outcome is one evaluation: the finding the evaluator would sign, and what led to it.
type Outcome struct {
	Finding       Finding   `json:"finding"`
	Judgment      *Judgment `json:"judgment,omitempty"` // the model's result as checked; absent when refused
	Refused       string    `json:"refused,omitempty"`  // why the model's result was refused
	Raw           string    `json:"raw"`                // the model's result as it returned it
	Configuration string    `json:"configuration"`      // the digest of the configuration that ran
	Usage         Usage     `json:"usage"`
}

// Evaluator runs one configuration.
type Evaluator struct {
	Model  Model
	Config Config
	// Tag draws the tag that marks the request's data; nil draws one at random.
	Tag func() (string, error)
}

// Evaluate answers the request. It returns an error for a request it cannot evaluate, and when the
// model returned no response; any response gives a finding.
func (e Evaluator) Evaluate(ctx context.Context, r Request) (Outcome, error) {
	if err := r.Valid(); err != nil {
		return Outcome{}, err
	}
	if err := e.Config.valid(); err != nil {
		return Outcome{}, err
	}
	out := Outcome{Configuration: e.Config.Digest()}
	if n := r.size(); n > maxInputBytes {
		return out.refuse(r, fmt.Sprintf("the inputs hold %d bytes, more than the %d this evaluator reads", n, maxInputBytes)), nil
	}
	draw := e.Tag
	if draw == nil {
		draw = randomTag
	}
	tag, err := draw()
	if err != nil {
		return Outcome{}, fmt.Errorf("drawing the request's tag: %w", err)
	}
	p := Assemble(r, tag)
	resp, err := e.Model.Judge(ctx, e.Config, p)
	if err != nil {
		return Outcome{}, err
	}
	return Settle(r, tag, resp, out.Configuration), nil
}

// Settle is the checking step on its own: it turns the judging model's response to a valid request,
// whose data was marked with the tag, into the outcome, with the finding the evaluator would sign.
// Evaluate calls it; a caller that assembled a prompt of its own, to compare it with this one, calls
// it with the tag it used, or none.
func Settle(r Request, tag string, resp Response, configuration string) Outcome {
	out := Outcome{Raw: resp.Text, Usage: resp.Usage, Configuration: configuration}
	var j Judgment
	var err error
	switch resp.Stop {
	case StopDone:
		j, err = check(r.inputs(), tag, resp.Text)
	case StopRefused:
		err = errors.New("the judging model declined to judge")
	case StopLength:
		err = errors.New("the judging model's result was cut short")
	case StopRejected:
		err = errors.New("the model's provider rejected the request")
	default:
		err = errors.New("the judging model stopped without a result")
	}
	if err != nil {
		return out.refuse(r, err.Error())
	}
	out.Judgment = &j
	out.Finding = finding(r, j)
	return out
}

// refuse makes the unknown finding for a request no result was established for.
func (out Outcome) refuse(r Request, why string) Outcome {
	out.Refused = why
	out.Finding = Finding{
		Check:     r.Check,
		Result:    Unknown,
		BoundTo:   r.boundTo(),
		Request:   r.ID,
		Reasoning: "No result was established: " + why + ".",
	}
	return out
}

// boundTo is what a finding is bound to: the candidate it judges and each input of its context, all
// of which the evaluator received.
func (r Request) boundTo() []string {
	var bound []string
	for _, in := range r.inputs() {
		if !slices.Contains(bound, in.Digest) {
			bound = append(bound, in.Digest)
		}
	}
	return bound
}

// maxInputBytes bounds what one evaluation reads, so that no input can make the request too long
// for the model.
const maxInputBytes = 1 << 20

func (r Request) size() int {
	n := len(r.Expectation)
	for _, in := range r.inputs() {
		n += len(in.Text)
	}
	return n
}

// inputs numbers the request's inputs from 1: the candidate, then the context in order.
func (r Request) inputs() []Input {
	return append([]Input{r.Candidate}, r.Context...)
}

// Valid reports whether the evaluator can evaluate the request.
func (r Request) Valid() error {
	switch {
	case r.ID == "":
		return errors.New("the request has no identity")
	case !path.MatchString(r.Check):
		return fmt.Errorf("the request's check %q is not an element path", r.Check)
	case r.IntendedUse != "" && !path.MatchString(r.IntendedUse):
		return fmt.Errorf("the request's intended use %q is not an element path", r.IntendedUse)
	case r.Expectation == "":
		return errors.New("the request carries no expectation for its check")
	case r.Candidate.Treatment != Examined:
		return fmt.Errorf("the candidate's treatment is %q, and a candidate is examined", r.Candidate.Treatment)
	}
	for i, in := range r.inputs() {
		switch {
		case !digestForm.MatchString(in.Digest):
			return fmt.Errorf("input %d has the digest %q, which is not sha256: and 64 hexadecimal digits", i+1, in.Digest)
		case !nameForm.MatchString(in.Name):
			return fmt.Errorf("input %d has the name %q, which is not 1 to 100 letters, digits, spaces and . _ / - @ + characters", i+1, in.Name)
		case in.Treatment != Governing && in.Treatment != Examined && in.Treatment != Supporting:
			return fmt.Errorf("input %d has the treatment %q, which is not governing, examined or supporting", i+1, in.Treatment)
		case !utf8.ValidString(in.Text):
			return fmt.Errorf("input %d is not UTF-8 text", i+1)
		}
	}
	if !utf8.ValidString(r.Expectation) {
		return errors.New("the request's expectation is not UTF-8 text")
	}
	return nil
}

// The forms of what the system text names: a request's check and use, and each input's digest and
// name. Content can reach a name, so a name holds no line break or other control.
var (
	path       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[A-Za-z0-9_.-]+$`)
	digestForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	nameForm   = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._/@+-]{0,99}$`)
)

// randomTag draws eight base-32 characters, forty bits that content written before the request
// cannot know.
func randomTag() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.EncodeToString(b), nil
}
