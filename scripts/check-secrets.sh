#!/usr/bin/env bash
# Pre-commit guard: blocks obvious secrets, real-looking home paths and raw
# session transcripts in staged changes. Complements gitleaks (run in CI).
set -euo pipefail
fail=0
staged=$(git diff --cached --name-only --diff-filter=ACM)
[ -z "$staged" ] && exit 0

# 1. forbidden file names
while IFS= read -r f; do
  case "$f" in
    *.pem|*id_rsa*|.env|.env.*|*credentials*|*.db|*.sqlite*|*.zip)
      echo "blocked file: $f"; fail=1 ;;
  esac
done <<<"$staged"

# Added lines only. Captured into a variable: piping into `grep -q` under
# pipefail would fail open (SIGPIPE makes the pipeline "fail", skipping the check).
added=$(git diff --cached -U0 -- . ':(exclude)scripts/check-secrets.sh' ':(exclude)internal/redact/*_test.go' | grep -E '^\+' | grep -vE '^\+\+\+' || true)

# 2. secret-shaped strings in added lines (testdata is scanned too: fixtures must stay fake-by-construction)
patterns='AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{40,}|sk-[A-Za-z0-9]{32,}|sk-ant-[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|AIza[0-9A-Za-z_-]{35}|ggb_(read|ingest|admin)_[A-Za-z0-9_-]{20,}'
if grep -E -q "$patterns" <<<"$added"; then
  echo "secret-shaped string in staged diff"; fail=1
fi

# 3. real home paths of the current user
if [ -n "${HOME:-}" ] && grep -F -q "$HOME" <<<"$added"; then
  echo "staged diff contains the real home directory path"; fail=1
fi
if [ -n "${USER:-}" ] && grep -E -q "/(home|Users)/$USER\b" <<<"$added"; then
  echo "staged diff contains a real user path"; fail=1
fi

# 4. gitleaks if available
if command -v gitleaks >/dev/null 2>&1; then
  cfg=()
  [ -f .gitleaks.toml ] && cfg=(--config .gitleaks.toml)
  gitleaks protect --staged --redact "${cfg[@]}" >/dev/null 2>&1 || { echo "gitleaks flagged staged changes (or failed to run)"; fail=1; }
fi
exit $fail
