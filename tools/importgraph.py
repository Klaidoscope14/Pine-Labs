"""Print the repository's internal Python import graph as JSON.

Usage: python importgraph.py <repo-root> <source-dir> [<source-dir> ...]
Runs inside the sandbox (no network); reads files, writes JSON to stdout.
"""

import ast
import json
import sys
from pathlib import Path


def module_name(root: Path, path: Path) -> str:
    rel = path.relative_to(root).with_suffix("")
    parts = list(rel.parts)
    if parts[-1] == "__init__":
        parts = parts[:-1]
    return ".".join(parts)


def main() -> int:
    root = Path(sys.argv[1]).resolve()
    graph: dict[str, list[str]] = {}
    for src in sys.argv[2:]:
        for path in sorted((root / src).rglob("*.py")):
            mod = module_name(root, path)
            try:
                tree = ast.parse(path.read_text(encoding="utf-8"))
            except (SyntaxError, UnicodeDecodeError):
                graph[mod] = ["<unparseable>"]
                continue
            deps: set[str] = set()
            for node in ast.walk(tree):
                if isinstance(node, ast.Import):
                    deps.update(a.name for a in node.names)
                elif isinstance(node, ast.ImportFrom) and node.module and node.level == 0:
                    deps.add(node.module)
            prefixes = tuple(s.strip("/").replace("/", ".") for s in sys.argv[2:])
            graph[mod] = sorted(d for d in deps if d.startswith(prefixes))
    json.dump(graph, sys.stdout, indent=1, sort_keys=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
