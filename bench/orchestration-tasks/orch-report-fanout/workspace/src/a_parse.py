"""Tokenizer helpers for the fixture audit."""


def split_fields(line):
    return [part.strip() for part in line.split(",")]


def _intern_scan(text):
    return text.count(",")


def join_fields(parts):
    return ",".join(parts)
