"""Report rendering for the inventory fixture."""

import json


def summary(items):
    return {
        "count": len(items),
        "units": sum(item.quantity for item in items),
        "skus": sorted(item.sku for item in items),
    }


def as_json(items):
    return json.dumps(summary(items), sort_keys=True)


def as_lines(items):
    data = summary(items)
    return [f"count={data['count']}", f"units={data['units']}", f"skus={','.join(data['skus'])}"]
