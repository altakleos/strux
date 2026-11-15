"""An adapter that reaches Codex through the OpenAI Codex SDK.

Codex runs signed in with the user's own ChatGPT plan, from a Codex home of its own, so that none of
the user's Codex configuration, AGENTS.md, hooks, skills, plugins, memories or MCP servers reaches
the model. The home is STRUX_CODEX_HOME, or ~/.strux/codex. Codex refuses a home that does not
exist, so create it and sign in to it once with
`mkdir -p -m 700 ~/.strux/codex && CODEX_HOME=~/.strux/codex codex login`. The adapter removes any
OpenAI API key from the environment it gives Codex, which would otherwise bill that key instead.

Codex runs as nothing but a model here: the request's governing text replaces its base
instructions, with the reminder after it, and the data reaches it as an external message, which
Codex treats as untrusted content below the instructions. Its shell tools and web search are off,
its sandbox reads only, every request to go beyond it is denied, it runs in an empty directory of
its own, and its thread is not saved. Its answer takes the schema's form.

Usage: python -m strux_adapters.codex < request.json
"""

from __future__ import annotations

import os
import tempfile
from pathlib import Path

from openai_codex import (
    ApprovalMode,
    Codex,
    CodexConfig,
    CodexError,
    ExternalMessage,
    Sandbox,
    TurnResult,
    is_retryable_error,
)
from openai_codex.types import ReasoningEffort

from .protocol import NoResponse, Request, Response, serve

HOME = "STRUX_CODEX_HOME"
DEFAULT_HOME = Path.home() / ".strux" / "codex"

# Variables that would make Codex bill an API account instead of the plan it is signed in with.
API_CREDENTIALS = ("OPENAI_API_KEY", "CODEX_API_KEY")

# Codex configured as a model alone.
CONFIG = {
    "features": {"shell_tool": False, "unified_exec": False},
    "web_search": "disabled",
    "project_doc_max_bytes": 0,
}


def home() -> str:
    path = Path(os.environ.get(HOME) or DEFAULT_HOME).expanduser()
    if not path.is_dir():
        raise SystemExit(
            f"the Codex home {path} does not exist: create it and sign in to it with "
            f"mkdir -p -m 700 {path} && CODEX_HOME={path} codex login"
        )
    return str(path)


def environment(codex_home: str) -> dict[str, str]:
    env = {k: v for k, v in os.environ.items() if k not in API_CREDENTIALS}
    env["CODEX_HOME"] = codex_home
    return env


def response(result: TurnResult) -> Response:
    """The evaluator's response for a Codex turn."""
    total = result.usage.total if result.usage else None
    tokens = {
        "input_tokens": (total.input_tokens - total.cached_input_tokens) if total else 0,
        "output_tokens": total.output_tokens if total else 0,
        "cache_read_tokens": total.cached_input_tokens if total else 0,
        "cache_write_tokens": (total.cache_write_input_tokens or 0) if total else 0,
    }
    status = getattr(result.status, "value", result.status)
    if status == "completed" and result.final_response:
        return Response(text=result.final_response, stop="done", **tokens)
    return Response(stop="other", **tokens)


def judge(request: Request) -> Response:
    codex_home = home()
    with tempfile.TemporaryDirectory(prefix="strux-codex-") as cwd:
        try:
            with Codex(CodexConfig(cwd=cwd, env=environment(codex_home))) as codex:
                thread = codex.thread_start(
                    base_instructions=request.instructions,
                    model=request.model or None,
                    sandbox=Sandbox.read_only,
                    approval_mode=ApprovalMode.deny_all,
                    cwd=cwd,
                    ephemeral=True,
                    config=CONFIG,
                )
                result = thread.run(
                    ExternalMessage(tool_name="evaluation_data", namespace="strux", content=request.data),
                    output_schema=request.schema,
                    effort=ReasoningEffort(request.effort) if request.effort else None,
                )
        except CodexError as err:
            if is_retryable_error(err):
                raise NoResponse(f"Codex is busy: {err}") from err
            raise
    return response(result)


def main() -> int:
    return serve(judge)


if __name__ == "__main__":
    raise SystemExit(main())
