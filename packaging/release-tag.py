"""Classify a reviewed release tag before spending CI build authority."""
import re
import sys


def classify(tag):
    core = r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    if re.fullmatch(core, tag):
        return "stable"
    if re.fullmatch(core + r"-(rc|d1)\.[1-9][0-9]*", tag):
        return "prerelease"
    raise ValueError("use vMAJOR.MINOR.PATCH or a numbered -rc.N / -d1.N candidate")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("one release tag required")
    try:
        print("channel=" + classify(sys.argv[1]))
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc
