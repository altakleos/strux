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

`-T agent=codex_cli` runs OpenAI's Codex CLI instead, with `OPENAI_API_KEY` and `--model openai/<model>`; it is as untested as the Claude Code path. A live agent works in `/work`, with the programs in `/work/bin`, and its limits are `token_limit` and `time_limit` (900 seconds), not a message count. It must run with `-T sandbox=docker`, so that the without-strux arm cannot reach a strux installed on your machine, and with an exact `agent_version` (X.Y.Z); the task refuses both otherwise, and refuses a strux that is not a Linux binary. It cannot check the architecture. A live agent's answer is the last `ANSWER:` line of its reply. `token_limit` (default 500,000 per sample) caps cost. Docker uses Inspect's default image, and this path has not been run. 

## Compare the arms

After a run of each arm, set them side by side:

```sh
uv run python -m strux_evals.compare logs/with_strux logs/without_strux   # --json for JSON
```

It reads the logs, groups runs by task, agent, agent version and model, and reports for each arm the pass rate over all trials with a 95% interval, mean tokens and mean seconds, and the difference between the arms. A trial that errored is counted apart, not as a failure. If the intervals overlap it says so: run more epochs before calling a difference. Run each arm several times; one run is a sample of one.

## Catch regressions with a baseline

A baseline is the with-strux pass rate of a known-good strux, committed in `baselines/`. Record one from the logs of a run, then check later runs against it:

```sh
uv run python -m strux_evals.baseline record baselines/main.json logs/with_strux --note "strux v0.1.0"
uv run python -m strux_evals.baseline check baselines/main.json logs/with_strux
```

`check` fails (exit 1) for a group whose pass rate is lower than the baseline's by more than chance explains (an exact one-sided Fisher test, p < 0.05), whose run has fewer than 10 trials, or that has no trials; exit 2 is a bad command line or unreadable input. Groups are task, agent, agent version and model, so a baseline made with one agent says nothing about another. An errored trial is not a failure and is left out. The check only sees large drops unless there are many trials. Against a baseline of all passes, with equal trials on each side, it flags a run at or below:

| trials per side | flagged at |
|---|---|
| 10 | 6 passes (60%) |
| 20 | 15 passes (75%) |
| 40 | 35 passes (88%) |

Use 20 or more per group for a baseline you rely on. Nothing runs the check automatically yet; a live run needs an API key and Docker.
