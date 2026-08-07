#!/usr/bin/env python3
"""Make the runtime's default matplotlib render Chinese correctly.

matplotlib defaults to DejaVu Sans, which has no CJK glyphs, so Chinese text
in plots renders as boxes (tofu) even though fonts-noto-cjk is installed in
the image. The claude-science code-execution sandbox sets MPLCONFIGDIR to a
fresh per-workspace cache and gives every spawn a tmpfs $HOME, so the only
matplotlib config that survives into the sandbox is the packaged
matplotlibrc inside the conda python env.

This script rewrites that file's default family lists in place (the shipped
values are commented-out samples that matplotlib de-comments when building
its defaults, so appending duplicate active lines would emit "Duplicate key"
warnings). Idempotent: safe to run at every container boot, and a no-op when
matplotlib is not installed yet (the conda env is provisioned by
claude-science on first boot; the entrypoint re-runs this on later boots).
"""

from __future__ import annotations

import os
import sys


# Keep DejaVu first in font.sans-serif for the classic matplotlib look; CJK
# glyphs fall back to Noto Sans CJK SC via font.family. serif/monospace lead
# with the Noto CJK faces so explicit family="serif"/"monospace" plots also
# render Chinese (JP variants cover Japanese-specific forms).
# font.family must be an explicit list: in this matplotlib build, per-glyph
# fallback only walks the font.family list, not the font.sans-serif/serif/
# monospace lists, so the latter alone still render Chinese as boxes.
# axes.unicode_minus must be off: the Unicode minus is absent from most CJK
# fonts and would render as a missing-glyph box on negative axis ticks.
MANAGED = {
    "font.family": (
        "sans-serif, Noto Sans CJK SC, serif, Noto Serif CJK SC, monospace, "
        "Noto Sans Mono CJK SC"
    ),
    "font.sans-serif": (
        "DejaVu Sans, Noto Sans CJK SC, Noto Sans CJK JP, Bitstream Vera Sans, "
        "Computer Modern Sans Serif, Lucida Grande, Verdana, Geneva, Lucid, "
        "Arial, Helvetica, Avant Garde, sans-serif"
    ),
    "font.serif": (
        "Noto Serif CJK SC, Noto Serif CJK JP, DejaVu Serif, "
        "Bitstream Vera Serif, Computer Modern Roman, New Century Schoolbook, "
        "Century Schoolbook L, Utopia, ITC Bookman, Bookman, "
        "Nimbus Roman No9 L, Times New Roman, Times, Palatino, Charter, serif"
    ),
    "font.monospace": (
        "Noto Sans Mono CJK SC, Noto Sans Mono CJK JP, DejaVu Sans Mono, "
        "Bitstream Vera Sans Mono, Computer Modern Typewriter, Courier New, "
        "Courier, Lucida Sans Typewriter, Lucida Typewriter, monospace"
    ),
    "axes.unicode_minus": "False",
}


def _is_clean(lines: list[str]) -> bool:
    """True when every managed key appears exactly once, active, with the
    exact expected value."""
    active: dict[str, str] = {}
    for raw in lines:
        stripped = raw.strip()
        if not stripped:
            continue
        if stripped.startswith("#"):
            candidate = stripped[1:].strip()
            if ":" in candidate and candidate.split(":", 1)[0].strip() in MANAGED:
                return False  # commented sample still present
            continue
        if ":" in stripped:
            key, _, value = stripped.partition(":")
            key = key.strip()
            if key in MANAGED:
                if key in active:
                    return False  # duplicate active line
                active[key] = value.strip()
    return set(active) == set(MANAGED) and all(
        active[key] == MANAGED[key] for key in MANAGED
    )


def _rebuild(lines: list[str]) -> list[str]:
    """Replace commented samples / stale active lines with one active line per
    managed key, positioned where the original sample used to be."""
    out: list[str] = []
    replaced: set[str] = set()
    for raw in lines:
        stripped = raw.strip()
        candidate = stripped
        if stripped.startswith("#"):
            candidate = stripped[1:].strip()
        key = None
        if ":" in candidate:
            key = candidate.split(":", 1)[0].strip()
        if key in MANAGED:
            if key not in replaced:
                out.append(f"{key}: {MANAGED[key]}\n")
                replaced.add(key)
            continue  # drop this sample/duplicate line
        out.append(raw)
    for key, value in MANAGED.items():
        if key not in replaced:
            out.append(f"{key}: {value}\n")
    return out


def main() -> int:
    try:
        import matplotlib
    except ImportError:
        print("[configure-cjk-fonts] matplotlib not installed; skipping", flush=True)
        return 0

    rc_path = os.path.join(matplotlib.get_data_path(), "matplotlibrc")
    try:
        with open(rc_path, encoding="utf-8") as fh:
            content = fh.read()
    except OSError as exc:
        print(f"[configure-cjk-fonts] cannot read {rc_path}: {exc}", flush=True)
        return 0

    lines = content.splitlines(keepends=True)
    if _is_clean(lines):
        print(f"[configure-cjk-fonts] already configured: {rc_path}", flush=True)
        return 0

    with open(rc_path, "w", encoding="utf-8") as fh:
        fh.writelines(_rebuild(lines))
    print(f"[configure-cjk-fonts] patched {rc_path}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
