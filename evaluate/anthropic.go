package evaluate

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// Anthropic is a judging model reached through the Anthropic Messages API. The governing channel
// is the request's system text and a system message after the data; the data is the one user
// turn. It offers the model no tools, and asks for the result in the schema's form. It sets no
// fallback to another model: a refusal is a refusal of the configured model, which the checking
// step turns into an unknown finding.
type Anthropic struct {
	Client anthropic.Client
}

// Judge sends one prompt, streaming the response so that a large output limit needs no special
// case. A request the API rejects for what it holds, such as inputs too long for the model, is a
// response that stopped as rejected; any other failure is an error, for the caller to retry or to
// fix the configuration.
func (a Anthropic) Judge(ctx context.Context, c Config, p Prompt) (Response, error) {
	stream := a.Client.Messages.NewStreaming(ctx, params(c, p))
	defer stream.Close()
	msg := anthropic.Message{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return Response{}, err
		}
	}
	if err := stream.Err(); err != nil {
		var apierr *anthropic.Error
		if errors.As(err, &apierr) && rejected(apierr.StatusCode) {
			return Response{Stop: StopRejected}, nil
		}
		return Response{}, err
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	stop := StopOther
	switch msg.StopReason {
	case anthropic.StopReasonEndTurn:
		stop = StopDone
	case anthropic.StopReasonRefusal:
		stop = StopRefused
	case anthropic.StopReasonMaxTokens, anthropic.StopReasonModelContextWindowExceeded:
		stop = StopLength
	}
	return Response{
		Text: text.String(),
		Stop: stop,
		Usage: Usage{
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		},
	}, nil
}

// rejected reports whether the API refused a request for what it holds: a malformed or unreadable
// request, or one too large. Authentication, a model it does not serve, limits on the rate of
// requests and its own failures are not.
func rejected(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity
}

// params is the request sent for a prompt. A prompt without a reminder sends no system message
// after the data.
func params(c Config, p Prompt) anthropic.MessageNewParams {
	messages := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(p.Data))}
	if p.Reminder != "" {
		messages = append(messages, anthropic.MessageParam{OfSystem: &anthropic.SystemMessageParam{Content: []anthropic.TextBlockParam{{Text: p.Reminder}}}})
	}
	return anthropic.MessageNewParams{
		Model:     c.Model,
		MaxTokens: c.MaxTokens,
		System:    []anthropic.TextBlockParam{{Text: p.System}},
		Messages:  messages,
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffort(c.Effort),
			Format: anthropic.JSONOutputFormatParam{Schema: p.Schema},
		},
	}
}
