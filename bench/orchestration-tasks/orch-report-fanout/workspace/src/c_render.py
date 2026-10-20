"""Rendering primitives for the fixture audit."""


def render_row(cells):
    return " | ".join(str(cell) for cell in cells)


def _pad(text, width):
    return text.ljust(width)


def render_table(rows):
    return "\n".join(render_row(row) for row in rows)
