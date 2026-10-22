"""The Inspect tasks. The solver is Claude Code or Codex CLI for live runs, or a scripted model for
the offline wiring check; the dataset and scorer are the same either way."""

from inspect_ai import Task, task
from inspect_ai.agent import Agent, react
from inspect_ai.scorer import CORRECT, INCORRECT, Score, Target, accuracy, scorer, stderr
from inspect_ai.solver import TaskState
from inspect_ai.tool import bash

from .dataset import samples

# Versions that follow whatever is current, so two runs would not use the same agent.
FLOATING = ("", "auto", "latest", "stable", "sandbox")


def normalize(answer: str) -> str:
    """The answer without the whitespace, quotes or backticks an agent may wrap it in."""
    return answer.strip().strip("`'\"").strip()


@scorer(metrics=[accuracy(), stderr()])
def submitted_target():
    """Deterministic: the submitted answer is exactly the expected one. No model judges it."""

    async def score(state: TaskState, target: Target) -> Score:
        answer = normalize(state.output.completion)
        ok = target.text != "" and answer == target.text
        return Score(value=CORRECT if ok else INCORRECT, answer=answer, explanation=f"expected {target.text!r}")

    return score


def offline_agent(version: str) -> Agent:
    """A bash-using agent on whatever model the run names; with mockllm/model it is scripted."""
    return react(tools=[bash(timeout=60)])


def claude_code_agent(version: str) -> Agent:
    """The real thing, for live runs. Needs an API key."""
    from inspect_swe import claude_code

    return claude_code(version=version)


def codex_cli_agent(version: str) -> Agent:
    """OpenAI's Codex CLI, for live runs. Needs an OpenAI API key."""
    from inspect_swe import codex_cli

    return codex_cli(version=version)


AGENTS = {
    "offline": offline_agent,
    "claude_code": claude_code_agent,
    "codex_cli": codex_cli_agent,
}


@task
def strux_stub(
    arm: str = "with_strux",
    agent: str = "offline",
    agent_version: str = "",
    sandbox: str = "local",
    token_limit: int = 500_000,
) -> Task:
    """One comparison arm. A live agent must run in a Docker sandbox, so that it cannot reach a
    strux installed on the host, and at a pinned version, so that runs are comparable."""
    if agent not in AGENTS:
        raise ValueError(f"unknown agent {agent!r}; agents are {', '.join(AGENTS)}")
    if agent != "offline":
        if sandbox != "docker":
            raise ValueError(f"{agent} runs in a Docker sandbox, not {sandbox!r}: pass -T sandbox=docker")
        if agent_version in FLOATING:
            raise ValueError(f"pin the {agent} version with -T agent_version=<version>, not {agent_version!r}")
    return Task(
        dataset=samples(arm),
        solver=AGENTS[agent](agent_version),
        scorer=submitted_target(),
        sandbox=sandbox,
        message_limit=20,
        token_limit=token_limit,
        metadata={"arm": arm, "agent": agent, "agent_version": agent_version},
    )
