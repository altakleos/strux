"""An adapter that reaches Claude through the Claude Agent SDK.

The SDK runs Claude Code, signed in as Claude Code is on this machine, so a call draws on the user's
own Claude plan. The adapter removes any API key from its environment before it starts Claude Code,
which would otherwise bill that key instead.

Claude Code runs as nothing but a model here: the request's governing text replaces its system
prompt, with the reminder after it, and the data is the one user message, delivered as written, so
that a path or a command in it is text and never makes Claude Code read a file or run a command. It
has no tools, reads no settings, CLAUDE.md, MCP server, skill or plugin, sends nothing beyond the
request, which also stops it asking the model to title the session from the data, and runs in an
empty directory of its own. Its answer takes the schema's form. Claude Code still adds a line naming
the Agent SDK before the system prompt, a note of its environment after the data, and the tool
through which the model returns the answer. The output limit is Claude Code's own.

Usage: python -m strux_adapters.claude < request.json
"""

from __future__ import annotations

import asyncio
import json
import os
import tempfile
from typing import Any

from claude_agent_sdk import ClaudeAgentOptions, ClaudeSDKError, ResultMessage, query

from .protocol import NoResponse, Request, Response, serve

# Variables that would make Claude Code bill an API account instead of the plan it is signed in with.
API_CREDENTIALS = ("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")

# HTTP statuses with which the API refuses a request for what it holds, such as inputs too long.
REJECTED = {400, 413, 422}

# Claude Code's own traffic is off: telemetry, updates and the request that titles the session.
ENV = {"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}

# Turns Claude Code may take: one to answer, and others to retry an answer outside the schema.
MAX_TURNS = 3


def options(request: Request, cwd: str) -> ClaudeAgentOptions:
    """Claude Code as a model alone: the governing text as its system prompt, and nothing else."""
    return ClaudeAgentOptions(
        system_prompt=request.instructions,
        tools=[],
        allowed_tools=[],
        setting_sources=[],
        strict_mcp_config=True,
        mcp_servers={},
        skills=[],
        plugins=[],
        model=request.model,
        effort=request.effort or None,  # type: ignore[arg-type]
        output_format={"type": "json_schema", "schema": request.schema},
        verbatim_prompts=True,
        env=ENV,
        max_turns=MAX_TURNS,
        cwd=cwd,
    )


def response(result: ResultMessage | None) -> Response:
    """The evaluator's response for Claude Code's result."""
    if result is None:
        raise NoResponse("Claude Code ended without a result")
    usage: dict[str, Any] = result.usage or {}
    tokens = {
        "input_tokens": int(usage.get("input_tokens") or 0),
        "output_tokens": int(usage.get("output_tokens") or 0),
        "cache_read_tokens": int(usage.get("cache_read_input_tokens") or 0),
        "cache_write_tokens": int(usage.get("cache_creation_input_tokens") or 0),
    }
    if result.api_error_status is not None:
        if result.api_error_status in REJECTED:
            return Response(stop="rejected", **tokens)
        raise NoResponse(f"the API answered with status {result.api_error_status}")
    if result.stop_reason == "refusal":
        return Response(stop="refused", **tokens)
    if result.stop_reason in ("max_tokens", "model_context_window_exceeded"):
        return Response(stop="length", **tokens)
    if result.is_error and result.subtype == "success":
        raise NoResponse("Claude Code reported an error without an answer, such as a missing sign-in")
    if result.is_error or result.subtype != "success" or result.structured_output is None:
        return Response(stop="other", **tokens)
    return Response(text=json.dumps(result.structured_output), stop="done", **tokens)


async def ask(request: Request, cwd: str) -> Response:
    result = None
    try:
        async for message in query(prompt=request.data, options=options(request, cwd)):
            if isinstance(message, ResultMessage):
                result = message
    except ClaudeSDKError:
        # The SDK raises after a result that is an error, such as the API's refusal of a request
        # for what it holds; the result says which.
        if result is None:
            raise
    return response(result)


def judge(request: Request) -> Response:
    with tempfile.TemporaryDirectory(prefix="strux-claude-") as cwd:
        try:
            return asyncio.run(ask(request, cwd))
        except ClaudeSDKError as err:
            raise NoResponse(f"the Claude Agent SDK failed: {err}") from err


def main() -> int:
    for name in API_CREDENTIALS:
        os.environ.pop(name, None)
    return serve(judge)


if __name__ == "__main__":
    raise SystemExit(main())
