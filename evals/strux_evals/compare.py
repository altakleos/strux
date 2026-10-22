"""Sets the two arms of a comparison side by side, from the logs of runs that already happened.

    uv run python -m strux_evals.compare logs/with_strux logs/without_strux

Runs are grouped by task, agent, agent version and model, and only the arm differs within a group,
so a group compares like with like. Every sample is one trial. Its pass rate is the share of
trials the scorer marked correct; a trial that errored (a harness or sandbox fault) is counted
apart and left out of the rate, so a fault is not read as the agent failing."""

import argparse
import json
import math
import sys
from collections import defaultdict
from collections.abc import Iterable
from dataclasses import asdict, dataclass, field
from pathlib import Path

from inspect_ai.log import EvalLog, read_eval_log
from inspect_ai.scorer import CORRECT

from .arms import ARMS


@dataclass(frozen=True)
class Trial:
    group: tuple[str, str, str, str]  # task, agent, agent version, model
    arm: str
    correct: bool
    errored: bool
    tokens: int
    seconds: float


@dataclass
class Stats:
    trials: int = 0
    errors: int = 0
    passes: int = 0
    rate: float | None = None
    low: float | None = None  # a 95% Wilson interval for the pass rate
    high: float | None = None
    mean_tokens: float | None = None
    mean_seconds: float | None = None


@dataclass
class Group:
    task: str
    agent: str
    agent_version: str
    model: str
    arms: dict[str, Stats] = field(default_factory=dict)
    difference: float | None = None  # with_strux rate minus without_strux rate


def wilson(passes: int, trials: int, z: float = 1.96) -> tuple[float, float]:
    """A 95% confidence interval for a share, which stays inside 0..1 and is usable at 0 and n."""
    if trials == 0:
        raise ValueError("no trials")
    p = passes / trials
    denominator = 1 + z * z / trials
    centre = (p + z * z / (2 * trials)) / denominator
    half = z * math.sqrt(p * (1 - p) / trials + z * z / (4 * trials * trials)) / denominator
    return max(0.0, centre - half), min(1.0, centre + half)


def trials_of(log: EvalLog) -> list[Trial]:
    arm = (log.eval.metadata or {}).get("arm")
    if arm not in ARMS:
        raise ValueError(f"{log.location}: no known arm in the log's metadata (got {arm!r})")
    meta = log.eval.metadata or {}
    group = (log.eval.task, str(meta.get("agent", "")), str(meta.get("agent_version", "")), log.eval.model)
    trials = []
    for sample in log.samples or []:
        score = next(iter((sample.scores or {}).values()), None)
        trials.append(
            Trial(
                group=group,
                arm=arm,
                correct=score is not None and score.value == CORRECT,
                errored=sample.error is not None or score is None,
                tokens=sum(u.total_tokens or 0 for u in (sample.model_usage or {}).values()),
                seconds=sample.total_time or 0.0,
            )
        )
    return trials


def read_trials(paths: Iterable[Path]) -> list[Trial]:
    files = []
    for path in paths:
        found = sorted(path.rglob("*.eval")) if path.is_dir() else [path]
        if not found:
            raise ValueError(f"no .eval logs in {path}")
        files += found
    trials = []
    for file in files:
        log = read_eval_log(str(file))
        if log.status != "success":
            raise ValueError(f"{file}: the run ended with status {log.status!r}, so its trials are incomplete")
        trials += trials_of(log)
    return trials


def summarize(trials: list[Trial]) -> list[Group]:
    by_group: dict[tuple, dict[str, list[Trial]]] = defaultdict(lambda: defaultdict(list))
    for trial in trials:
        by_group[trial.group][trial.arm].append(trial)
    groups = []
    for key in sorted(by_group):
        group = Group(*key)
        for arm in ARMS:
            if arm not in by_group[key]:
                continue
            ts = by_group[key][arm]
            scored = [t for t in ts if not t.errored]
            stats = Stats(trials=len(scored), errors=len(ts) - len(scored), passes=sum(t.correct for t in scored))
            if scored:
                stats.rate = stats.passes / stats.trials
                stats.low, stats.high = wilson(stats.passes, stats.trials)
                stats.mean_tokens = sum(t.tokens for t in scored) / stats.trials
                stats.mean_seconds = sum(t.seconds for t in scored) / stats.trials
            group.arms[arm] = stats
        with_, without = (group.arms.get(a) for a in ARMS)
        if with_ and without and with_.rate is not None and without.rate is not None:
            group.difference = with_.rate - without.rate
        groups.append(group)
    return groups


def render(groups: list[Group]) -> str:
    lines = []
    for g in groups:
        lines.append(f"{g.task}  agent={g.agent or '-'} {g.agent_version or ''}  model={g.model}".rstrip())
        for arm in ARMS:
            s = g.arms.get(arm)
            if s is None:
                lines.append(f"  {arm:<14} (no runs)")
            elif s.rate is None:
                lines.append(f"  {arm:<14} no scored trials; {s.errors} errored")
            else:
                lines.append(
                    f"  {arm:<14} {s.passes}/{s.trials} pass = {s.rate:.0%} (95% {s.low:.0%}-{s.high:.0%})"
                    f"  tokens {s.mean_tokens:,.0f}  seconds {s.mean_seconds:,.1f}  errored {s.errors}"
                )
        if g.difference is not None:
            lines.append(f"  difference     {g.difference:+.0%} (with_strux minus without_strux)")
            with_, without = g.arms["with_strux"], g.arms["without_strux"]
            if with_.low <= without.high and without.low <= with_.high:
                lines.append("                 the intervals overlap: too few trials to call this a difference")
        lines.append("")
    return "\n".join(lines).rstrip() + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("paths", nargs="+", type=Path, help="eval logs, or directories holding them")
    parser.add_argument("--json", action="store_true", help="write the groups as JSON")
    args = parser.parse_args(argv)
    try:
        groups = summarize(read_trials(args.paths))
    except ValueError as e:
        print(f"compare: {e}", file=sys.stderr)
        return 1
    print(
        json.dumps([asdict(g) for g in groups], indent=2) if args.json else render(groups),
        end="\n" if args.json else "",
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
