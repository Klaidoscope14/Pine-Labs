#!/usr/bin/env bash
# Creates the local "code host" repo from the sample sources, at the path the
# config's code_host endpoint points to. Idempotent: does nothing if it exists.
# Builds the first commit directly into a bare repo (no temporary checkout).
set -euo pipefail
config="$1"
src="$(cd "$2" && pwd)"
remote="$(bin/aegis-config repo-remote "$config")"
if [[ -d "$remote" ]]; then
  echo "code host repo already exists: $remote"
  exit 0
fi
mkdir -p "$(dirname "$remote")"
git init -q --bare -b main "$remote"
printf '__pycache__/\n.venv/\n.pytest_cache/\n*.pyc\n' >> "$remote/info/exclude"
export GIT_DIR="$remote" GIT_WORK_TREE="$src" GIT_INDEX_FILE="$remote/seed-index"
export GIT_AUTHOR_NAME=aegis-seed GIT_AUTHOR_EMAIL=aegis-seed@aegis.invalid
export GIT_COMMITTER_NAME=aegis-seed GIT_COMMITTER_EMAIL=aegis-seed@aegis.invalid
git add -A
tree="$(git write-tree)"
commit="$(git commit-tree "$tree" -m "Seed pine-settlements-svc")"
git update-ref refs/heads/main "$commit"
echo "seeded $remote at $commit"
