"""A baseline is the with-strux pass rate of a known-good version of strux, kept in the repository.
A later run is checked against it, so a change that makes agents do worse with strux is caught.

    uv run python -m strux_evals.baseline record baselines/main.json logs/with_strux --note "strux v0.1.0"
    uv run python -m strux_evals.baseline check baselines/main.json logs/with_strux

A group is a task, agent, agent version and model, as in strux_evals.compare. A run is a regression
when its pass rate is lower than the baseline's by more than chance would explain: an exact one-sided
Fisher test below ALPHA. With few trials the test can only catch large drops, so the check also
fails a group with fewer than MIN_TRIALS trials, which could not show anything either way.

check exits 0 when no group regressed, 1 when one did or could not be checked, and 2 for a bad
command line or unreadable input."""

import argparse
import json
import math
import sys
from dataclasses import asdict, dataclass
from pathlib import Path

from .compare import Trial, read_trials

VERSION = 1
ALPHA = 0.05
MIN_TRIALS = 10
ARM = "with_strux"


@dataclass(frozen=True)
class Entry:
    task: str
    agent: str
    agent_version: str
    model: str
    passes: int
    trials: int

    @property
    def key(self) -> tuple[str, str, str, str]:
        return (self.task, self.agent, self.agent_version, self.model)

    def __str__(self) -> str:
        return f"{self.task} agent={self.agent or '-'} {self.agent_version} model={self.model}".replace("  ", " ")


def fisher_lower(passes: int, trials: int, base_passes: int, base_trials: int) -> float:
    """The chance, if the pass rate had not changed, of at most `passes` of `trials` passing, given
    the passes seen in both runs together: an exact one-sided Fisher p-value for "now lower"."""
    if min(trials, base_trials) <= 0 or not (0 <= passes <= trials and 0 <= base_passes <= base_trials):
        raise ValueError("passes and trials must be consistent and trials positive")
    total, passed = trials + base_trials, passes + base_passes
    ways = math.comb(total, trials)
    return sum(math.comb(passed, i) * math.comb(total - passed, trials - i) for i in range(passes + 1)) / ways


def entries_of(trials: list[Trial]) -> list[Entry]:
    """The with-strux arm's scored trials of each group. Errored trials are left out."""
    counts: dict[tuple, list[int]] = {}
    for t in trials:
        if t.arm != ARM or t.errored:
            continue
        passes_trials = counts.setdefault(t.group, [0, 0])
        passes_trials[0] += t.correct
        passes_trials[1] += 1
    return [Entry(*key, passes=p, trials=n) for key, (p, n) in sorted(counts.items())]


def write(path: Path, entries: list[Entry], note: str) -> None:
    if not entries:
        raise ValueError(f"no {ARM} trials to record")
    path.parent.mkdir(parents=True, exist_ok=True)
    data = {"version": VERSION, "arm": ARM, "note": note, "groups": [asdict(e) for e in entries]}
    path.write_text(json.dumps(data, indent=2) + "\n")


def read(path: Path) -> list[Entry]:
    try:
        data = json.loads(path.read_text())
        if data["version"] != VERSION or data["arm"] != ARM:
            raise ValueError(f"{path}: not a version {VERSION} {ARM} baseline")
        entries = [Entry(**g) for g in data["groups"]]
    except (OSError, KeyError, TypeError, json.JSONDecodeError) as e:
        raise ValueError(f"{path}: cannot read the baseline: {e}") from e
    if not entries:
        raise ValueError(f"{path}: the baseline has no groups")
    return entries


@dataclass(frozen=True)
class Verdict:
    group: Entry
    ok: bool
    message: str


def check(baseline: list[Entry], now: list[Entry]) -> list[Verdict]:
    current = {e.key: e for e in now}
    verdicts = []
    for base in baseline:
        entry = current.get(base.key)
        if entry is None:
            verdicts.append(Verdict(base, False, "no trials in this run"))
        elif entry.trials < MIN_TRIALS:
            verdicts.append(Verdict(base, False, f"only {entry.trials} trials; at least {MIN_TRIALS} are needed"))
        else:
            p = fisher_lower(entry.passes, entry.trials, base.passes, base.trials)
            line = (
                f"{entry.passes}/{entry.trials} = {entry.passes / entry.trials:.0%} "
                f"against a baseline of {base.passes}/{base.trials} = {base.passes / base.trials:.0%}"
            )
            if p < ALPHA:
                verdicts.append(Verdict(base, False, f"regression: {line} (p = {p:.3f})"))
            else:
                verdicts.append(Verdict(base, True, f"{line} (p = {p:.3f}, not lower)"))
    return verdicts


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)
    record = sub.add_parser("record", help="write a baseline from the logs of a known-good run")
    record.add_argument("baseline", type=Path)
    record.add_argument("logs", nargs="+", type=Path)
    record.add_argument("--note", default="", help="what the baseline was made from, such as a strux version")
    check_ = sub.add_parser("check", help="check a run against a baseline")
    check_.add_argument("baseline", type=Path)
    check_.add_argument("logs", nargs="+", type=Path)
    try:
        args = parser.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0
    try:
        entries = entries_of(read_trials(args.logs))
        if args.command == "record":
            write(args.baseline, entries, args.note)
            print(f"recorded {len(entries)} group(s) in {args.baseline}")
            return 0
        verdicts = check(read(args.baseline), entries)
    except ValueError as e:
        print(f"baseline: {e}", file=sys.stderr)
        return 2
    for v in verdicts:
        print(f"{'ok  ' if v.ok else 'FAIL'} {v.group}: {v.message}")
    return 0 if all(v.ok for v in verdicts) else 1


if __name__ == "__main__":
    sys.exit(main())
