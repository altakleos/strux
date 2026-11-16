package evaluate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// Adapter is a judging model reached through a program that speaks for one model service, such as
// an agent SDK signed in with the user's own plan. For each prompt it starts the program, writes
// one AdapterRequest to its standard input as JSON, and reads one AdapterResponse from its standard
// output. The program holds whatever credentials the service needs, so strux holds none.
type Adapter struct {
	Command []string // the program and its arguments
}

// AdapterRequest is what an adapter reads: the configuration's model settings and the prompt, with
// its governing text, its data and its reminder apart.
type AdapterRequest struct {
	Model     string         `json:"model"`
	Effort    string         `json:"effort,omitempty"`
	MaxTokens int64          `json:"max_tokens"`
	System    string         `json:"system"`
	Data      string         `json:"data"`
	Reminder  string         `json:"reminder,omitempty"`
	Schema    map[string]any `json:"schema"`
}

// AdapterResponse is what an adapter writes: the model's text, why it stopped and what it used; or,
// when no response came back and asking again could succeed, the error alone.
type AdapterResponse struct {
	Text  string `json:"text"`
	Stop  Stop   `json:"stop"`
	Usage Usage  `json:"usage"`
	Error string `json:"error,omitempty"`
}

// maxAdapterOutput bounds what strux reads from an adapter.
const maxAdapterOutput = 4 << 20

// Judge runs the adapter for one prompt.
func (a Adapter) Judge(ctx context.Context, c Config, p Prompt) (Response, error) {
	if len(a.Command) == 0 {
		return Response{}, errors.New("the adapter names no program")
	}
	req, err := json.Marshal(AdapterRequest{
		Model: c.Model, Effort: c.Effort, MaxTokens: c.MaxTokens,
		System: p.System, Data: p.Data, Reminder: p.Reminder, Schema: p.Schema,
	})
	if err != nil {
		return Response{}, err
	}
	cmd := exec.CommandContext(ctx, a.Command[0], a.Command[1:]...) //nolint:gosec // an adapter runs the program its configuration names
	cmd.Stdin = bytes.NewReader(req)
	out := &bounded{max: maxAdapterOutput}
	stderr := &bounded{max: 4096}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		return Response{}, fmt.Errorf("the adapter %s failed: %w: %s", a.Command[0], err, strings.TrimSpace(stderr.String()))
	}
	if out.over {
		return Response{}, fmt.Errorf("the adapter %s wrote more than %d bytes", a.Command[0], maxAdapterOutput)
	}
	var resp AdapterResponse
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("the adapter %s wrote no response in its form: %w", a.Command[0], err)
	}
	if resp.Error != "" {
		return Response{}, fmt.Errorf("the adapter %s got no response: %s: %w", a.Command[0], resp.Error, ErrNoResponse)
	}
	if !slices.Contains([]Stop{StopDone, StopRefused, StopLength, StopRejected, StopOther}, resp.Stop) {
		return Response{}, fmt.Errorf("the adapter %s gave the stop %q, which is not one strux knows", a.Command[0], resp.Stop)
	}
	return Response{Text: resp.Text, Stop: resp.Stop, Usage: resp.Usage}, nil
}

// bounded keeps what is written to it up to a limit, and notes whether more came. It holds its
// buffer as a field, not embedded, so that a copy cannot reach the buffer's ReadFrom around Write.
type bounded struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *bounded) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); len(p) > room {
		b.over = true
		b.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *bounded) Bytes() []byte  { return b.buf.Bytes() }
func (b *bounded) String() string { return b.buf.String() }
