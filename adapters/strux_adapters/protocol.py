"""The exchange between the strux evaluator and an adapter.

The evaluator writes one request: the model's settings, and the prompt with its governing text
(`system` and `reminder`) apart from its data. The adapter writes one response: the model's text,
why it stopped, and the tokens it used; or, when no response came back and asking again could
succeed, an error alone.
"""

from __future__ import annotations

import json
import sys
from collections.abc import Callable
from dataclasses import dataclass
from typing import IO, Any

# Why a model stopped, as the evaluator reads it.
STOPS = ("done", "refused", "length", "rejected", "other")


@dataclass(frozen=True)
class Request:
    model: str
    effort: str
    max_tokens: int
    system: str
    data: str
    reminder: str
    schema: dict[str, Any]

    @property
    def instructions(self) -> str:
        """The governing text for a service that takes one: the system text, then the reminder."""
        return f"{self.system}\n\n{self.reminder}" if self.reminder else self.system


@dataclass(frozen=True)
class Response:
    text: str = ""
    stop: str = "other"
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0

    def __post_init__(self) -> None:
        if self.stop not in STOPS:
            raise ValueError(f"the stop {self.stop!r} is not one of {', '.join(STOPS)}")

    def to_json(self) -> dict[str, Any]:
        return {
            "text": self.text,
            "stop": self.stop,
            "usage": {
                "input_tokens": self.input_tokens,
                "output_tokens": self.output_tokens,
                "cache_read_tokens": self.cache_read_tokens,
                "cache_write_tokens": self.cache_write_tokens,
            },
        }


class NoResponse(Exception):
    """No response came back from the service, and asking again could succeed."""


_FIELDS: dict[str, type] = {
    "model": str,
    "effort": str,
    "max_tokens": int,
    "system": str,
    "data": str,
    "reminder": str,
    "schema": dict,
}
_OPTIONAL = {"effort": "", "reminder": ""}


def read_request(stream: IO[str]) -> Request:
    """Reads a request, refusing any field it does not know and any it lacks or mistypes."""
    try:
        raw = json.load(stream)
    except json.JSONDecodeError as err:
        raise ValueError(f"the request is not JSON: {err}") from err
    if not isinstance(raw, dict):
        raise ValueError("the request is not an object")
    unknown = sorted(set(raw) - set(_FIELDS))
    if unknown:
        raise ValueError(f"the request has fields no adapter knows: {', '.join(unknown)}")
    values: dict[str, Any] = {}
    for name, kind in _FIELDS.items():
        value = raw.get(name, _OPTIONAL.get(name))
        if value is None or not isinstance(value, kind) or (kind is int and isinstance(value, bool)):
            raise ValueError(f"the request's {name} is missing or not a {kind.__name__}")
        values[name] = value
    return Request(**values)


def serve(judge: Callable[[Request], Response], stdin: IO[str] = sys.stdin, stdout: IO[str] = sys.stdout) -> int:
    """Answers one request with judge and returns the exit status. A request it cannot read exits 2,
    and a response that did not come back is written as an error for the evaluator to retry."""
    try:
        request = read_request(stdin)
    except ValueError as err:
        print(err, file=sys.stderr)
        return 2
    try:
        answer: dict[str, Any] = judge(request).to_json()
    except NoResponse as err:
        answer = {**Response().to_json(), "error": str(err) or "no response"}
    stdout.write(json.dumps(answer))
    stdout.flush()
    return 0
