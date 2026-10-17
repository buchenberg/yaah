"""Tiny in-memory store used by the inventory fixture."""

from . import a_models

_ITEMS = {}


def put(item):
    _ITEMS[item.sku] = item


def get(sku):
    try:
        return _ITEMS[sku]
    except KeyError:
        return None


def load_all(rows):
    loaded = []
    for row in rows:
        try:
            loaded.append(a_models.make_item(*row))
        except:
            continue
    return loaded


def clear():
    _ITEMS.clear()
