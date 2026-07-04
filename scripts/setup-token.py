#!/usr/bin/env python3
"""Create a fake OAuth token for Claude Science BYOK."""

import base64
import json
import os
import re
import sys

from cryptography.fernet import Fernet

TOKEN_DIR = os.path.expanduser("~/.claude-science/.oauth-tokens")
ENC_KEY_FILE = os.path.expanduser("~/.claude-science/encryption.key")
CLAUDE_AI_SCOPES = "user:inference user:file_upload user:profile user:mcp_servers user:plugins"


def read_oauth_key():
    with open(ENC_KEY_FILE) as f:
        for line in f:
            if line.startswith("OAUTH_ENCRYPTION_KEY="):
                return line.strip().split("=", 1)[1]
    raise ValueError("OAUTH_ENCRYPTION_KEY not found in encryption.key")


def sanitize_user_id(uid: str) -> str:
    return re.sub(r"[^a-zA-Z0-9_-]", "", uid)


def encrypt_fernet(key: str, plaintext: str) -> str:
    f = Fernet(key.encode())
    return f.encrypt(plaintext.encode()).decode()


def main():
    account_uuid = "byok-user-000000000000000000"
    org_uuid = "org_byok_000000000000"
    fake_access_token = "fake-bearer-token-for-proxy"

    token_data = {
        "access_token": fake_access_token,
        "refresh_token": None,
        "api_key": None,
        "token_expires_at": "2099-12-31T23:59:59Z",
        "provider": "claude_ai",
        "scopes": CLAUDE_AI_SCOPES,
        "email": "byok@localhost",
        "account_uuid": account_uuid,
        "subscription_type": "max",
        "rate_limit_tier": "tier_5",
        "seat_tier": "enterprise_usage_based",
        "org_uuid": org_uuid,
        "organization": {
            "uuid": org_uuid,
            "name": "BYOK Organization",
            "organization_type": "claude_max",
            "rate_limit_tier": "tier_5",
            "seat_tier": "enterprise_usage_based",
            "billing_type": "api",
            "has_extra_usage_enabled": True,
            "claude_ai_completion_feedback_enabled": False,
        },
        "billing_type": "api",
        "has_extra_usage_enabled": True,
    }

    oauth_key = read_oauth_key()
    plaintext = json.dumps(token_data)
    try:
        encrypted = encrypt_fernet(oauth_key, plaintext)
    except Exception as e:
        print(f"Encryption failed: {e}")
        sys.exit(1)

    os.makedirs(TOKEN_DIR, mode=0o700, exist_ok=True)
    safe_id = sanitize_user_id(account_uuid)
    token_path = os.path.join(TOKEN_DIR, f"{safe_id}.enc")

    with open(token_path, "w") as f:
        f.write(encrypted)
    os.chmod(token_path, 0o600)

    print(f"OAuth token written to: {token_path}")


if __name__ == "__main__":
    main()
