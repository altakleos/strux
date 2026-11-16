package evaluate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// The test binary stands in for an adapter when STRUX_TEST_ADAPTER names how it answers, so the
// tests need no other program on any platform.
func TestMain(m *testing.M) {
	if mode := os.Getenv("STRUX_TEST_ADAPTER"); mode != "" {
		os.Exit(fakeAdapter(mode))
	}
	os.Exit(m.Run())
}

func fakeAdapter(mode string) int {
	in, _ := io.ReadAll(os.Stdin)
	var req AdapterRequest
	if err := json.Unmarshal(in, &req); err != nil {
		fmt.Fprintln(os.Stderr, "bad request:", err)
		return 3
	}
	answer := func(v any) int {
		b, _ := json.Marshal(v)
		if _, err := os.Stdout.Write(b); err != nil {
			return 4
		}
		return 0
	}
	switch mode {
	case "echo": // answers with what it received, so a test can see the request
		return answer(AdapterResponse{Text: string(in), Stop: StopDone, Usage: Usage{InputTokens: 7, OutputTokens: 3}})
	case "refuse":
		return answer(AdapterResponse{Stop: StopRefused})
	case "error":
		return answer(AdapterResponse{Error: "rate limited"})
	case "crash":
		fmt.Fprintln(os.Stderr, "the service is unreachable")
		return 1
	case "garbage":
		fmt.Print("not json")
		return 0
	case "unknown-stop":
		return answer(map[string]any{"text": "", "stop": "finished"})
	case "extra-field":
		return answer(map[string]any{"text": "", "stop": "done", "cost": 1})
	case "flood":
		fmt.Print(strings.Repeat("x", maxAdapterOutput+1))
		return 0
	}
	return 2
}

func adapter(t *testing.T, mode string) Adapter {
	t.Helper()
	t.Setenv("STRUX_TEST_ADAPTER", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Adapter{Command: []string{exe, "-test.run=^$"}}
}

func TestAnAdapterReceivesThePromptWithItsChannelsApart(t *testing.T) {
	p := Assemble(request(), "TAG7")
	resp, err := adapter(t, "echo").Judge(context.Background(), Config{Backend: "fake", Model: "m", Effort: "low", MaxTokens: 9}, p)
	if err != nil {
		t.Fatal(err)
	}
	var got AdapterRequest
	if err := json.Unmarshal([]byte(resp.Text), &got); err != nil {
		t.Fatal(err)
	}
	if got.System != p.System || got.Data != p.Data || got.Reminder != p.Reminder || got.Model != "m" || got.Effort != "low" || got.MaxTokens != 9 || got.Schema["type"] != "object" {
		t.Errorf("the adapter received %+v", got)
	}
	if resp.Stop != StopDone || resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 {
		t.Errorf("response = %+v", resp)
	}
}

func TestAnAdapterThatStopsWithoutAResultGivesThatStop(t *testing.T) {
	resp, err := adapter(t, "refuse").Judge(context.Background(), config, Assemble(request(), "T"))
	if err != nil || resp.Stop != StopRefused {
		t.Errorf("response = %+v, err = %v", resp, err)
	}
}

func TestAnAdapterThatGivesNoResponseIsAnError(t *testing.T) {
	for mode, want := range map[string]string{
		"error":        "got no response: rate limited",
		"crash":        "the service is unreachable",
		"garbage":      "no response in its form",
		"unknown-stop": `the stop "finished"`,
		"extra-field":  "no response in its form",
		"flood":        "more than",
	} {
		_, err := adapter(t, mode).Judge(context.Background(), config, Assemble(request(), "T"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to say %q", mode, err, want)
		}
		if errors.Is(err, ErrNoResponse) != (mode == "error") {
			t.Errorf("%s: err = %v, which asking again could cure only when the adapter got no response", mode, err)
		}
	}
	if _, err := (Adapter{}).Judge(context.Background(), config, Prompt{}); err == nil {
		t.Error("an adapter with no program ran")
	}
}

func TestAnEvaluatorReachesItsModelThroughAnAdapter(t *testing.T) {
	out, err := Evaluator{Model: adapter(t, "refuse"), Config: config, Tag: fixed("T")}.Evaluate(context.Background(), request())
	if err != nil || out.Finding.Result != Unknown || !strings.Contains(out.Refused, "declined") {
		t.Errorf("outcome = %+v, err = %v", out, err)
	}
}
