"""Small aggregation helpers."""


def mean(values):
    if not values:
        return 0.0
    return sum(values) / len(values)


def _variance(values):
    m = mean(values)
    return sum((v - m) ** 2 for v in values) / len(values)


def summarize(values):
    return {"mean": mean(values), "variance": _variance(values)}
