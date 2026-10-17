"""Assorted helpers for the inventory fixture."""

import math


def chunks(values, size):
    for start in range(0, len(values), size):
        yield values[start : start + size]


def pad_sku(sku):
    return sku.strip().upper().ljust(8, "0")


def safe_div(numerator, denominator):
    if denominator == 0:
        return math.inf
    return numerator / denominator
