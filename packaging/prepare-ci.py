#!/usr/bin/env python3
"""Maintainer-only pinned CI prerequisites. Never included in user packages."""
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys
import tarfile
import urllib.request

def prepare_extra_tool(tool, destination):
    archive_path = destination / (tool["name"] + ".download")
    if not archive_path.exists():
        with urllib.request.urlopen(tool["url"], timeout=120) as response:
            data = response.read(256 * 1024 * 1024)
    else:
        data = archive_path.read_bytes()
    if hashlib.sha256(data).hexdigest() != tool["sha256"]:
        raise SystemExit("Artifact digest mismatch: " + tool["name"])
    archive_path.write_bytes(data)
    if tool["name"] == "lua":
        source = destination / "lua-source"
        source.mkdir(exist_ok=True)
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
            for member in archive:
                name = PurePosixPath(member.name)
                if name.is_absolute() or ".." in name.parts or not (member.isfile() or member.isdir()):
                    raise SystemExit("Unsafe Lua source archive")
                target = source / str(name)
                if member.isdir():
                    target.mkdir(parents=True, exist_ok=True)
                else:
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(archive.extractfile(member).read())
        build = source / ("lua-" + tool["version"])
        subprocess.run(["make", "macosx"], cwd=build, check=True)
        shutil.copyfile(build / "src/lua", destination / "lua")
    else:
        binary = data
        if tool.get("member"):
            with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
                matches = [m for m in archive if m.name == tool["member"]]
                if len(matches) != 1 or not matches[0].isfile():
                    raise SystemExit("Missing/unsafe tool member")
                binary = archive.extractfile(matches[0]).read()
        if "binarySHA256" in tool and hashlib.sha256(binary).hexdigest() != tool["binarySHA256"]:
            raise SystemExit("Executable digest mismatch")
        (destination / tool["name"]).write_bytes(binary)
    os.chmod(destination / tool["name"], 0o700)
    print("Prepared " + tool["name"] + " " + tool["version"], flush=True)



def main():
    root = Path(__file__).resolve().parent.parent
    args = sys.argv[1:]
    fast = len(args) == 2 and args[0] == "--fast"
    if not (len(args) == 1 or fast):
        raise SystemExit("usage: prepare-ci.py [--fast] DESTINATION")
    destination = Path(args[-1]).resolve()
    locks = json.loads((root / "packaging/ci-tools.json").read_text())
    if fast:
        destination.mkdir(parents=True, exist_ok=True, mode=0o700)
        for name in ["task", "actionlint"]:
            prepare_extra_tool(next(t for t in locks if t["name"] == name), destination)
        return
    destination.mkdir(parents=True, exist_ok=True, mode=0o700)
    runtime_locks = json.loads((root / "packaging/tools-darwin-arm64.json").read_text())
    cache = destination / "runtime-cache"
    cache.mkdir(parents=True, exist_ok=True, mode=0o700)
    cache.chmod(0o700)
    (cache / "downloads").mkdir(parents=True, exist_ok=True, mode=0o700)
    # Reuse only previously checksum-verified archives, never arbitrary PATH tools.
    for tool in runtime_locks:
        old = destination / (tool["name"] + ".download")
        if old.exists() and hashlib.sha256(old.read_bytes()).hexdigest() == tool["sha256"]:
            cached = cache / "downloads" / (tool["sha256"] + ".part")
            cached.write_bytes(old.read_bytes())
            cached.chmod(0o600)
    prepared = subprocess.check_output([
        "go", "run", "./cmd/atlas-release", "--prepare-tools", "--output", str(cache)
    ], cwd=root, text=True).strip()
    for tool in runtime_locks:
        binary = Path(prepared) / tool["name"]
        shutil.copyfile(binary, destination / tool["name"])
        os.chmod(destination / tool["name"], 0o700)
        print("Prepared " + tool["name"] + " " + tool["version"], flush=True)

    for tool in locks:
        prepare_extra_tool(tool, destination)

    # Historical render fixtures use their own immutable toolchain, never the new
    # product tools. These executables are not shipped in installation packages.
    frozen_commit = "697ebf04e9362abcce9a4b8db005ee747493417d"
    frozen_locks = []
    for path in ("packaging/tools-darwin-arm64.json", "packaging/ci-tools.json"):
        frozen_locks.extend(json.loads(subprocess.check_output(
            ["git", "show", frozen_commit + ":" + path], cwd=root, text=True)))
    historical = destination / "ot1"
    historical.mkdir(parents=True, exist_ok=True, mode=0o700)
    for name in ("helm", "kubectl", "yq"):
        tool = next(t for t in frozen_locks if t["name"] == name)
        prepare_extra_tool(tool, historical)


if __name__ == "__main__":
    main()
