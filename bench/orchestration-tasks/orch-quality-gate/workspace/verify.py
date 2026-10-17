"""Checks for the quality-gate fixture; run with: python3 verify.py"""

from mathx import average

assert average([2, 4]) == 3
assert average([5]) == 5
try:
    average([])
except ValueError:
    pass
else:
    raise AssertionError("empty input must raise ValueError")
print("verify: ok")
