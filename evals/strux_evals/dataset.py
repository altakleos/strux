"""Tasks are data, in tasks/*.yaml, so they do not depend on the harness that runs them."""

import base64
import functools
import json
import shutil
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

import yaml
from inspect_ai.dataset import Sample

from .arms import LOCAL_BIN, sandbox_files, setup_script, strux_binary, work_dir

TASKS_DIR = Path(__file__).resolve().parent.parent / "tasks"

# The repository, which the directories a task gives the agent are named relative to.
REPO = Path(__file__).resolve().parents[2]

# What a task's prompt tells the agent to submit when strux is not available to it.
UNAVAILABLE = "unavailable"


@dataclass(frozen=True)
class Spec:
    id: str
    prompt: str
    run: tuple[str, ...]
    field: str
    # The files the agent works on, as (name, contents), each placed in the working directory of
    # the sandbox, which the prompt names as {work}, and where strux runs to give the answer.
    files: tuple[tuple[str, str], ...] = ()
    # The directories of the repository the agent works on, as (name, path from the repository),
    # each copied into the working directory under its name, as the files are.
    dirs: tuple[tuple[str, str], ...] = ()


def _file_name(task: str, name: str) -> str:
    path = PurePosixPath(name)
    if path.is_absolute() or not path.parts or ".." in path.parts or name != str(path):
        raise ValueError(f"task {task}: file {name!r} is not a plain relative path")
    return name


def _repo_dir(task: str, path: str) -> str:
    """A directory of the repository, named by a plain relative path."""
    _file_name(task, path)
    if not (REPO / path).is_dir():
        raise ValueError(f"task {task}: {path!r} is no directory of the repository")
    return path


def _dir_files(path: str) -> list[tuple[str, bytes]]:
    """Every file under a directory of the repository, by its path in the directory, in order."""
    root = REPO / path
    return [(f.relative_to(root).as_posix(), f.read_bytes()) for f in sorted(root.rglob("*")) if f.is_file()]


def load_specs(directory: Path = TASKS_DIR) -> list[Spec]:
    specs = []
    for path in sorted(directory.glob("*.yaml")):
        raw = yaml.safe_load(path.read_text())
        files = tuple(
            sorted((_file_name(raw["id"], name), contents) for name, contents in raw.get("files", {}).items())
        )
        dirs = tuple(
            sorted(
                (_file_name(raw["id"], name), _repo_dir(raw["id"], path)) for name, path in raw.get("dirs", {}).items()
            )
        )
        specs.append(
            Spec(raw["id"], raw["prompt"].strip(), tuple(raw["expect"]["run"]), raw["expect"]["field"], files, dirs)
        )
    if not specs:
        raise ValueError(f"no tasks in {directory}")
    return specs


def field_of(value, field: str):
    """The value at a dotted path in strux's JSON output, such as packages.0.digest: a key of an
    object, or the index of an item of a list."""
    for step in field.split("."):
        value = value[int(step)] if isinstance(value, list) else value[step]
    return value


@functools.cache
def _strux_says(
    binary: str,
    stamp: tuple[int, int],
    run: tuple[str, ...],
    field: str,
    files: tuple[tuple[str, str], ...],
    dirs: tuple[tuple[str, str], ...] = (),
) -> str:
    # stamp is the binary's modification time and size, so a rebuilt binary is run again. strux
    # runs in a directory that holds the task's files and directories, as the agent's does.
    with tempfile.TemporaryDirectory() as work:
        for name, contents in files:
            target = Path(work, name)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(contents)
        for name, path in dirs:
            shutil.copytree(REPO / path, Path(work, name))
        out = subprocess.run([binary, *run], check=True, capture_output=True, text=True, cwd=work).stdout
    answer = str(field_of(json.loads(out), field)).strip()
    if not answer or answer == UNAVAILABLE:
        raise ValueError(f"strux {' '.join(run)} gave no usable {field!r}: {out!r}")
    return answer


def expected(spec: Spec, arm: str) -> str:
    """The answer a correct agent gives in an arm: what strux reports when it is installed, and
    that it is unavailable when it is not. What strux reports is taken from the binary under test,
    so a wrong version from strux is for strux's own tests to catch (internal/version), not these
    evals, which measure whether an agent reports what is in its sandbox."""
    if arm == "without_strux":
        return UNAVAILABLE
    binary = strux_binary()
    info = binary.stat()
    return _strux_says(str(binary), (info.st_mtime_ns, info.st_size), spec.run, spec.field, spec.files, spec.dirs)


def task_files(spec: Spec, bin: str = LOCAL_BIN) -> dict[str, str]:
    """A task's files, as Inspect's Sample.files maps them: at their names in the working
    directory, given as data URIs so that Inspect never reads contents as a path on the host."""
    work = work_dir(bin)

    def at(name: str) -> str:
        return name if work == "." else f"{work}/{name}"

    out = {
        at(name): "data:text/plain;base64," + base64.b64encode(contents.encode()).decode()
        for name, contents in spec.files
    }
    for name, path in spec.dirs:
        for sub, data in _dir_files(path):
            out[at(f"{name}/{sub}")] = "data:application/octet-stream;base64," + base64.b64encode(data).decode()
    return out


def samples(arm: str, specs: list[Spec] | None = None, bin: str = LOCAL_BIN, ids: tuple[str, ...] = ()) -> list[Sample]:
    chosen = [spec for spec in specs or load_specs() if not ids or spec.id in ids]
    if ids and {spec.id for spec in chosen} != set(ids):
        raise ValueError(f"no task {sorted(set(ids) - {spec.id for spec in chosen})}")
    return [
        Sample(
            id=spec.id,
            input=spec.prompt.format(bin=bin, work=work_dir(bin)),
            target=expected(spec, arm),
            files=sandbox_files(arm, bin) | task_files(spec, bin),
            setup=setup_script(bin),
            metadata={"arm": arm},
        )
        for spec in chosen
    ]
