"""The offline wiring check: both arms build, the agent runs in the sandbox, the scorer scores.
A scripted model stands in for the agent, so it needs no API key and costs nothing."""

import json
import os
import subprocess
from pathlib import Path

import pytest
from inspect_ai import eval as inspect_eval
from inspect_ai.model import ModelOutput, get_model

from strux_evals.arms import ARMS, sandbox_files
from strux_evals.dataset import UNAVAILABLE, expected, load_specs, samples
from strux_evals.task import normalize, strux_stub

REPO = Path(__file__).resolve().parents[2]


@pytest.fixture(scope="session", autouse=True)
def strux(tmp_path_factory):
    """The strux binary under test: STRUX_BIN if set, else one built from this checkout."""
    if os.environ.get("STRUX_BIN"):
        return
    binary = tmp_path_factory.mktemp("bin") / "strux"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/strux"], cwd=REPO, check=True)
    os.environ["STRUX_BIN"] = str(binary)


def scripted(answer: str):
    """A model that runs strux once, then submits answer."""
    return get_model(
        "mockllm/model",
        custom_outputs=[
            ModelOutput.for_tool_call("mockllm/model", "bash", {"command": "bin/strux version --json"}),
            ModelOutput.for_tool_call("mockllm/model", "submit", {"answer": answer}, content=""),
        ],
    )


def run(arm: str, answer: str, log_dir: Path):
    (log,) = inspect_eval(strux_stub(arm=arm), model=scripted(answer), log_dir=str(log_dir), display="none")
    assert log.status == "success", log.error
    return log


def test_tasks_load():
    specs = load_specs()
    ids = [s.id for s in specs]
    assert ids and len(ids) == len(set(ids))
    assert all(UNAVAILABLE in s.prompt for s in specs), "a prompt must say what to submit without strux"


def test_arms_differ_only_in_strux():
    with_, without = (sandbox_files(a) for a in ARMS)
    assert "bin/strux" in with_ and "bin/strux" not in without
    a, b = samples("with_strux"), samples("without_strux")
    assert [(s.input, s.setup) for s in a] == [(s.input, s.setup) for s in b]
    assert [s.target for s in a] != [s.target for s in b]


def test_expected_answers_per_arm():
    spec = load_specs()[0]
    assert expected(spec, "without_strux") == UNAVAILABLE
    assert expected(spec, "with_strux") not in ("", UNAVAILABLE)


def test_unknown_arm_is_rejected():
    with pytest.raises(ValueError):
        sandbox_files("with_something_else")


def bash_result(log):
    (message,) = [m for m in log.samples[0].messages if m.role == "tool" and m.function == "bash"]
    return message


def accuracy(log) -> float:
    return log.results.scores[0].metrics["accuracy"].value


def test_with_strux_scores_the_real_version(tmp_path):
    version = expected(load_specs()[0], "with_strux")
    log = run("with_strux", version, tmp_path)
    result = bash_result(log)
    assert result.error is None and json.loads(result.text)["version"] == version
    assert accuracy(log) == 1.0


def test_without_strux_unavailable_is_correct(tmp_path):
    log = run("without_strux", UNAVAILABLE, tmp_path)
    result = bash_result(log)
    assert "bin/strux" in result.text and "version" not in result.text
    assert accuracy(log) == 1.0


def test_inventing_a_version_without_strux_is_wrong(tmp_path):
    assert accuracy(run("without_strux", "v9.9.9", tmp_path)) == 0.0


def test_saying_unavailable_with_strux_is_wrong(tmp_path):
    assert accuracy(run("with_strux", UNAVAILABLE, tmp_path)) == 0.0


@pytest.mark.parametrize(
    ("answer", "ok"),
    [
        ("v0.0.1", True),
        (" `v0.0.1`\n", True),
        ('"v0.0.1"', True),
        ("v0.0.12", False),
        ("maybe v0.0.1?", False),
        ("", False),
    ],
)
def test_scorer_is_an_exact_match(answer, ok):
    assert (normalize(answer) == "v0.0.1") is ok


@pytest.mark.parametrize("agent", ["claude_code", "codex_cli"])
def test_live_agents_need_docker_and_a_pinned_version(agent):
    with pytest.raises(ValueError, match="Docker"):
        strux_stub(agent=agent, agent_version="1.2.3")
    for floating in ("", "auto", "latest"):
        with pytest.raises(ValueError, match="pin"):
            strux_stub(agent=agent, agent_version=floating, sandbox="docker")


@pytest.mark.parametrize("agent", ["claude_code", "codex_cli"])
def test_live_agents_build_when_pinned_in_docker(agent):
    assert strux_stub(arm="with_strux", agent=agent, agent_version="1.2.3", sandbox="docker").solver is not None


def test_unknown_agent_is_rejected():
    with pytest.raises(ValueError, match="unknown agent"):
        strux_stub(arm="with_strux", agent="codex")
