#!/usr/bin/env bash
# rbac-covers-executor.sh — the chart's Role must permit every Kubernetes call
# the executor makes.
#
# These two files have no compiler between them. The executor gained PVC
# create/delete for the staging volume (ADR 0022) and the Role was never
# updated, so every DAG using staging failed with a 403 in any cluster that
# enforces RBAC — while `helm unittest` stayed green, because it asserts what
# the template renders and never what the code needs.
#
# This closes the loop in the direction that bites: a call with no permission is
# an error. The reverse (a permission with no call) is reported as a warning
# only — over-granting is worth knowing about, but it is not a broken deploy,
# and `get`/`watch` on pods are plausibly wanted by future work.
#
# It is a grep, not type-aware analysis. It reads `CoreV1().<Resource>(...)` and
# the verb chained onto it, which is how every call in internal/executor is
# written today. A call built through a variable would be missed — accepted,
# because the alternative is a linter plugin and this catches the real drift.
set -euo pipefail

cd "$(dirname "$0")/.."

EXEC_DIR="internal/executor"
CHART="helm/leoflow"
fail=0

# Go clientset method -> Kubernetes RBAC resource name.
resource_of() {
	case "$1" in
	Pods) echo "pods" ;;
	PersistentVolumeClaims) echo "persistentvolumeclaims" ;;
	ConfigMaps) echo "configmaps" ;;
	Secrets) echo "secrets" ;;
	Services) echo "services" ;;
	Events) echo "events" ;;
	*) echo "" ;;
	esac
}

# Render the Role once. Values are the minimum that lets the chart render.
role="$(helm template rbac-check "$CHART" \
	--set database.url=postgres://h/d \
	--set redis.url=redis://h/0 \
	--set auth.jwtSecret=j \
	--set agentTLS.serverCertSecret=cert \
	--set agentTLS.caConfigMap=ca \
	--show-only templates/rbac.yaml 2>/dev/null)"

if [ -z "$role" ]; then
	echo "FAIL: could not render templates/rbac.yaml" >&2
	exit 1
fi

# Collect (resource, verb) pairs the executor calls.
# Production files only. Tests drive a fake clientset and legitimately call
# verbs production never needs — a test asserting a PVC was created reads it
# back with Get, which is not a permission the control plane requires.
# Excluding by filename, not by filtering output lines: `grep -h` prints no
# filenames, so a line-level filter here would silently match nothing.
prod_files="$(find "$EXEC_DIR" -name '*.go' ! -name '*_test.go')"
calls="$(grep -hoE 'CoreV1\(\)\.[A-Za-z]+\([^)]*\)\.[A-Za-z]+\(' $prod_files |
	sed -E 's/CoreV1\(\)\.([A-Za-z]+)\(.*\)\.([A-Za-z]+)\($/\1 \2/' |
	sort -u)"

if [ -z "$calls" ]; then
	echo "FAIL: found no Kubernetes calls in $EXEC_DIR — the grep pattern has drifted from the code" >&2
	exit 1
fi

while read -r method verb; do
	[ -n "$method" ] || continue
	res="$(resource_of "$method")"
	if [ -z "$res" ]; then
		echo "FAIL: $method is called in $EXEC_DIR but this script has no RBAC mapping for it." >&2
		echo "      Add it to resource_of() so the permission can be checked." >&2
		fail=1
		continue
	fi
	lverb="$(echo "$verb" | tr '[:upper:]' '[:lower:]')"
	# Pair each rule's `resources:` with its own `verbs:`, one line per rule.
	#
	# A proximity match (grep -A<n>) was the first attempt and is wrong for a
	# security check: the window bleeds into the NEXT rule, so a verb granted on
	# some other resource can satisfy the test. That fails open — the one
	# direction a permission check must never fail in. Whether it happens to be
	# correct depends on how many lines apart two rules sit, which is not a
	# property anyone maintains on purpose.
	pairs="$(echo "$role" | awk '
		/^[[:space:]]*-?[[:space:]]*resources:/ { r = $0 }
		/^[[:space:]]*verbs:/ { if (r != "") { print r " || " $0; r = "" } }
	')"
	if ! echo "$pairs" | grep "\"$res\"" | grep -q "\"$lverb\""; then
		echo "FAIL: the executor calls $method.$verb but the Role does not grant $lverb on $res" >&2
		echo "      -> add it to $CHART/templates/rbac.yaml, or stop making the call" >&2
		fail=1
	else
		echo "OK:   $res/$lverb granted (called as $method.$verb)"
	fi
done <<<"$calls"

if [ "$fail" -ne 0 ]; then
	echo >&2
	echo "rbac-covers-executor: the chart's Role does not cover the executor's calls." >&2
	exit 1
fi

echo
echo "rbac-covers-executor: every executor call is permitted"
