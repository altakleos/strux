"""The offline wiring check: both arms build, the agent runs in the sandbox, the scorer scores.
A scripted model stands in for the agent, so it needs no API key and costs nothing."""

import json
from pathlib import Path

import pytest
from inspect_ai import eval as inspect_eval
from inspect_ai.model import ModelOutput, get_model

from strux_evals.arms import ARMS, is_elf, sandbox_files, strux_binary
from strux_evals.dataset import UNAVAILABLE, Spec, expected, field_of, load_specs, samples, task_files
from strux_evals.task import is_correct, strux_stub

REPO = Path(__file__).resolve().parents[2]


def scripted(answer: str):
    """A model that runs strux once, then submits answer."""
    return get_model(
        "mockllm/model",
        custom_outputs=[
            ModelOutput.for_tool_call("mockllm/model", "bash", {"command": "bin/strux version --json"}),
            ModelOutput.for_tool_call("mockllm/model", "submit", {"answer": answer}, content=""),
        ],
    )


def version_spec():
    (spec,) = [s for s in load_specs() if s.id == "version"]
    return spec


def run(arm: str, answer: str, log_dir: Path):
    (log,) = inspect_eval(
        strux_stub(arm=arm, tasks="version"), model=scripted(answer), log_dir=str(log_dir), display="none"
    )
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
    spec = version_spec()
    assert expected(spec, "without_strux") == UNAVAILABLE
    assert expected(spec, "with_strux") not in ("", UNAVAILABLE)


def test_docker_layout_is_absolute_and_the_prompt_names_it():
    a, b = samples("with_strux", bin="/work/bin"), samples("without_strux", bin="/work/bin")
    assert all("/work/bin" in s.input and "{bin}" not in s.input for s in a + b)
    assert all("/work/bin/strux" in s.files and "/work/bin/.keep" not in s.files for s in a)
    assert all("/work/bin/.keep" in s.files and "/work/bin/strux" not in s.files for s in b)
    assert all(name.startswith("/work/") for s in a + b for name in s.files)
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
    version = expected(version_spec(), "with_strux")
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
    spec = version_spec()
    for version in ("v1", "v22"):
        binary.write_text(f'#!/bin/sh\necho \'{{"version":"{version}"}}\'\n')
        binary.chmod(0o755)
        assert expected(spec, "with_strux") == version


def test_unknown_agent_is_rejected():
    with pytest.raises(ValueError, match="unknown agent"):
        strux_stub(arm="with_strux", agent="codex")


def test_a_task_names_the_tasks_it_runs():
    assert [s.id for s in samples("without_strux", ids=("version",))] == ["version"]
    with pytest.raises(ValueError, match="no task"):
        samples("without_strux", ids=("no-such-task",))


def test_a_field_is_a_dotted_path():
    out = {"packages": [{"package": "notes", "digest": "sha256:ab"}], "problems": []}
    assert field_of(out, "packages.0.digest") == "sha256:ab"
    assert field_of(out, "problems") == []


def test_task_files_lie_in_the_working_directory(tmp_path):
    (tmp_path / "t.yaml").write_text(
        "id: t\nprompt: Read {work}/a.txt with the programs in {bin}, or say unavailable.\n"
        "files:\n  a.txt: hello\n  sub/b.txt: there\n"
        "expect:\n  run: [read]\n  field: text\n"
    )
    (spec,) = load_specs(tmp_path)
    assert spec.files == (("a.txt", "hello"), ("sub/b.txt", "there"))
    local, docker = task_files(spec), task_files(spec, bin="/work/bin")
    assert list(local) == ["a.txt", "sub/b.txt"] and list(docker) == ["/work/a.txt", "/work/sub/b.txt"]
    assert all(v.startswith("data:text/plain;base64,") for v in local.values())
    a, b = samples("without_strux", specs=[spec]), samples("without_strux", specs=[spec], bin="/work/bin")
    assert a[0].input == "Read ./a.txt with the programs in bin, or say unavailable."
    assert b[0].input == "Read /work/a.txt with the programs in /work/bin, or say unavailable."


@pytest.mark.parametrize("name", ["/abs.txt", "../out.txt", "a/../b.txt", "", "./a.txt"])
def test_a_task_file_is_a_plain_relative_path(tmp_path, name):
    (tmp_path / "t.yaml").write_text(
        f"id: t\nprompt: unavailable\nfiles:\n  {name!r}: x\nexpect:\n  run: [x]\n  field: x\n"
    )
    with pytest.raises(ValueError, match="plain relative path"):
        load_specs(tmp_path)


def test_strux_runs_beside_the_task_files(tmp_path, monkeypatch):
    binary = tmp_path / "strux"
    binary.write_text('#!/bin/sh\nprintf \'{"text":"%s"}\' "$(cat a.txt)"\n')
    binary.chmod(0o755)
    monkeypatch.setenv("STRUX_BIN", str(binary))
    spec = Spec("t", "unavailable", ("read",), "text", (("a.txt", "hello"),))
    assert expected(spec, "with_strux") == "hello"


def test_task_dirs_are_copied_into_the_working_directory(tmp_path, monkeypatch):
    (tmp_path / "t.yaml").write_text(
        "id: t\nprompt: Read {work}/copy, or say unavailable.\ndirs:\n  copy: evals/tasks\n"
        "expect:\n  run: [read]\n  field: text\n"
    )
    (spec,) = load_specs(tmp_path)
    assert spec.dirs == (("copy", "evals/tasks"),)
    files = task_files(spec, bin="/work/bin")
    assert "/work/copy/version.yaml" in files and all(v.startswith("data:") for v in files.values())
    binary = tmp_path / "strux"
    binary.write_text('#!/bin/sh\nprintf \'{"text":"%s"}\' "$(head -c 5 copy/version.yaml)"\n')
    binary.chmod(0o755)
    monkeypatch.setenv("STRUX_BIN", str(binary))
    assert expected(spec, "with_strux") == "# The"


@pytest.mark.parametrize("path", ["../outside", "/abs", "evals/no-such-dir"])
def test_a_task_dir_is_a_directory_of_the_repository(tmp_path, path):
    (tmp_path / "t.yaml").write_text(
        f"id: t\nprompt: unavailable\ndirs:\n  d: {path!r}\nexpect:\n  run: [x]\n  field: x\n"
    )
    with pytest.raises(ValueError):
        load_specs(tmp_path)
