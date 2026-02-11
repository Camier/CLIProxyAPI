#!/usr/bin/env bash

set -euo pipefail

BASE_REF="${1:-origin/main}"
SELF_SCRIPT_PATH="scripts/no_test_double_policy.sh"
SELF_WORKFLOW_PATH=".github/workflows/pr-no-test-double-policy.yml"
FORBIDDEN_PATTERN='\b(mock|mocks|mocked|mocking|fake|fakes|faker|monkey[[:space:]_-]*patch(es|ed|ing)?)\b'

if ! git rev-parse --verify "$BASE_REF" >/dev/null 2>&1; then
  echo "Policy check failed: base ref '$BASE_REF' not found."
  exit 2
fi

DIFF_TEXT="$(git diff --no-color --unified=0 "$BASE_REF"...HEAD -- . \
  ":(exclude)$SELF_SCRIPT_PATH" \
  ":(exclude)$SELF_WORKFLOW_PATH")"

if [[ -z "$DIFF_TEXT" ]]; then
  echo "Strict policy check passed (no diff to inspect)."
  exit 0
fi

VIOLATIONS="$(printf '%s\n' "$DIFF_TEXT" | grep -E '^\+[^+]' | grep -Eni "$FORBIDDEN_PATTERN" || true)"

if [[ -n "$VIOLATIONS" ]]; then
  echo "Strict policy violation: added lines contain disallowed terms."
  echo "Forbidden classes: mock / fake / monkey patch variants."
  echo "$VIOLATIONS"
  exit 1
fi

echo "Strict policy check passed."
