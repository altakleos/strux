"""Tasks are data, in tasks/*.yaml, so they do not depend on the harness that runs them."""

import json
import subprocess
from dataclasses import dataclass
from pathlib import Path

import yaml
from inspect_ai.dataset import Sample

from .arms import sandbox_files, setup_script, strux_binary

TASKS_DIR = Path(__file__).resolve().parent.parent / "tasks"


@dataclass(frozen=True)
class Spec:
    id: str
    prompt: str
    run: list[str]
    field: str


def load_specs(directory: Path = TASKS_DIR) -> list[Spec]:
    specs = []
    for path in sorted(directory.glob("*.yaml")):
        raw = yaml.safe_load(path.read_text())
        specs.append(Spec(raw["id"], raw["prompt"].strip(), list(raw["expect"]["run"]), raw["expect"]["field"]))
    if not specs:
        raise ValueError(f"no tasks in {directory}")
    return specs


def expected(spec: Spec) -> str:
    """The answer a correct agent gives, from running the strux binary under test."""
    out = subprocess.run([str(strux_binary()), *spec.run], check=True, capture_output=True, text=True).stdout
    return str(json.loads(out)[spec.field])


def samples(arm: str, specs: list[Spec] | None = None) -> list[Sample]:
    return [
        Sample(
            id=spec.id,
            input=spec.prompt,
            target=expected(spec),
            files=sandbox_files(arm),
            setup=setup_script(),
            metadata={"arm": arm},
        )
        for spec in specs or load_specs()
    ]
