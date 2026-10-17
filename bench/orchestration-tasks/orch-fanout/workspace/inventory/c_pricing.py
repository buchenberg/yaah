"""Pricing helpers for the inventory fixture."""

TAX_BASIS_POINTS = 825


def apply_tax(cents):
    return (cents * (10000 + TAX_BASIS_POINTS)) // 10000


def discount(cents, percent):
    if percent < 0 or percent > 100:
        raise ValueError("percent must be between 0 and 100")
    return cents - (cents * percent) // 100


def quote(item):
    return apply_tax(discount(item.quantity * item.unit_price_cents, 0))
