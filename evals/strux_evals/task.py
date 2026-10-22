"""The Inspect tasks. The solver is Claude Code for live runs, or a scripted model for the offline
wiring check; the dataset, sandbox and scorer are the same either way."""

from inspect_ai import Task, task
from inspect_ai.agent import react
from inspect_ai.scorer import CORRECT, INCORRECT, Score, Target, accuracy, scorer, stderr
from inspect_ai.solver import Solver, TaskState, solver
from inspect_ai.tool import bash

from .dataset import samples


@scorer(metrics=[accuracy(), stderr()])
def submitted_target():
    """Deterministic: the submitted answer contains the expected string. No model judges it."""

    async def score(state: TaskState, target: Target) -> Score:
        answer = state.output.completion.strip()
        ok = target.text != "" and target.text in answer
        return Score(value=CORRECT if ok else INCORRECT, answer=answer, explanation=f"expected {target.text!r}")

    return score


@solver
def offline_agent() -> Solver:
    """A bash-using agent on whatever model the run names; with mockllm/model it is scripted."""
    return react(tools=[bash(timeout=60)])


@solver
def claude_code_agent(version: str = "auto") -> Solver:
    """The real thing, for live runs. Needs an API key and a pinned Claude Code version."""
    from inspect_swe import claude_code

    return claude_code(version=version)


@solver
def codex_cli_agent(version: str = "auto") -> Solver:
    """OpenAI's Codex CLI, for live runs. Needs an OpenAI API key and a pinned Codex version."""
    from inspect_swe import codex_cli

    return codex_cli(version=version)


AGENTS = {
    "offline": offline_agent,
    "claude_code": claude_code_agent,
    "codex_cli": codex_cli_agent,
}


@task
def strux_stub(arm: str = "with_strux", agent: str = "offline") -> Task:
    if agent not in AGENTS:
        raise ValueError(f"unknown agent {agent!r}; agents are {', '.join(AGENTS)}")
    solver_ = AGENTS[agent]()
    return Task(
        dataset=samples(arm),
        solver=solver_,
        scorer=submitted_target(),
        sandbox="local",
        message_limit=20,
        metadata={"arm": arm},
    )
