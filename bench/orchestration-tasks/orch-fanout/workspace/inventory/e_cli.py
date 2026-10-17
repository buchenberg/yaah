"""Command-line entry point for the inventory fixture."""

import sys

from . import a_models, b_storage, c_pricing, d_reports


def seed_store():
    b_storage.clear()
    rows = [("widget", 3, 1250), ("gadget", 2, 999)]
    for item in b_storage.load_all(rows):
        b_storage.put(item)


def run(argv):
    seed_store()
    items = [a_models.make_item(*row) for row in (("widget", 3, 1250), ("gadget", 2, 999))]
    total = sum(c_pricing.quote(item) for item in items)
    print(d_reports.as_json(items))
    print(f"total_cents={total}")
    return 0


def main():
    return run(sys.argv[1:])
