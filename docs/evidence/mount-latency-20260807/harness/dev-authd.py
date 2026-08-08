#!/usr/bin/env python3
"""Minimal RS256 token issuer + JWKS endpoint for the latency measurement run.

The relayfile server verifies bearer tokens as RS256 against a JWKS document
(`internal/httpapi/auth.go:203-215`, `:262-276`). Rather than boot the full
relayauth monorepo for a measurement run, this issues an ephemeral keypair and
serves the matching JWKS, so the whole auth dependency is one self-contained,
reproducible file.

The private key is written OUTSIDE the repository (a scratch directory passed
in by the caller) and is a throwaway credential scoped to this run only. It is
never committed and never leaves the two hosts.

Usage:
    dev-authd.py serve KEY_DIR BIND_HOST PORT
    dev-authd.py mint  KEY_DIR WORKSPACE_ID AGENT_NAME TTL_SECONDS
"""

import base64
import hashlib
import http.server
import json
import os
import sys
import time

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa

# Scopes the mount daemon needs, mirroring scripts/generate-dev-token.sh.
SCOPES = [
    "fs:read",
    "fs:write",
    "sync:read",
    "sync:trigger",
    "ops:read",
    "ops:replay",
    "admin:read",
    "admin:replay",
]


def b64url(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def load_or_create_key(key_dir):
    """Return (private_key, kid), generating a fresh keypair on first use."""
    os.makedirs(key_dir, mode=0o700, exist_ok=True)
    key_path = os.path.join(key_dir, "latency-run-key.pem")
    if os.path.exists(key_path):
        with open(key_path, "rb") as handle:
            private_key = serialization.load_pem_private_key(handle.read(), password=None)
    else:
        private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        pem = private_key.private_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PrivateFormat.PKCS8,
            encryption_algorithm=serialization.NoEncryption(),
        )
        # 0600 before any bytes land on disk.
        descriptor = os.open(key_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(pem)

    numbers = private_key.public_key().public_numbers()
    modulus = numbers.n.to_bytes((numbers.n.bit_length() + 7) // 8, "big")
    kid = hashlib.sha256(modulus).hexdigest()[:16]
    return private_key, kid


def jwks_document(private_key, kid):
    numbers = private_key.public_key().public_numbers()
    modulus = numbers.n.to_bytes((numbers.n.bit_length() + 7) // 8, "big")
    exponent = numbers.e.to_bytes((numbers.e.bit_length() + 7) // 8, "big")
    return {
        "keys": [
            {
                "kid": kid,
                "kty": "RSA",
                "alg": "RS256",
                "use": "sig",
                "n": b64url(modulus),
                "e": b64url(exponent),
            }
        ]
    }


def mint(key_dir, workspace_id, agent_name, ttl_seconds):
    private_key, kid = load_or_create_key(key_dir)
    header = {"alg": "RS256", "typ": "JWT", "kid": kid}
    now = int(time.time())
    payload = {
        "wks": workspace_id,
        "workspace_id": workspace_id,
        "sub": agent_name,
        "agent_name": agent_name,
        "aud": ["relayfile"],
        "iat": now,
        "exp": now + int(ttl_seconds),
        "scopes": SCOPES,
    }
    signing_input = (
        b64url(json.dumps(header, separators=(",", ":")).encode())
        + "."
        + b64url(json.dumps(payload, separators=(",", ":")).encode())
    )
    signature = private_key.sign(
        signing_input.encode(), padding.PKCS1v15(), hashes.SHA256()
    )
    print(signing_input + "." + b64url(signature))


def serve(key_dir, bind_host, port):
    private_key, kid = load_or_create_key(key_dir)
    document = json.dumps(jwks_document(private_key, kid)).encode()

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.startswith("/.well-known/jwks.json"):
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(document)))
                self.end_headers()
                self.wfile.write(document)
            else:
                self.send_response(404)
                self.end_headers()

        def log_message(self, *args):
            pass  # keep the trial logs clean

    sys.stderr.write(f"dev-authd JWKS on {bind_host}:{port} kid={kid}\n")
    sys.stderr.flush()
    http.server.HTTPServer((bind_host, port), Handler).serve_forever()


if __name__ == "__main__":
    if len(sys.argv) == 5 and sys.argv[1] == "serve":
        serve(sys.argv[2], sys.argv[3], int(sys.argv[4]))
    elif len(sys.argv) == 6 and sys.argv[1] == "mint":
        mint(sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5])
    else:
        sys.stderr.write(__doc__)
        sys.exit(2)
