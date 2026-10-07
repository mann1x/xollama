#!/bin/bash
# The security check of a release (docs/protocols/RELEASE.md, "The security
# check"). Fetches the open Dependabot alerts and runs govulncheck on the
# release toolchain, writes both into docs/protocols/security/, and prints what
# changed against the snapshot committed there. Every alert in the delta is
# investigated before the release; the new snapshot is committed with the
# release record.
#
#   scripts/security-check.sh            # write the new snapshot, print the delta
#   OUT=/some/dir scripts/security-check.sh   # write elsewhere (dry run)
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
REPO=${REPO:-mann1x/xollama}
OUT=${OUT:-docs/protocols/security}
mkdir -p "$OUT"
DEP=$OUT/dependabot.tsv
VULN=$OUT/govulncheck.txt
OLD=$(mktemp)
trap 'rm -f "$OLD" "$OLD.json"' EXIT
git show "HEAD:docs/protocols/security/dependabot.tsv" > "$OLD" 2>/dev/null || : > "$OLD"

# The release toolchain: the newest patch release on go.mod's go line, as the
# release workflow's plan job derives it (RELEASE.md).
line=$(awk '/^go /{split($2,v,"."); print v[1]"."v[2]; exit}' go.mod)
tc=$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' | python3 -c "
import json,sys
l='go$line.'
v=[r['version'] for r in json.load(sys.stdin) if r['stable'] and r['version'].startswith(l)]
print(max(v,key=lambda s:int(s[len(l):])))")

gh api -X GET "repos/$REPO/dependabot/alerts" --paginate --slurp -F state=open -F per_page=100 > "$OLD.json"
python3 - "$OLD.json" "$DEP" <<'EOF'
import json, sys
pages = json.load(open(sys.argv[1]))
alerts = [a for p in pages for a in p]
cols = ["ghsa", "severity", "scope", "manifest", "package", "vulnerable", "fixed", "created", "summary"]
rows = []
for a in alerts:
    v, s, d = a["security_vulnerability"], a["security_advisory"], a["dependency"]
    rows.append([s["ghsa_id"], s["severity"], d.get("scope") or "", d["manifest_path"],
                 d["package"]["name"], v["vulnerable_version_range"],
                 (v.get("first_patched_version") or {}).get("identifier") or "",
                 a["created_at"][:10], s["summary"].replace("\t", " ")[:120]])
rows.sort(key=lambda r: (r[3], r[4], r[0], r[5]))
with open(sys.argv[2], "w") as f:
    f.write("\t".join(cols) + "\n")
    for r in rows:
        f.write("\t".join(r) + "\n")
EOF

GOTOOLCHAIN=$tc govulncheck -show verbose ./... > "$VULN.full" 2>&1 || true
{ echo "# govulncheck $(GOTOOLCHAIN=$tc go version | awk '{print $3}'), tree $(git rev-parse --short HEAD), $(date -u +%F)"
  grep -E '^=== |^Vulnerability #|^    [A-Z].*\.|^    Found in:|^    Fixed in:|^  Module:|^  Standard library|^Your code is affected|^This scan also found|^vulnerabilities in modules' "$VULN.full"; } > "$VULN"
rm -f "$VULN.full"

echo "== Dependabot: $(($(wc -l < "$DEP") - 1)) open (was $(( $(wc -l < "$OLD") > 0 ? $(wc -l < "$OLD") - 1 : 0 )))"
python3 - "$OLD" "$DEP" <<'EOF'
import csv, sys
def load(p):
    try:
        with open(p) as f:
            return {(r["ghsa"], r["manifest"], r["package"], r["vulnerable"]): r for r in csv.DictReader(f, delimiter="\t")}
    except FileNotFoundError:
        return {}
old, new = load(sys.argv[1]), load(sys.argv[2])
for k in sorted(new.keys() - old.keys(), key=lambda k: (k[1], k[2])):
    r = new[k]; print(f"  NEW  {r['severity']:8} {r['scope']:11} {r['manifest']}  {r['package']} {r['vulnerable']} -> {r['fixed']}  {r['summary'][:70]}")
for k in sorted(old.keys() - new.keys(), key=lambda k: (k[1], k[2])):
    r = old[k]; print(f"  GONE {r['severity']:8} {r['scope']:11} {r['manifest']}  {r['package']} {r['vulnerable']}")
EOF
echo "== govulncheck ($tc)"; grep -E '^Your code is affected|^This scan also found|^vulnerabilities in modules' "$VULN"
# An alert is the fork's own when its package's line in the manifest is one
# the fork changed against the upstream release it is based on; otherwise it
# is upstream's, inherited unchanged.
up=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' "$(git merge-base HEAD upstream/main)" 2>/dev/null || echo upstream/main)
echo "== alerts on a line the fork changed against $up (empty = every alert is upstream's)"
tail -n +2 "$DEP" | cut -f4,5 | sort -u | while IFS=$'\t' read -r m pkg; do
  git diff "$up" HEAD -- "$m" | grep -E '^[-+][^-+]' | grep -qF "$pkg" && echo "  $m  $pkg"; done || true
echo "wrote $DEP and $VULN"
