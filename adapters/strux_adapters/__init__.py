"""Adapters through which the strux evaluator reaches a model service.

Each adapter is a program that reads one request from its standard input and writes one response to
its standard output, both JSON, in the form `protocol` defines and the evaluate package of strux
reads. An adapter signs in to its service as the service's own tools do, so strux holds no
credential.
"""
