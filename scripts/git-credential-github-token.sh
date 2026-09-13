#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" != "get" ]; then
  exit 0
fi

protocol=""
host=""
while IFS= read -r line; do
  case "$line" in
    protocol=*) protocol="${line#protocol=}" ;;
    host=*) host="${line#host=}" ;;
  esac
done

if [ "${protocol}" != "https" ]; then
  exit 0
fi

case "${host}" in
  github.com|*.github.com) ;;
  *) exit 0 ;;
esac

token_file="${ITERXP_GITHUB_TOKEN_FILE:-$HOME/token/github}"
if [ ! -r "$token_file" ]; then
  echo "git-credential-github-token: token file not readable: $token_file" >&2
  exit 1
fi

token="$(tr -d '[:space:]' < "$token_file")"
printf 'username=x-access-token\npassword=%s\n' "$token"
exit 0
