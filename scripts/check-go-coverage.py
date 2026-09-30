#!/usr/bin/env python3
"""Merge workspace coverage, excluding generated Go files, and require >60%."""

import re
import sys
from pathlib import Path


def summarize(root, manifest, output):
    blocks = {}
    generated = {}
    for entry in manifest.read_text().splitlines():
        module, profile = entry.split("\t")
        module_dir = root / module
        module_path = re.search(
            r"^module\s+(\S+)", (module_dir / "go.mod").read_text(), re.MULTILINE
        ).group(1).strip('"')
        lines = Path(profile).read_text().splitlines()
        if not lines or lines[0] != "mode: set":
            raise ValueError(f"invalid coverage profile: {profile}")
        for line in lines[1:]:
            location, statements, count = line.rsplit(None, 2)
            filename = location.rsplit(":", 1)[0]
            if not filename.startswith(module_path + "/"):
                raise ValueError(f"coverage file outside module: {filename}")
            if filename not in generated:
                source = module_dir / filename[len(module_path) + 1:]
                with source.open() as stream:
                    generated[filename] = False
                    for source_line in stream:
                        if re.fullmatch(r"// Code generated .* DO NOT EDIT\.\s*", source_line):
                            generated[filename] = True
                            break
                        if source_line.startswith("package "):
                            break
            if generated[filename]:
                continue
            statements, count = int(statements), int(count)
            if location in blocks:
                old_statements, old_count = blocks[location]
                if statements != old_statements:
                    raise ValueError(f"inconsistent coverage block: {location}")
                count = max(count, old_count)
            blocks[location] = (statements, count)

    output.write_text("mode: set\n" + "".join(
        f"{location} {statements} {count}\n"
        for location, (statements, count) in sorted(blocks.items())
    ))
    total = sum(statements for statements, _ in blocks.values())
    covered = sum(statements for statements, count in blocks.values() if count > 0)
    return covered, total


def main():
    root, manifest, output = map(Path, sys.argv[1:])
    covered, total = summarize(root, manifest, output)
    if not total:
        print("Go coverage failed: no non-generated statements found", file=sys.stderr)
        return 1
    print(f"Go coverage: {100 * covered / total:.4f}% ({covered}/{total} statements)")
    print(f"Coverage report: {output}")
    # Compare integers so rounded percentages never affect the gate.
    if covered * 100 <= total * 60:
        print("Go coverage failed: overall coverage must be greater than 60%", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
