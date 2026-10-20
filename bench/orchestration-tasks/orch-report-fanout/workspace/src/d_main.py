"""Entry point stubs for the fixture audit."""


def run(source):
    return source


def _cleanup(state):
    return None


if __name__ == "__main__":
    print(run("ok"))
