#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
for file in docs/architecture.md docs/data-model.md docs/storage-strategy.md docs/acceptance.md; do
  test -s "$ROOT/$file"
done
test "$(grep -c '^```mermaid$' "$ROOT/docs/architecture.md")" -ge 2
grep -q '企业微信' "$ROOT/docs/architecture.md"
grep -q '风险清单' "$ROOT/docs/architecture.md"
test "$(grep -c '^| .* | .* | .* |$' "$ROOT/docs/architecture.md")" -ge 8
grep -q 'Projection Checkpoint' "$ROOT/docs/data-model.md"
grep -q 'Qdrant/Milvus' "$ROOT/docs/storage-strategy.md"
grep -q 'Stage 7 是本项目最后一个交付阶段' "$ROOT/docs/acceptance.md"
if grep -R -q 'Stage 8\|阶段 8' "$ROOT/docs"; then
  echo "error: final documentation must not defer work to Stage 8" >&2
  exit 1
fi
echo "Stage 7 documentation verification passed"
