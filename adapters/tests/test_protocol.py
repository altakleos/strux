import io
import json

import pytest

from strux_adapters.protocol import NoResponse, Request, Response, read_request, serve

REQUEST = {
    "model": "m",
    "effort": "low",
    "max_tokens": 9,
    "system": "govern",
    "data": "examine",
    "reminder": "close",
    "schema": {"type": "object"},
}


def test_a_request_is_read_with_its_channels_apart():
    r = read_request(io.StringIO(json.dumps(REQUEST)))
    assert (r.system, r.data, r.reminder, r.model, r.max_tokens) == ("govern", "examine", "close", "m", 9)
    assert r.instructions == "govern\n\nclose"
    assert read_request(io.StringIO(json.dumps({**REQUEST, "reminder": ""}))).instructions == "govern"


@pytest.mark.parametrize(
    "change",
    [
        {"tools": ["Bash"]},
        {"system": None},
        {"max_tokens": "9"},
        {"max_tokens": True},
        {"schema": []},
    ],
)
def test_a_request_outside_the_form_is_refused(change):
    with pytest.raises(ValueError):
        read_request(io.StringIO(json.dumps({**REQUEST, **change})))
    with pytest.raises(ValueError):
        read_request(io.StringIO("not json"))


def test_optional_fields_may_be_left_out():
    raw = {k: v for k, v in REQUEST.items() if k not in ("effort", "reminder")}
    r = read_request(io.StringIO(json.dumps(raw)))
    assert (r.effort, r.reminder) == ("", "")


def test_serve_writes_the_response_or_the_error():
    out = io.StringIO()
    assert (
        serve(lambda r: Response(text=r.data, stop="done", input_tokens=3), io.StringIO(json.dumps(REQUEST)), out) == 0
    )
    assert json.loads(out.getvalue()) == {
        "text": "examine",
        "stop": "done",
        "usage": {"input_tokens": 3, "output_tokens": 0, "cache_read_tokens": 0, "cache_write_tokens": 0},
    }

    def busy(_):
        raise NoResponse("rate limited")

    out = io.StringIO()
    assert serve(busy, io.StringIO(json.dumps(REQUEST)), out) == 0
    assert json.loads(out.getvalue())["error"] == "rate limited"
    assert serve(busy, io.StringIO("{}"), io.StringIO()) == 2


def test_a_response_holds_only_stops_the_evaluator_knows():
    with pytest.raises(ValueError):
        Response(stop="finished")


def request(**change):
    return Request(**{**REQUEST, **change})
