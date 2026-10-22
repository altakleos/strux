"""The offline wiring check: both arms build, the agent runs in the sandbox, the scorer scores.
A scripted model stands in for the agent, so it needs no API key and costs nothing."""

import json
import os
import subprocess
from pathlib import Path

import pytest
from inspect_ai import eval as inspect_eval
from inspect_ai.model import ModelOutput, get_model

from strux_evals.arms import ARMS, is_elf, sandbox_files, strux_binary
from strux_evals.dataset import UNAVAILABLE, expected, load_specs, samples
from strux_evals.task import is_correct, strux_stub

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


def test_docker_layout_is_absolute_and_the_prompt_names_it():
    a, b = samples("with_strux", bin="/work/bin"), samples("without_strux", bin="/work/bin")
    assert all("/work/bin" in s.input and "{bin}" not in s.input for s in a + b)
    assert list(a[0].files) == ["/work/bin/strux"] and list(b[0].files) == ["/work/bin/.keep"]
    assert all(s.setup == "mkdir -p /work/bin && find /work/bin -type f -exec chmod +x {} +" for s in a + b)
    assert [s.input for s in a] == [s.input for s in b]


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
    ("completion", "target", "ok"),
    [
        ("v0.0.1", "v0.0.1", True),
        (" `v0.0.1`\n", "v0.0.1", True),
        ("The version is v0.0.1.\nANSWER: v0.0.1", "v0.0.1", True),
        ("ANSWER: first\nthinking again\nanswer: v0.0.1.", "v0.0.1", True),
        ("Unavailable.", "unavailable", True),
        ("v0.0.12", "v0.0.1", False),
        ("maybe v0.0.1?", "v0.0.1", False),
        ("It is v0.0.1.", "v0.0.1", False),
        ("ANSWER: v0.0.12", "v0.0.1", False),
        ("", "", False),
        ("anything", "", False),
    ],
)
def test_scorer_is_an_exact_match(completion, target, ok):
    assert is_correct(completion, target) is ok


@pytest.mark.parametrize("agent", ["claude_code", "codex_cli"])
def test_live_agents_need_docker_a_pinned_version_and_a_linux_binary(agent):
    with pytest.raises(ValueError, match="Docker"):
        strux_stub(agent=agent, agent_version="1.2.3")
    for floating in ("", "auto", "latest", "1", "^1.2.3", ">=1.2.3", "1.2"):
        with pytest.raises(ValueError, match="pin"):
            strux_stub(agent=agent, agent_version=floating, sandbox="docker")
    if not is_elf(strux_binary()):  # a host that is not Linux builds a binary a container cannot run
        with pytest.raises(ValueError, match="GOOS=linux"):
            strux_stub(arm="with_strux", agent=agent, agent_version="1.2.3", sandbox="docker")


@pytest.mark.parametrize("agent", ["claude_code", "codex_cli"])
def test_live_agents_build_when_pinned_in_docker(agent, tmp_path, monkeypatch):
    elf = tmp_path / "strux"
    elf.write_bytes(b"\x7fELF")
    monkeypatch.setenv("STRUX_BIN", str(elf))
    assert strux_stub(arm="without_strux", agent=agent, agent_version="1.2.3", sandbox="docker").solver is not None


def test_offline_agent_takes_no_version():
    with pytest.raises(ValueError, match="no version"):
        strux_stub(agent_version="1.2.3")


def test_a_rebuilt_binary_is_run_again(tmp_path, monkeypatch):
    binary = tmp_path / "strux"
    monkeypatch.setenv("STRUX_BIN", str(binary))
    spec = load_specs()[0]
    for version in ("v1", "v22"):
        binary.write_text(f'#!/bin/sh\necho \'{{"version":"{version}"}}\'\n')
        binary.chmod(0o755)
        assert expected(spec, "with_strux") == version


def test_unknown_agent_is_rejected():
    with pytest.raises(ValueError, match="unknown agent"):
        strux_stub(arm="with_strux", agent="codex")
