#!/usr/bin/env bash
# Test a claude-hub file without starting Claude and without printing any token.
#   tools/claude-hub/test.sh [file]        (default: the template next to this script)
# Checks: syntax; the template has no token; each profile, an unknown profile and a bad <name>_KEY
# resolve as expected against a stand-in `claude`; `claude-hub statusline` prints a line.
set -u
DIR=$(cd "$(dirname "$0")" && pwd); F="${1:-$DIR/claude-hub}"; fail=0
ok() { printf 'ok    %s\n' "$1"; }; bad() { printf 'FAIL  %s\n' "$1"; fail=1; }
bash -n "$F" && ok "syntax" || bad "syntax"
if [ "$F" = "$DIR/claude-hub" ]; then
  if grep -q 'sk-ant' "$F" || grep -Eq '^KEY_[A-Za-z0-9_]+="[^"]+"' "$F"; then bad "template holds a token"; else ok "template is token-free"; fi
fi
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; mkdir "$T/bin"
cat > "$T/bin/claude" <<'FAKE'
#!/bin/sh
t="${CLAUDE_CODE_OAUTH_TOKEN-}"; [ -n "$t" ] && t="set(len ${#t})" || t="unset"
echo "dir=${CLAUDE_CONFIG_DIR-unset} token=$t profile=${CLAUDE_HUB_PROFILE-unset} args=$*"
FAKE
chmod +x "$T/bin/claude"
run() { (cd "$T" && env -u CLAUDE_CODE_OAUTH_TOKEN -u CLAUDE_CONFIG_DIR -u CLAUDE_HUB_PROFILE \
          PATH="$T/bin:/usr/bin:/bin" HOME="$HOME" bash "$F" "$@" 2>/dev/null); }
profiles=$(sed -n 's/^PROFILES="\(.*\)"/\1/p' "$F")
for p in $profiles; do
  out=$(run "$p" --x)
  case "$out" in *"profile=$p "*"args=--x") ok "profile $p -> ${out%% profile=*}";; *) bad "profile $p -> $out";; esac
done
first=${profiles%% *}
def=$(sed -n 's/^DEFAULT_PROFILE="\(.*\)"/\1/p' "$F")
out=$(run mcp list)
case "$out" in *"args=mcp list") ok "subcommand 'mcp list' passes through (profile ${def:-none})";; *) bad "subcommand -> $out";; esac
out=$(run "fix the bug")
case "$out" in *"args=fix the bug") ok "prompt as first argument passes through";; *) bad "prompt -> $out";; esac
echo nosuchprofile > "$T/.claude-profile"; out=$(run --x); rm -f "$T/.claude-profile"
case "$out" in *"profile=unset"*"args=--x") ok "unknown .claude-profile -> plain claude";; *) bad "unknown .claude-profile -> $out";; esac
# Inside a running session: keep its profile, dir and token; an explicit profile still switches.
second=$(echo "$profiles" | awk '{print $2}')
nest() { (cd "$T" && env CLAUDE_HUB_PROFILE="$second" CLAUDE_CONFIG_DIR=/nested/dir CLAUDE_CODE_OAUTH_TOKEN=NESTED \
          PATH="$T/bin:/usr/bin:/bin" HOME="$HOME" bash "$F" "$@" 2>/dev/null); }
out=$(nest -p hi)
case "$out" in "dir=/nested/dir token=set(len 6) profile=$second args=-p hi") ok "nested call keeps the session's profile ($second)";; *) bad "nested -> $out";; esac
skey=$(sed -n "s/^${second}_KEY=\"\(.*\)\"/\1/p" "$F")
sed "s/^${skey}=.*/${skey}=\"T0KEN-OF-$second\"/" "$F" > "$T/withkey"; chmod +x "$T/withkey"
out=$(cd "$T" && env -u CLAUDE_CODE_OAUTH_TOKEN CLAUDE_HUB_PROFILE="$second" CLAUDE_CONFIG_DIR=/nested/dir \
      PATH="$T/bin:/usr/bin:/bin" HOME="$HOME" bash "$T/withkey" -p hi 2>/dev/null)
case "$out" in "dir=/nested/dir token=set(len $(( 9 + ${#second} ))) profile=$second args=-p hi") ok "nested call without a token (hooks) gets its own profile's key back";; *) bad "nested no-token -> $out";; esac
out=$(nest "$first" --x)
case "$out" in *"profile=$first args=--x") ok "nested call with explicit '$first' switches";; *) bad "nested explicit -> $out";; esac
# Installed as `claude` in a folder ahead of the real one: no recursion, reaches the real claude,
# and follows the real claude's link when auto-update repoints it.
mkdir -p "$T/hub" "$T/real/versions" "$T/realbin"
ln -s "$F" "$T/hub/claude"
for v in 1.0 2.0; do printf '#!/bin/sh\necho "real=%s args=$*"\n' "$v" > "$T/real/versions/$v"; chmod +x "$T/real/versions/$v"; done
ln -s "$T/real/versions/1.0" "$T/realbin/claude"
asclaude() { (cd "$T" && env -u CLAUDE_HUB_PROFILE PATH="$T/hub:$T/realbin:/usr/bin:/bin" HOME="$HOME" claude "$@" 2>/dev/null); }
out=$(asclaude mcp list); [ "$out" = "real=1.0 args=mcp list" ] && ok "as 'claude': runs the real claude" || bad "as claude -> $out"
ln -sfn "$T/real/versions/2.0" "$T/realbin/claude"
out=$(asclaude --version); [ "$out" = "real=2.0 args=--version" ] && ok "as 'claude': follows an auto-update" || bad "after update -> $out"
out=$(cd "$T" && env -u CLAUDE_HUB_PROFILE PATH="$T/hub:/usr/bin:/bin" HOME="$HOME" claude --x 2>&1); rc=$?
[ $rc = 127 ] && ok "no real claude on PATH -> error, no loop" || bad "self only -> rc=$rc $out"
sed "s/^${first}_KEY=.*/${first}_KEY=\"KEY_does_not_exist\"/" "$F" > "$T/badkey"
out=$(cd "$T" && env -u CLAUDE_CODE_OAUTH_TOKEN PATH="$T/bin:/usr/bin:/bin" HOME="$HOME" bash "$T/badkey" "$first" 2>/dev/null)
case "$out" in *"token=unset profile=$first"*) ok "bad ${first}_KEY -> Claude's own login";; *) bad "bad key -> $out";; esac
line=$(echo '{}' | CLAUDE_HUB_PROFILE="$first" bash "$F" statusline 2>/dev/null)
[ -n "$line" ] && ok "statusline: $(printf '%s' "$line" | sed 's/\x1b\[[0-9;]*m//g')" || bad "statusline empty"
[ $fail = 0 ] && echo "ALL OK" || { echo "FAILED"; exit 1; }
