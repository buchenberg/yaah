"""Domain models for the inventory fixture."""

from dataclasses import dataclass


@dataclass
class Item:
    sku: str
    quantity: int
    unit_price_cents: int


def make_item(sku, quantity, unit_price_cents):
    return Item(sku=sku, quantity=int(quantity), unit_price_cents=int(unit_price_cents))


def total_value(items):
    return sum(item.quantity * item.unit_price_cents for item in items)
