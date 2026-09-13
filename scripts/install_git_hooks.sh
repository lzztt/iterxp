#!/usr/bin/env bash
# Install the IterXP git hooks into the repository while preserving existing hooks.
# The managed hook sources live in scripts/hooks/. Existing active hooks with the
# same name are preserved by backing them up and chaining them.
set -euo pipefail

repo_dir="${1:-$(git rev-parse --show-toplevel 2>/dev/null || pwd)}"
hook_src_dir="$repo_dir/scripts/hooks"
hook_dir="$(git -C "$repo_dir" config --get core.hooksPath 2>/dev/null || true)"
if [ -z "$hook_dir" ]; then
  hook_dir="$repo_dir/.git/hooks"
  mkdir -p "$hook_dir"
else
  case "$hook_dir" in
    /*) ;;
    *) hook_dir="$repo_dir/$hook_dir" ;;
  esac
  mkdir -p "$hook_dir"
fi

installed=0
for src in "$hook_src_dir"/*; do
  name="$(basename "$src")"
  dst="$hook_dir/$name"
  if [ ! -f "$dst" ]; then
    cp "$src" "$dst"
    chmod 0755 "$dst"
    echo "install_git_hooks: installed $name"
    installed=1
    continue
  fi
  # Preserve an existing hook by chaining it, unless it already chains ours.
  if grep -qF "IterXP pre-push guard" "$dst" 2>/dev/null || cmp -s "$src" "$dst"; then
    continue
  fi
  backup="$dst.pre-iterxp.$(date +%Y%m%d%H%M%S)"
  mv "$dst" "$backup"
  cat > "$dst" <<CHAIN
#!/usr/bin/env bash
# Chained by IterXP install_git_hooks.sh. Original hook preserved at $backup.
if [ -x "$backup" ]; then
  "$backup" "\$@"
fi
exec "$hook_dir/$name.iterxp" "\$@"
CHAIN
  chmod 0755 "$dst"
  cp "$src" "$hook_dir/$name.iterxp"
  chmod 0755 "$hook_dir/$name.iterxp"
  echo "install_git_hooks: chained existing $name (backup $backup)"
  installed=1
done

# Re-point core.hooksPath only if we are installing into a managed dir.
if [ -n "$(git -C "$repo_dir" config --get core.hooksPath 2>/dev/null || true)" ]; then
  git -C "$repo_dir" config core.hooksPath "$hook_dir"
fi

if [ "$installed" -eq 1 ]; then
  echo "install_git_hooks: OK hooks_dir=$hook_dir"
else
  echo "install_git_hooks: hooks already up to date hooks_dir=$hook_dir"
fi
