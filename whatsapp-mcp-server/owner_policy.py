"""Owner-only, fail-closed policy shared with the Go bridge. No network or secrets."""

import argparse
import fcntl
import json
import os
import re
import stat
import sys
import tempfile
from pathlib import Path

CAPABILITIES = {
    "read_messages": "Expose approved cached group text to MCP",
    "cache_messages": "Cache incoming text for approved groups",
    "send_messages": "Send messages (unsupported in this read-only profile)",
    "media": "Download/upload media (unsupported)",
    "webhooks": "Forward incoming content (unsupported)",
    "history_requests": "Request older history from the phone (unsupported)",
    "read_receipts": "Send read receipts (unsupported)",
    "call_capture": "Store call metadata (unsupported)",
}
SUPPORTED = {"read_messages", "cache_messages"}
GROUP = re.compile(r"[0-9]+(?:-[0-9]+)?@g\.us", re.ASCII)
MAX_POLICY_BYTES = 65536


class PolicyError(ValueError):
    pass


def validate(policy):
    if not isinstance(policy, dict) or set(policy) != {"version", "access_enabled", "allowed_groups", "capabilities"}:
        raise PolicyError("Policy must contain exactly version, access_enabled, allowed_groups and capabilities")
    if type(policy["version"]) is not int or policy["version"] != 1 or type(policy["access_enabled"]) is not bool:
        raise PolicyError("Policy version must be 1 and access_enabled must be Boolean")
    groups = policy["allowed_groups"]
    if (
        not isinstance(groups, list)
        or len(groups) > 100
        or any(not isinstance(g, str) or not GROUP.fullmatch(g) for g in groups)
    ):
        raise PolicyError("Use at most 100 exact numeric group JIDs ending in @g.us")
    if len(groups) != len(set(groups)):
        raise PolicyError("Duplicate group JID")
    caps = policy["capabilities"]
    if not isinstance(caps, dict) or set(caps) != set(CAPABILITIES) or any(type(v) is not bool for v in caps.values()):
        raise PolicyError("Every documented capability must be Boolean")
    if any(caps[k] for k in caps.keys() - SUPPORTED):
        raise PolicyError("This profile cannot enable sending, media, webhooks, history requests, receipts or calls")
    if policy["access_enabled"] and not groups:
        raise PolicyError("Enabled access requires at least one exact group JID")
    return policy


def _pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise PolicyError("Duplicate JSON key")
        result[key] = value
    return result


def private_directory(path):
    path = Path(path).absolute()
    # Refuse symlinks in every component; do not alter unrelated ancestors.
    for component in [*reversed(path.parents), path]:
        if component.is_symlink():
            raise PolicyError("Policy directory must not contain symlinks")
    info = path.stat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise PolicyError("Policy directory must be owned by the current user with mode 0700")


def load(path=None):
    path = Path(path or os.getenv("WHATSAPP_POLICY_FILE", ""))
    if not path.is_absolute():
        raise PolicyError("WHATSAPP_POLICY_FILE must name an absolute private policy path")
    private_directory(path.parent)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise PolicyError("Policy must be a current-user regular file with mode 0600")
        with os.fdopen(fd, "rb", closefd=False) as stream:
            data = stream.read(MAX_POLICY_BYTES + 1)
        if len(data) > MAX_POLICY_BYTES:
            raise PolicyError("Policy too large")
        return validate(json.loads(data, object_pairs_hook=_pairs))
    finally:
        os.close(fd)


def allowed(policy, capability):
    return tuple(policy["allowed_groups"]) if policy["access_enabled"] and policy["capabilities"][capability] else ()


def atomic_write(path, policy):
    validate(policy)
    path = Path(path)
    if not path.is_absolute():
        raise PolicyError("Policy path must be absolute")
    private_directory(path.parent)
    if path.is_symlink():
        raise PolicyError("Policy cannot be a symlink")
    fd, temporary = tempfile.mkstemp(prefix=".policy-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(policy, stream, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def initial():
    return {
        "version": 1,
        "access_enabled": False,
        "allowed_groups": [],
        "capabilities": {k: k in SUPPORTED for k in CAPABILITIES},
    }


def cli(argv=None):
    parser = argparse.ArgumentParser(
        description="Manage local group permissions; never reads sessions, messages or credentials"
    )
    parser.add_argument("--policy", required=True, type=Path)
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument(
        "--confirm", action="store_true", help="Confirm a permission expansion after inspecting the proposed policy"
    )
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("init", "show", "status", "validate", "revoke", "enable", "rollback"):
        commands.add_parser(name)
    groups = commands.add_parser("groups", help="Replace exact allowed group JIDs; empty revokes all groups")
    groups.add_argument("jids", nargs="*")
    capability = commands.add_parser(
        "capability", help="Edit capability flags; unsafe capabilities are intentionally unsupported"
    )
    capability.add_argument("name", choices=CAPABILITIES)
    capability.add_argument("value", choices=("on", "off"))
    args = parser.parse_args(argv)
    lock = None
    try:
        private_directory(args.policy.parent)
        lock_fd = os.open(
            args.policy.with_name(args.policy.name + ".lock"), os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600
        )
        lock = os.fdopen(lock_fd, "rb")
        info = os.fstat(lock.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
            raise PolicyError("Policy lock must be an owner-only regular file")
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
        current = initial() if args.command == "init" else load(args.policy)
        if args.command in {"show", "status", "validate"}:
            print(json.dumps(current, indent=2))
            if args.command == "status":
                print("Policy status only; no connection or process inspection. Reloaded on each request/event.")
                print(json.dumps(CAPABILITIES, indent=2))
            return 0
        updated = json.loads(json.dumps(current))
        if args.command == "init" and args.policy.exists():
            raise PolicyError("Policy already exists; use show, revoke or rollback")
        if args.command == "revoke":
            updated["access_enabled"] = False
            updated["allowed_groups"] = []
        elif args.command == "enable":
            updated["access_enabled"] = True
        elif args.command == "groups":
            updated["allowed_groups"] = args.jids
            if not args.jids:
                updated["access_enabled"] = False
        elif args.command == "capability":
            updated["capabilities"][args.name] = args.value == "on"
        elif args.command == "rollback":
            updated = load(args.policy.with_name(args.policy.name + ".previous"))
        validate(updated)
        expansion = (
            (not current["access_enabled"] and updated["access_enabled"])
            or bool(set(updated["allowed_groups"]) - set(current["allowed_groups"]))
            or any(updated["capabilities"][k] and not current["capabilities"][k] for k in CAPABILITIES)
        )
        print(json.dumps(updated, indent=2))
        if expansion and not (args.confirm or args.dry_run):
            raise PolicyError("Permission expansion requires --confirm; inspect with --dry-run first")
        if not args.dry_run:
            if args.command != "init":
                atomic_write(args.policy.with_name(args.policy.name + ".previous"), current)
            atomic_write(args.policy, updated)
        return 0
    except (PolicyError, OSError, json.JSONDecodeError) as exc:
        print(f"Policy rejected ({type(exc).__name__}): {exc}", file=sys.stderr)
        return 2
    finally:
        if lock is not None:
            lock.close()


if __name__ == "__main__":
    raise SystemExit(cli())
