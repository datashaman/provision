#!/usr/bin/env bash
# Shared helpers for the Valkey direct-local scripts. Callers define fail(), provision, state, work_dir,
# config_dir, operator, signing_key and plan_id before use.
# shellcheck disable=SC2154 # those variables are supplied by the sourcing script
assert_contains() { grep -Fq "$2" "$1" || fail "$1 does not contain: $2"; }

json_assert() {
  python3 - "$1" "$2" "$3" <<'PY'
import json, sys
path, field, expected = sys.argv[1:]
value = json.load(open(path, encoding="utf-8"))
for segment in field.split("."):
    value = value[int(segment)] if isinstance(value, list) else value[segment]
if value != json.loads(expected):
    raise SystemExit(f"{path}: {field} = {value!r}, expected {expected}")
PY
}

plan_kind() {
  python3 - "$1" "$2" <<'PY'
import json, sys
for op in json.load(open(sys.argv[1], encoding="utf-8"))["operations"]:
    if op["id"] == sys.argv[2]:
        print(op["kind"]); break
else:
    raise SystemExit(f"operation {sys.argv[2]} is absent")
PY
}

plan_operations() {
  python3 -c 'import json,sys; print(" ".join(o["id"] for o in json.load(open(sys.argv[1]))["operations"]))' "$1"
}

journal_assert_latest() {
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import json, sys
path, op, kind, outcome = sys.argv[1:]
events = [e for e in json.load(open(path, encoding="utf-8"))["events"] if e["operationId"] == op]
if not events:
    raise SystemExit(f"{path}: no event for {op}")
last = events[-1]
if last["kind"] != kind or last.get("outcome", "-") != outcome:
    raise SystemExit(f"{path}: latest {op} is {last['kind']}/{last.get('outcome', '-')}, expected {kind}/{outcome}")
PY
}

journal_assert_paused() {
  python3 - "$1" "$2" <<'PY'
import json, sys
path, op = sys.argv[1:]
events = json.load(open(path, encoding="utf-8"))["events"]
last = events[-1]
if last["operationId"] != op or last["kind"] != "intent":
    raise SystemExit(f"{path}: journal must end at {op} intent, got {last['operationId']} {last['kind']}")
if any(e["operationId"] == op and e["kind"] == "outcome" for e in events):
    raise SystemExit(f"{path}: interrupted {op} was marked with an outcome")
if not (last.get("attemptId") and last.get("fencingToken")):
    raise SystemExit(f"{path}: paused {op} lacks attempt/fencing evidence")
PY
}

journal_assert_resume() {
  python3 - "$1" "$2" "$3" <<'PY'
import json, sys
path, op, outcome = sys.argv[1:]
events = [e for e in json.load(open(path, encoding="utf-8"))["events"] if e["operationId"] == op]
if not [e for e in events if e["kind"] == "intent" and e.get("observation", {}).get("resumeOfAttemptId")]:
    raise SystemExit(f"{path}: {op} has no resume provenance")
last = events[-1]
if last["kind"] != "outcome" or last.get("outcome") != outcome:
    raise SystemExit(f"{path}: resumed {op} latest = {last!r}, expected {outcome}")
PY
}

abs() { echo "$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"; }

preview_and_approve() {
  local name="$1" directory="$work_dir/$1"
  mkdir -m 0700 "$directory"
  "$provision" config validate --file "$config_dir/root.yaml" >"$directory/validation.json"
  "$provision" plan preview --file "$config_dir/root.yaml" --state "$state" >"$directory/plan.json"
  plan_id="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["id"])' "$directory/plan.json")"
  [[ "$plan_id" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "invalid Plan identity for $name"
  "$provision" plan approve --file "$config_dir/root.yaml" --plan "$plan_id" --actor "$operator" --state "$state" --expires-after 2h >"$directory/approval.json"
  json_assert "$directory/approval.json" eligible true
}


write_status() { "$provision" deployment status --plan "$plan_id" --state "$state" >"$work_dir/$1/$2-status.json"; }
plan_generation() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["keyValue"]["generationId"])' "$1"; }

set_environment_version() {
  python3 - "$config_dir/environment.yaml" "$1" <<'PY'
from pathlib import Path
import sys
path, version = Path(sys.argv[1]), sys.argv[2]
out = []
for line in path.read_text(encoding="utf-8").splitlines():
    if line.strip().startswith("version:"):
        continue
    out.append(line)
    if line.strip() == "credential: secret://lab/valkey-url":
        out.append(line[: len(line) - len(line.lstrip())] + f'version: "{version}"')
path.write_text("\n".join(out) + "\n", encoding="utf-8")
PY
}

