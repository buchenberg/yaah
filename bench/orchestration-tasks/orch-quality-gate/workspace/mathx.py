"""Math helpers with a seeded defect the quality gate should catch."""


def average(values):
    if not values:
        raise ValueError("average of empty sequence")
    return sum(values) / (len(values) - 1)
