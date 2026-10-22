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
from strux_evals.dataset import expected, load_specs, samples
from strux_evals.task import strux_stub

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
            ModelOutput.for_tool_call("mockllm/model", "submit", {"answer": answer}),
        ],
    )


def run(arm: str, answer: str, log_dir: Path):
    (log,) = inspect_eval(strux_stub(arm=arm), model=scripted(answer), log_dir=str(log_dir), display="none")
    assert log.status == "success", log.error
    return log


def test_tasks_load():
    assert [s.id for s in load_specs()] == ["version"]


def test_arms_differ_only_in_strux():
    with_, without = (sandbox_files(a) for a in ARMS)
    assert "bin/strux" in with_ and "bin/strux" not in without
    a, b = samples("with_strux"), samples("without_strux")
    assert [(s.input, s.target, s.setup) for s in a] == [(s.input, s.target, s.setup) for s in b]


def test_unknown_arm_is_rejected():
    with pytest.raises(ValueError):
        sandbox_files("with_something_else")


def bash_result(log):
    (message,) = [m for m in log.samples[0].messages if m.role == "tool" and m.function == "bash"]
    return message


def test_with_strux_scores_the_real_version(tmp_path):
    version = expected(load_specs()[0])
    log = run("with_strux", version, tmp_path)
    result = bash_result(log)
    assert result.error is None and json.loads(result.text)["version"] == version
    assert log.results.scores[0].metrics["accuracy"].value == 1.0


def test_wrong_answer_scores_zero(tmp_path):
    log = run("with_strux", "unavailable", tmp_path)
    assert log.results.scores[0].metrics["accuracy"].value == 0.0


def test_without_strux_the_command_fails_in_the_sandbox(tmp_path):
    log = run("without_strux", "unavailable", tmp_path)
    result = bash_result(log)
    assert "bin/strux: No such file or directory" in result.text
    assert log.results.scores[0].metrics["accuracy"].value == 0.0
