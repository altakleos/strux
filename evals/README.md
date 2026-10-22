# Evals

Evals that compare an agent on the same tasks with strux and without it, to show that strux brings value, and that run the with-strux arm again after each change to catch regressions. They use [Inspect AI](https://inspect.aisi.org.uk) and, for the real agent, [`inspect_swe`](https://pypi.org/project/inspect-swe/).

This is a spike: one task, `tasks/version.yaml`, that exercises the pieces. It proves the wiring, not that strux helps.

## How it is built

- `tasks/*.yaml` are tasks as data, so they do not depend on the harness. A task has a prompt and an expected answer, taken from strux's own output.
- `strux_evals/arms.py` defines the two arms. They differ in one thing: `bin/strux` exists in the sandbox of `with_strux` and not of `without_strux`. The prompt, model, tools and limits are the same.
- `strux_evals/task.py` is the Inspect task. The scorer is deterministic: no model judges the answer.

## Check the wiring

`make evals-check` runs ruff and pytest. The tests run both arms against a scripted model in a local sandbox, so they need no API key, no Docker and cost nothing. CI runs them as the `evals` job. They need [uv](https://docs.astral.sh/uv/) and Go; they build strux, or use the binary in `STRUX_BIN`.

## Compare with a real agent

This path is untested: this spike has not run a real agent, Claude Code or Codex. It costs money and CI does not run it. Build strux, then run each arm several times:

```sh
go build -o /tmp/strux ./cmd/strux
cd evals
export STRUX_BIN=/tmp/strux ANTHROPIC_API_KEY=...
for arm in with_strux without_strux; do
  uv run inspect eval strux_evals/task.py@strux_stub -T arm=$arm -T agent=claude_code \
    --model anthropic/<model> --epochs 5 --log-dir logs/$arm
done
```

`-T agent=codex_cli` runs OpenAI's Codex CLI instead, with `OPENAI_API_KEY` and `--model openai/<model>`; it is as untested as the Claude Code path. `inspect_swe` runs the agent in the task's sandbox, which this spike sets to `local`; a run that isolates the agent needs a Docker sandbox with an image holding a pinned agent, which is the next step. Report the pass rate of each arm over its epochs, not one run.
