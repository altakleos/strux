# Evals

Evals that compare an agent on the same tasks with strux and without it, to show that strux brings value, and that run the with-strux arm again after each change to catch regressions. They use [Inspect AI](https://inspect.aisi.org.uk) and, for the real agent, [`inspect_swe`](https://pypi.org/project/inspect-swe/).

This is a spike: one task, `tasks/version.yaml`, that exercises the pieces. In the arm with strux the right answer is its version; in the arm without it, "unavailable". An honest agent passes both, so the comparison only shows a difference when an agent guesses or fails to use strux. It proves the wiring, not that strux helps.

## How it is built

- `tasks/*.yaml` are tasks as data, so they do not depend on the harness. A task has a prompt and an expected answer, taken from strux's own output.
- `strux_evals/arms.py` defines the two arms. They differ in one thing: `bin/strux` exists in the sandbox of `with_strux` and not of `without_strux`. The prompt, model, tools and limits are the same.
- `strux_evals/task.py` is the Inspect task. The scorer is deterministic: no model judges the answer.

## Check the wiring

`make evals-check` runs ruff and pytest. The tests run both arms against a scripted model in a local sandbox, so they need no API key, no Docker and cost nothing. CI runs them as the `evals` job. They need [uv](https://docs.astral.sh/uv/) and Go; they build strux, or use the binary in `STRUX_BIN`.

## Compare with a real agent

This path is untested: this spike has not run a real agent, Claude Code or Codex. It costs money and CI does not run it. Build strux, then run each arm several times:

```sh
# the sandbox is Linux: build for its architecture, such as arm64 on Apple silicon
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/strux ./cmd/strux
cd evals
export STRUX_BIN=/tmp/strux ANTHROPIC_API_KEY=...
for arm in with_strux without_strux; do
  uv run inspect eval strux_evals/task.py@strux_stub -T arm=$arm -T agent=claude_code -T agent_version=X.Y.Z -T sandbox=docker \
    --model anthropic/<model> --epochs 5 --log-dir logs/$arm
done
```

`-T agent=codex_cli` runs OpenAI's Codex CLI instead, with `OPENAI_API_KEY` and `--model openai/<model>`; it is as untested as the Claude Code path. A live agent must run with `-T sandbox=docker`, so that the without-strux arm cannot reach a strux installed on your machine, and with an exact `agent_version` (X.Y.Z); the task refuses both otherwise, and refuses a strux that is not a Linux binary. It cannot check the architecture. A live agent's answer is the last `ANSWER:` line of its reply. `token_limit` (default 500,000 per sample) caps cost. Docker uses Inspect's default image, and this path has not been run. Report the pass rate of each arm over its epochs, not one run.
