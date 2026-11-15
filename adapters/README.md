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
as the one message, delivered as written, none of the user's settings, instruction files, skills,
plugins, servers or connected apps, an empty working directory, and an answer in the request's
schema. Each service would add tools and text of its own, so each adapter turns them off, and what
remains is stated here and checked by a test:

| Adapter | What reaches the model besides the request |
|---|---|
| Claude | A line naming the Agent SDK before the system prompt, a note of the environment (directory, platform, model, date) after the data, and the tool through which the model returns its answer |
| Codex | Nothing: no tool, and no text of Codex's own |

Codex's tools come from its model catalog as well as from its configuration, so the Codex adapter
reads the catalog from Codex for each request and gives Codex back one in which no model has tools
of Codex's own. Codex refuses a home that does not exist, so create its own home and sign in to it
once with `mkdir -p -m 700 ~/.strux/codex && CODEX_HOME=~/.strux/codex codex login`.

Run one from the repository's root as `uv run --directory adapters --locked python -m strux_adapters.claude`.
`make adapters-check` runs ruff and the tests, which sign in nowhere. Most stand in for each SDK;
`tests/test_claude_request.py` and `tests/test_codex_request.py` run the service's own program
against a local server that stands in for its API, and check every request it sends. Run them again
after an SDK changes, since a new release can bring back a tool or text the adapter had turned off.
