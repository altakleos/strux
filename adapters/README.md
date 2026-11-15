# Adapters

The programs through which the strux evaluator reaches a model service, signed in with the user's
own plan, so that strux holds no credential and a call bills no API account. The `evaluate` package
starts one for each prompt, writes one request to its standard input and reads one response from
its standard output, as `strux_adapters/protocol.py` and `evaluate.Adapter` define.

| Adapter | Service | Signs in as |
|---|---|---|
| `python -m strux_adapters.claude` | Claude, through the Claude Agent SDK | Claude Code on this machine, with any `ANTHROPIC_API_KEY` removed |
| `python -m strux_adapters.codex` | Codex, through the OpenAI Codex SDK | Codex, from its own home `~/.strux/codex` (or `STRUX_CODEX_HOME`), with any OpenAI API key removed |

Each runs its service as a model alone: the request's governing text as its instructions, the data
as the one message, no tools, none of the user's settings, instruction files, plugins or servers,
an empty working directory, and an answer in the request's schema. Codex refuses a home that does
not exist, so create its own home and sign in to it once with
`mkdir -p -m 700 ~/.strux/codex && CODEX_HOME=~/.strux/codex codex login`.

Run one from the repository's root as `uv run --directory adapters --locked python -m strux_adapters.claude`.
`make adapters-check` runs ruff and the tests, which stand in for each SDK and sign in nowhere.
