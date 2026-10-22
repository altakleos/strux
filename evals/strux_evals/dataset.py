"""Tasks are data, in tasks/*.yaml, so they do not depend on the harness that runs them."""

import functools
import json
import subprocess
from dataclasses import dataclass
from pathlib import Path

import yaml
from inspect_ai.dataset import Sample

from .arms import sandbox_files, setup_script, strux_binary

TASKS_DIR = Path(__file__).resolve().parent.parent / "tasks"

# What a task's prompt tells the agent to submit when strux is not available to it.
UNAVAILABLE = "unavailable"


@dataclass(frozen=True)
class Spec:
    id: str
    prompt: str
    run: tuple[str, ...]
    field: str


def load_specs(directory: Path = TASKS_DIR) -> list[Spec]:
    specs = []
    for path in sorted(directory.glob("*.yaml")):
        raw = yaml.safe_load(path.read_text())
        specs.append(Spec(raw["id"], raw["prompt"].strip(), tuple(raw["expect"]["run"]), raw["expect"]["field"]))
    if not specs:
        raise ValueError(f"no tasks in {directory}")
    return specs


@functools.cache
def _strux_says(binary: str, run: tuple[str, ...], field: str) -> str:
    out = subprocess.run([binary, *run], check=True, capture_output=True, text=True).stdout
    answer = str(json.loads(out)[field]).strip()
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
    return _strux_says(str(strux_binary()), spec.run, spec.field)


def samples(arm: str, specs: list[Spec] | None = None) -> list[Sample]:
    return [
        Sample(
            id=spec.id,
            input=spec.prompt,
            target=expected(spec, arm),
            files=sandbox_files(arm),
            setup=setup_script(),
            metadata={"arm": arm},
        )
        for spec in specs or load_specs()
    ]
