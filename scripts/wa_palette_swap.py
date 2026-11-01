#!/usr/bin/env python3
"""Swap emerald Tailwind classes for the WhatsApp-typology tokens (wa-*).
Order matters: hover: variants first so they are not double-replaced."""
import pathlib

MAP = [
    ("hover:bg-emerald-500", "hover:bg-wa-bright"),
    ("bg-emerald-600", "bg-wa"),
    ("bg-emerald-500", "bg-wa-bright"),
    ("text-emerald-600", "text-wa-ink"),
    ("text-emerald-700", "text-wa-ink"),
    ("bg-emerald-50", "bg-wa-mist"),
    ("bg-emerald-100", "bg-wa-lime"),
    ("border-emerald-200", "border-wa-line"),
    ("border-emerald-300", "border-wa-line"),
]

FILES = [
    "src/app/page.tsx",
    "src/components/room/forms.tsx",
    "src/components/room/room-screen.tsx",
    "src/components/room/states.tsx",
    "src/components/room/controls.tsx",
    "src/components/room/settings-dialog.tsx",
]

root = pathlib.Path("/home/z/my-project")
for rel in FILES:
    p = root / rel
    text = p.read_text()
    before = text
    for old, new in MAP:
        text = text.replace(old, new)
    if text != before:
        p.write_text(text)
        n = sum(before.count(o) for o, _ in MAP)
        print(f"{rel}: {n} replacements")
    else:
        print(f"{rel}: no changes")
