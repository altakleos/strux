"""An adapter that reaches Codex through the OpenAI Codex SDK.

Codex runs signed in with the user's own ChatGPT plan, from a Codex home of its own, so that none of
the user's Codex configuration, AGENTS.md, hooks, skills, plugins, memories or MCP servers reaches
the model. The home is STRUX_CODEX_HOME, or ~/.strux/codex. Codex refuses a home that does not
exist, so create it and sign in to it once with
`mkdir -p -m 700 ~/.strux/codex && CODEX_HOME=~/.strux/codex codex login`. The adapter removes any
OpenAI API key from the environment it gives Codex, which would otherwise bill that key instead.

Codex runs as nothing but a model here: the request's governing text replaces its base
instructions, with the reminder after it, and the data reaches it as an external message, which
Codex treats as untrusted content below the instructions. Codex would put tools and text of its own
in front of the model, so the adapter turns each off: in the configuration, the tools that run
commands, search the web, view images, sleep, set goals, ask the user or reach the apps, connectors
and plugins of the ChatGPT account, and the text that lists skills and describes the sandbox, the
environment and the collaboration mode; in each model's entry of the catalog, which it reads from
Codex and gives Codex back, the tool that runs code calling every other tool, sub-agents, the patch
tool, experimental tools, and the request form that repeats the instructions. What reaches the model
is the governing text and the data, and no tool. Its sandbox reads only, every request to go beyond
it is denied, it runs in an empty directory of its own, and its thread is not saved. Its answer
takes the schema's form.

Usage: python -m strux_adapters.codex < request.json
"""

from __future__ import annotations

import json
import os
import subprocess
import tempfile
from pathlib import Path

from codex_cli_bin import bundled_codex_path
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

# Codex configured as a model alone: each key turns off a tool, or text, that Codex would otherwise
# put in front of the model.
CONFIG = {
    "features": dict.fromkeys(
        (
            "shell_tool",
            "unified_exec",
            "apps",
            "plugins",
            "remote_plugin",
            "multi_agent",
            "goals",
            "sleep_tool",
            "skill_search",
            "tool_suggest",
            "view_image",
            "image_generation",
            "browser_use",
            "computer_use",
            "in_app_browser",
            "hooks",
        ),
        False,
    ),
    "web_search": "disabled",
    "project_doc_max_bytes": 0,
    "include_permissions_instructions": False,
    "include_environment_context": False,
    "include_collaboration_mode_instructions": False,
    "skills": {"include_instructions": False, "bundled": {"enabled": False}},
    "tools": {"experimental_request_user_input": {"enabled": False}},
}

# What a model's catalog entry sets that gives the model tools of Codex's own: the tool that runs
# code calling every other tool, sub-agents and the patch tool; and what the adapter sets in their
# place: no experimental tools, and the request form that sends the instructions once.
CATALOG_DROPS = ("tool_mode", "multi_agent_version", "multi_agent_reasoning_effort", "apply_patch_tool_type")
CATALOG_SETS = {"experimental_supported_tools": [], "use_responses_lite": False}


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


def read_catalog(env: dict[str, str], cwd: str) -> list[dict]:
    """Codex's model catalog, as Codex reads it signed in."""
    out = subprocess.run(
        [str(bundled_codex_path()), "debug", "models"],
        env=env,
        cwd=cwd,
        capture_output=True,
        text=True,
        timeout=120,
    )
    if out.returncode != 0:
        raise NoResponse(f"Codex did not give its model catalog: {out.stderr.strip()[:500]}")
    return json.loads(out.stdout)["models"]


def catalog(env: dict[str, str], cwd: str, model: str) -> list[dict]:
    """Codex's model catalog with no entry giving its model tools of Codex's own. A model the
    catalog lacks would run with entries Codex supplies, so it is refused."""
    models = read_catalog(env, cwd)
    if model not in {m.get("slug") for m in models}:
        raise SystemExit(f"Codex's model catalog has no model {model!r}")
    for m in models:
        for key in CATALOG_DROPS:
            m.pop(key, None)
        m.update(CATALOG_SETS)
    return models


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
    env = environment(home())
    with tempfile.TemporaryDirectory(prefix="strux-codex-") as root:
        cwd = os.path.join(root, "work")
        os.mkdir(cwd)
        models = os.path.join(root, "models.json")
        with open(models, "w") as f:
            json.dump({"models": catalog(env, cwd, request.model)}, f)
        try:
            with Codex(CodexConfig(cwd=cwd, env=env)) as codex:
                thread = codex.thread_start(
                    base_instructions=request.instructions,
                    model=request.model,
                    sandbox=Sandbox.read_only,
                    approval_mode=ApprovalMode.deny_all,
                    cwd=cwd,
                    ephemeral=True,
                    config={**CONFIG, "model_catalog_json": models},
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
