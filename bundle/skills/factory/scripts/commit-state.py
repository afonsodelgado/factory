#!/usr/bin/env python3
import datetime
import fcntl
import json
import os
import pathlib
import sys
import tempfile


def commit(path, proposal, session):
    if not session:
        raise ValueError("supervisor session identity is required")
    with open(str(path) + ".lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        with open(path) as source:
            current = json.load(source)
        expected = proposal["expected_revision"]
        snapshot = proposal["snapshot"]
        identity = ("incarnation", "holder_generation")
        if any(current.get(key) != proposal.get(key) or snapshot.get(key) != current.get(key) for key in identity):
            raise ValueError("factory incarnation or holder changed")
        if current["revision"] == snapshot["revision"] and current.get("processed_events") == snapshot.get("processed_events"):
            return
        if current["revision"] != expected or snapshot["revision"] != expected + 1:
            raise ValueError("stale workflow proposal; reload state and reconcile")
        if snapshot["schema_version"] != 2:
            raise ValueError("unsupported state schema")
        current.update(snapshot)
        current["updated_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
        current["session_id"] = session
        descriptor, temporary = tempfile.mkstemp(prefix=".state-", dir=path.parent)
        try:
            with os.fdopen(descriptor, "w") as target:
                json.dump(current, target, indent=2)
                target.write("\n")
                target.flush()
                os.fsync(target.fileno())
            os.replace(temporary, path)
            directory = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)


if __name__ == "__main__":
    try:
        if len(sys.argv) != 4:
            raise ValueError("usage: commit-state.py <state.json> <proposal.json> <supervisor session>")
        with open(sys.argv[2]) as source:
            proposal = json.load(source)
        commit(pathlib.Path(sys.argv[1]), proposal, sys.argv[3])
    except (OSError, ValueError, KeyError) as error:
        sys.exit(str(error))
