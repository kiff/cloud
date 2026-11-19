#!/usr/bin/env bash
# Checks that the box's role may read every secret deploy.sh reads.
# deploy.sh names each secret; the role's inline policies grant them by ARN
# or prefix. The two used to drift: a secret added to deploy.sh with no
# grant came up empty and turned a feature off without a word.
#
# Usage: scripts/check-deploy-secrets.sh [role]   (default kiff-ec2-app-role)
# Exit 0 when every secret is granted, 1 when one isn't, 2 when IAM can't
# be read with the caller's credentials.
set -euo pipefail
cd "$(dirname "$0")/.."
role="${1:-kiff-ec2-app-role}"

# The names come from deploy.sh; each value from the environment (deploy.sh
# exports them, or source .env.deploy first), else from deploy.sh's literal
# default. An empty value or "-" is a secret that's off. Any other value is
# neither a Secrets Manager name (kiff/...) nor its ARN, so its grant can't be
# checked: it's said, never skipped without a word.
secrets=""
while IFS= read -r name; do
	value="${!name-}"
	[ -n "$value" ] || value=$(sed -nE "s/^${name}=\"\\\$\{${name}:-([^}]*)\}\"$/\1/p" scripts/deploy.sh)
	case "$value" in
		arn:aws:secretsmanager:*|kiff/*) secrets+="$value"$'\n' ;;
		""|-) ;;
		*) echo "warning: $name is neither a Secrets Manager name (kiff/...) nor its ARN; its grant isn't checked" >&2 ;;
	esac
done < <(sed -nE 's/^([A-Z_]+(_SECRET|_ARN))="\$\{[A-Z_]+:?-.*/\1/p' scripts/deploy.sh | sort -u)
[ -n "$secrets" ] || { echo "no secrets set: source .env.deploy, or run it from deploy.sh" >&2; exit 1; }

policies=$(aws iam list-role-policies --role-name "$role" --query PolicyNames --output text 2>/dev/null) || {
	echo "can't read $role's policies with these credentials" >&2
	exit 2
}
grants=""
for p in $policies; do
	grants+=$(aws iam get-role-policy --role-name "$role" --policy-name "$p" --query PolicyDocument --output json)$'\n'
done

GRANTS="$grants" SECRETS="$secrets" python3 - <<'PY'
import fnmatch, json, os, sys
raw = os.environ["GRANTS"]
dec, docs, i = json.JSONDecoder(), [], 0
while i < len(raw):
    if raw[i].isspace():
        i += 1
        continue
    d, i = dec.raw_decode(raw, i)
    docs.append(d)
resources = []
for d in docs:
    for st in d.get("Statement", []):
        if st.get("Effect") != "Allow":
            continue
        acts = st.get("Action", [])
        acts = [acts] if isinstance(acts, str) else acts
        if not any(a in ("secretsmanager:GetSecretValue", "secretsmanager:*", "*") for a in acts):
            continue
        r = st.get("Resource", [])
        resources += [r] if isinstance(r, str) else r
missing = []
for s in os.environ["SECRETS"].split():
    # A name is granted by its ARN with Secrets Manager's 6-character suffix.
    target = s if s.startswith("arn:") else None
    ok = False
    for r in resources:
        if target:
            ok = fnmatch.fnmatchcase(target, r)
        else:
            name = r.split(":secret:")[-1]
            ok = fnmatch.fnmatchcase(s + "-AbCdEf", name) or (name.startswith(s + "-") and len(name) == len(s) + 7)
        if ok:
            break
    if not ok:
        missing.append(s)
for m in missing:
    print(f"not granted to the role: {m}")
sys.exit(1 if missing else 0)
PY
echo "every secret deploy.sh reads is granted to $role"
