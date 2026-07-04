#!/usr/bin/env python3
import os

path = "/opt/api-bridge/scripts/install-safe.sh"
with open(path) as f:
    lines = f.readlines()

cut = next(i for i, l in enumerate(lines) if "install_macos_service()" in l)

with open(path, "w") as f:
    f.writelines(lines[:cut] + ["exit 0\n"])
