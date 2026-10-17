"""Checks for the settle-apply fixture; run with: python3 verify.py"""

from calc import add, multiply, power

assert add(2, 3) == 5
assert multiply(3, 4) == 12
assert power(2, 5) == 32
print("verify: ok")
