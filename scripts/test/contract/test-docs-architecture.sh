#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${ROOT}"

overview=docs/总体设计.md

# Every go.work module is listed in the overview's code-organization table.
while IFS= read -r module_path; do
  [[ -n "${module_path}" ]] || continue
  grep -Fq "| \`${module_path}\` |" "${overview}" || {
    echo "${overview} missing go.work module: ${module_path}" >&2
    exit 1
  }
done < <(awk '/^use \(/ {on=1; next} on && /^\)/ {exit} on {gsub(/^[[:space:]]*\.\//, ""); if (length) print}' go.work)

# Every module design document is reachable from the overview and the site sidebar.
for doc in docs/模块/*.md; do
  name="$(basename "${doc}" .md)"
  grep -Fq "(模块/${name}.md)" "${overview}" || {
    echo "${overview} does not link ${doc}" >&2
    exit 1
  }
  grep -Fq "/模块/${name}'" docs/.vitepress/config.ts || {
    echo "docs/.vitepress/config.ts sidebar does not list ${doc}" >&2
    exit 1
  }
done

# Relative Markdown links inside docs/ and the READMEs resolve to existing files.
python3 - <<'PY'
import os, re, sys

roots = ["docs", "README.md", "modules", "packages", "web/README.md", "web-host/README.md"]
files = []
for root in roots:
    if os.path.isfile(root):
        files.append(root)
        continue
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in ("node_modules", ".vitepress", "public")]
        files += [os.path.join(dirpath, f) for f in filenames if f.endswith(".md")]

link = re.compile(r"\]\(([^)\s]+)\)")
broken = []
for path in files:
    with open(path, encoding="utf-8") as handle:
        text = re.sub(r"```.*?```", "", handle.read(), flags=re.S)
    for target in link.findall(text):
        if re.match(r"^[a-z]+:", target) or target.startswith("#"):
            continue
        target = target.split("#", 1)[0]
        if not target:
            continue
        resolved = os.path.normpath(os.path.join(os.path.dirname(path), target))
        if not os.path.exists(resolved):
            broken.append(f"{path} -> {target}")
if broken:
    print("broken documentation links:", *broken, sep="\n  ", file=sys.stderr)
    sys.exit(1)
PY

echo 'architecture documentation contract passed'
