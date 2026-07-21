#!/usr/bin/env python3
"""Back up and restore the exact secret files resolved by Docker Compose."""

from __future__ import annotations

import argparse
import base64
import json
import os
import shutil
import stat
import subprocess
import tempfile
from pathlib import Path


SECRET_NAMES = ("web_admin_password", "webdav_master_key", "release_public_key", "event_release_public_key")
EVENT_PRIVATE_NAME = "event_release_private_key"
COMPOSE_COMMAND = ("docker", "compose", "-f", "compose.yaml", "-f", "compose.npm.yaml")


def compose_config() -> dict:
    completed = subprocess.run(
        (*COMPOSE_COMMAND, "config", "--format", "json"),
        check=True,
        stdout=subprocess.PIPE,
        text=True,
    )
    return json.loads(completed.stdout)


def configured_paths(config: dict) -> dict[str, Path]:
    configured = config.get("secrets", {})
    result: dict[str, Path] = {}
    for name in SECRET_NAMES:
        value = configured.get(name, {}).get("file")
        if not isinstance(value, str) or not value:
            raise ValueError(f"Compose secret {name!r} has no source file")
        result[name] = Path(value).expanduser().absolute()
    return result


def configured_secret_path(config: dict, name: str) -> Path:
    value = config.get("secrets", {}).get(name, {}).get("file")
    if not isinstance(value, str) or not value:
        raise ValueError(f"Compose secret {name!r} has no source file")
    return Path(value).expanduser().absolute()


def require_event_key_pair(public_path: Path, private_path: Path) -> None:
    require_regular_file(public_path, "event release public key")
    require_regular_file(private_path, "event release private key")
    try:
        public_key = base64.b64decode(public_path.read_bytes().strip(), validate=True)
        private_key = base64.b64decode(private_path.read_bytes().strip(), validate=True)
    except (ValueError, base64.binascii.Error) as error:
        raise ValueError("event release key pair is not valid base64") from error
    if len(public_key) != 32 or len(private_key) != 64 or private_key[32:] != public_key:
        raise ValueError("event release public and private keys do not match")


def require_distinct_release_keys(update_public_path: Path, event_public_path: Path) -> None:
    require_regular_file(update_public_path, "client update public key")
    require_regular_file(event_public_path, "event release public key")
    if update_public_path.read_bytes().strip() == event_public_path.read_bytes().strip():
        raise ValueError("client update and event release public keys must be different")


def require_regular_file(path: Path, label: str) -> None:
    metadata = path.lstat()
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        raise ValueError(f"{label} must be a regular file without symlinks: {path}")


def backup(config: dict, destination: Path) -> None:
    sources = configured_paths(config)
    for name, source in sources.items():
        require_regular_file(source, f"source for {name}")
    require_event_key_pair(sources["event_release_public_key"], configured_secret_path(config, EVENT_PRIVATE_NAME))
    require_distinct_release_keys(sources["release_public_key"], sources["event_release_public_key"])
    destination.mkdir(mode=0o700, parents=True, exist_ok=False)
    manifest: dict[str, str] = {}
    for name, source in sources.items():
        target = destination / f"{name}.secret"
        shutil.copyfile(source, target)
        os.chmod(target, 0o600)
        manifest[name] = str(source)
    manifest_path = destination / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(manifest_path, 0o600)


def atomic_restore(source: Path, target: Path) -> None:
    require_regular_file(source, "backup secret")
    target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    if target.exists() or target.is_symlink():
        require_regular_file(target, "restore target")
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{target.name}.", dir=target.parent)
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "wb") as output, source.open("rb") as input_file:
            shutil.copyfileobj(input_file, output)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, target)
    finally:
        temporary.unlink(missing_ok=True)


def restore(config: dict, source_directory: Path) -> None:
    manifest_path = source_directory / "manifest.json"
    require_regular_file(manifest_path, "secret manifest")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    current = configured_paths(config)
    # Validate the complete future pair before replacing any current secret.
    require_event_key_pair(source_directory / "event_release_public_key.secret", configured_secret_path(config, EVENT_PRIVATE_NAME))
    require_distinct_release_keys(source_directory / "release_public_key.secret", source_directory / "event_release_public_key.secret")
    for name in SECRET_NAMES:
        expected = manifest.get(name)
        if expected != str(current[name]):
            raise ValueError(
                f"configured restore path for {name} changed: backup={expected!r}, current={str(current[name])!r}"
            )
        require_regular_file(source_directory / f"{name}.secret", f"backup secret for {name}")
        if current[name].exists() or current[name].is_symlink():
            require_regular_file(current[name], f"restore target for {name}")
    for name, target in current.items():
        atomic_restore(source_directory / f"{name}.secret", target)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("backup", "restore"))
    parser.add_argument("directory", type=Path)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    config = compose_config()
    if args.action == "backup":
        backup(config, args.directory)
    else:
        restore(config, args.directory)


if __name__ == "__main__":
    main()
