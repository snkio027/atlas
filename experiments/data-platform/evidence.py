"""One fresh attempt. Existing paths never grant execution rights again."""
import json
import os
from pathlib import Path
import re


class Journal:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.directory.mkdir(mode=0o700, exist_ok=False)
        # Persist the directory entry too: losing the attempt directory after a
        # crash would lose all claims and could make a replay look like first use.
        fd = os.open(self.directory.parent, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def _write(self, name, value):
        if not re.fullmatch(r"[a-z][a-z0-9-]+", name):
            raise ValueError("invalid evidence identity")
        path = self.directory / (name + ".json")
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as stream:
            stream.write((json.dumps(value, sort_keys=True, indent=2) + "\n").encode())
            stream.flush()
            os.fsync(stream.fileno())
        fd = os.open(self.directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def claim(self, name, value):
        self._write(name + "-intent", value)

    def record(self, name, value):
        self._write(name, value)
