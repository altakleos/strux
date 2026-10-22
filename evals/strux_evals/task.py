"""The Inspect tasks. The solver is Claude Code or Codex CLI for live runs, or a scripted model for
the offline wiring check; the dataset and scorer are the same either way."""

import re

from inspect_ai import Task, task
from inspect_ai.agent import Agent, react
from inspect_ai.scorer import CORRECT, INCORRECT, Score, Target, accuracy, scorer, stderr
from inspect_ai.solver import TaskState
from inspect_ai.tool import bash

from .arms import is_elf, strux_binary
from .dataset import samples

# An exact release, so two runs use the same agent. "latest" and ranges follow whatever is current.
PINNED = re.compile(r"\d+\.\d+\.\d+")

# The line a prompt asks the agent to end its reply with. Agents that submit an answer through a tool
# have no such line, and their whole completion is the answer.
ANSWER_LINE = re.compile(r"^\s*ANSWER:\s*(.+?)\s*$", re.IGNORECASE | re.MULTILINE)


def normalize(answer: str) -> str:
    """The answer without the whitespace, quotes, backticks or closing full stop an agent may wrap it in."""
    return answer.strip().strip("`'\".").strip()


def extract(completion: str) -> str:
    """The answer in an agent's final reply: its last ANSWER line, else all of it."""
    lines = ANSWER_LINE.findall(completion)
    return normalize(lines[-1] if lines else completion)


def is_correct(completion: str, target: str) -> bool:
    return target != "" and extract(completion).casefold() == target.casefold()


@scorer(metrics=[accuracy(), stderr()])
def submitted_target():
    """Deterministic: the answer is exactly the expected one, ignoring case. No model judges it."""

    async def score(state: TaskState, target: Target) -> Score:
        answer = extract(state.output.completion)
        ok = is_correct(state.output.completion, target.text)
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
    strux installed on the host, and at a pinned version, so that runs are comparable. The offline
    agent runs on the host, but only ever runs bin/strux, so a strux on the PATH does not reach it."""
    if agent not in AGENTS:
        raise ValueError(f"unknown agent {agent!r}; agents are {', '.join(AGENTS)}")
    if agent == "offline":
        if agent_version:
            raise ValueError("the offline agent has no version; drop -T agent_version")
    else:
        if sandbox != "docker":
            raise ValueError(f"{agent} runs in a Docker sandbox, not {sandbox!r}: pass -T sandbox=docker")
        if not PINNED.fullmatch(agent_version):
            raise ValueError(f"pin the {agent} version with -T agent_version=X.Y.Z, not {agent_version!r}")
        if arm == "with_strux" and not is_elf(strux_binary()):
            raise ValueError("a Docker sandbox runs Linux: build strux with GOOS=linux and the container's GOARCH")
    return Task(
        dataset=samples(arm),
        solver=AGENTS[agent](agent_version),
        scorer=submitted_target(),
        sandbox=sandbox,
        message_limit=20,
        token_limit=token_limit,
        metadata={"arm": arm, "agent": agent, "agent_version": agent_version},
    )
