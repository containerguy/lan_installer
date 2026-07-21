#!/usr/bin/env python3
"""Validate purpose-bound LANReady Ed25519 public keys before a client build."""

from __future__ import annotations

import argparse
import base64
import binascii


def decode_public_key(value: str, label: str) -> bytes:
    try:
        decoded = base64.b64decode(value.strip(), validate=True)
    except (ValueError, binascii.Error) as error:
        raise ValueError(f"{label} is not valid base64") from error
    if len(decoded) != 32:
        raise ValueError(f"{label} must contain exactly 32 bytes")
    return decoded


def validate(update_key: str, event_key: str) -> None:
    update = decode_public_key(update_key, "client update public key")
    event = decode_public_key(event_key, "event release public key")
    if update == event:
        raise ValueError("client update and event release public keys must be different")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("update_key")
    parser.add_argument("event_key")
    args = parser.parse_args()
    validate(args.update_key, args.event_key)


if __name__ == "__main__":
    main()
