#!/usr/bin/env python3
"""Resolve LANReady data/cache bind sources from the effective Compose config."""

from __future__ import annotations

import argparse
import json
import subprocess
from pathlib import Path


COMPOSE_COMMAND = ("docker", "compose", "-f", "compose.yaml", "-f", "compose.npm.yaml")


def compose_config() -> dict:
    completed = subprocess.run(
        (*COMPOSE_COMMAND, "config", "--format", "json"),
        check=True,
        stdout=subprocess.PIPE,
        text=True,
    )
    return json.loads(completed.stdout)


def bind_source(config: dict, target: str) -> Path:
    volumes = config.get("services", {}).get("server", {}).get("volumes", [])
    matches = [volume for volume in volumes if volume.get("target") == target]
    if len(matches) != 1 or matches[0].get("type") != "bind":
        raise ValueError(f"server target {target} must have exactly one bind mount")
    source_value = matches[0].get("source")
    if not isinstance(source_value, str) or not source_value:
        raise ValueError(f"server target {target} has no bind source")
    source = Path(source_value)
    if not source.is_absolute():
        raise ValueError(f"Compose did not resolve {target} to an absolute source")
    source = source.resolve(strict=True)
    if not source.is_dir():
        raise ValueError(f"bind source for {target} is not a directory: {source}")
    return source


def cache_mode(config: dict) -> str:
    data = bind_source(config, "/data")
    cache = bind_source(config, "/cache")
    return "inside-data" if cache == data or cache.is_relative_to(data) else "external"


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("value", choices=("cache", "data", "cache-mode"))
    args = parser.parse_args()
    config = compose_config()
    if args.value == "cache":
        print(bind_source(config, "/cache"))
    elif args.value == "data":
        print(bind_source(config, "/data"))
    else:
        print(cache_mode(config))


if __name__ == "__main__":
    main()
