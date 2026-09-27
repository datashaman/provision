#!/usr/bin/env bash
# Live acceptance for Schedule civil-time missed-run policy on a disposable Host.
set -euo pipefail

usage() {
  echo "usage: $0 [--skip-normal] --provision BINARY --signing-key FILE --config ROOT.yaml --secret-file FILE --work-dir DIRECTORY --target HOST --target-user USER" >&2
  exit 2
}

provision=""
signing_key=""
config=""
secret_file=""
work_dir=""
target=""
target_user=""
skip_normal=false
while (($#)); do
  case "$1" in
    --skip-normal) skip_normal=true; shift ;;
    --provision) (($# >= 2)) || usage; provision="$2"; shift 2 ;;
    --signing-key) (($# >= 2)) || usage; signing_key="$2"; shift 2 ;;
    --config) (($# >= 2)) || usage; config="$2"; shift 2 ;;
    --secret-file) (($# >= 2)) || usage; secret_file="$2"; shift 2 ;;
    --work-dir) (($# >= 2)) || usage; work_dir="$2"; shift 2 ;;
    --target) (($# >= 2)) || usage; target="$2"; shift 2 ;;
    --target-user) (($# >= 2)) || usage; target_user="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -x "$provision" && -f "$signing_key" && -f "$config" && -f "$secret_file" ]] || usage
[[ "$work_dir" == /* && ! -e "$work_dir" ]] || usage
[[ "$target" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ && "$target_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || usage

mkdir -m 0700 "$work_dir"

if [[ "$skip_normal" == false ]]; then
  echo "[1/3] prove normal Schedule occurrence through complete async deployment"
  "$(dirname "$0")/test-first-scheduled-message-host.sh" \
    --provision "$provision" \
    --signing-key "$signing_key" \
    --config "$config" \
    --secret-file "$secret_file" \
    --work-dir "$work_dir/normal" \
    --target "$target" \
    --target-user "$target_user"
else
  echo "[1/3] reuse existing normal Schedule deployment"
fi

echo "[2/3] drive installed applet through missed-run skip and bounded catch-up"
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target_user@$target" 'python3 -' > "$work_dir/policy-summary.json" <<'PY'
import datetime as dt
import json
import os
import sqlite3
import subprocess
import sys
import time
from pathlib import Path
from zoneinfo import ZoneInfo

schedule_path = Path("/var/lib/provision/environments/lab/schedules/every-minute/schedule.json")
runtime = Path("/var/lib/provision/environments/lab/runtime/provision-runtime-schedule")

def sudo_write_schedule(record):
    payload = json.dumps(record, indent=2, sort_keys=True) + "\n"
    subprocess.run(
        ["sudo", "-n", "python3", "-c", "import pathlib,sys; p=pathlib.Path(sys.argv[1]); p.write_text(sys.stdin.read(), encoding='utf-8'); p.chmod(0o444)", str(schedule_path)],
        input=payload,
        text=True,
        check=True,
    )

def sudo_runtime():
    completed = subprocess.run(
        ["sudo", "-n", str(runtime), "run", "--environment", "lab", "--schedule", "every-minute"],
        text=True,
        capture_output=True,
        check=True,
    )
    return json.loads(completed.stdout)

def read_record():
    return json.loads(schedule_path.read_text(encoding="utf-8"))

def latest_wall(ledger):
    with sqlite3.connect(f"file:{ledger}?mode=ro&immutable=1", uri=True) as connection:
        row = connection.execute("select due_wall from occurrences order by due_wall desc limit 1").fetchone()
    if row is None:
        raise SystemExit("normal deployment did not create a Schedule occurrence")
    return dt.datetime.strptime(row[0], "%Y-%m-%dT%H:%M")

def recent(ledger, count=20):
    with sqlite3.connect(f"file:{ledger}?mode=ro&immutable=1", uri=True) as connection:
        return [
            {
                "wallDueAt": row[0],
                "disposition": row[1],
                "outcome": row[2],
                "attempts": row[3],
            }
            for row in connection.execute(
                """
                select o.due_wall, o.disposition, i.outcome, count(a.number)
                from occurrences o
                join invocations i on i.id = o.invocation_id
                left join attempts a on a.invocation_id = i.id
                group by o.id, i.id
                order by o.due_wall desc
                limit ?
                """,
                (count,),
            )
        ]

def wait_until_after(location, wall, seconds=1):
    while True:
        now = dt.datetime.now(location).replace(second=0, microsecond=0).replace(tzinfo=None)
        if now > wall:
            return now
        time.sleep(seconds)

def wait_until_at_least(location, wall, seconds=1):
    while True:
        now = dt.datetime.now(location).replace(second=0, microsecond=0).replace(tzinfo=None)
        if now >= wall:
            return now
        time.sleep(seconds)

record = read_record()
ledger = record["ledgerPath"]
location = ZoneInfo(record["timezone"])
subprocess.run(["sudo", "-n", "systemctl", "stop", record["timerUnit"]], check=True)

normal_wall = latest_wall(ledger)
skip_wall = normal_wall + dt.timedelta(minutes=1)
wait_until_after(location, skip_wall)

record["expression"] = f"{skip_wall.minute} {skip_wall.hour} * * *"
record["missedRun"] = {"mode": "skip", "maxOccurrences": 0}
sudo_write_schedule(record)
skip_result = sudo_runtime()
skip_rows = recent(ledger)
skip_match = [row for row in skip_rows if row["wallDueAt"] == skip_wall.strftime("%Y-%m-%dT%H:%M")]
if not skip_match or skip_match[0]["disposition"] != "skipped-missed" or skip_match[0]["attempts"] != 0:
    raise SystemExit(f"missed-run skip did not record a skipped occurrence without a Task: {skip_rows!r}")

catchup_ready = skip_wall + dt.timedelta(minutes=3)
wait_until_at_least(location, catchup_ready)
record = read_record()
record["expression"] = "* * * * *"
record["missedRun"] = {"mode": "bounded-catch-up", "maxOccurrences": 2}
sudo_write_schedule(record)
catchup_first = sudo_runtime()
catchup_second = sudo_runtime()
rows = recent(ledger)
new_rows = [row for row in rows if row["wallDueAt"] > skip_wall.strftime("%Y-%m-%dT%H:%M")]
runnable = [row for row in new_rows if row["disposition"] in {"recorded", "running", "succeeded", "failed", "timed-out", "uncertain"}]
skipped = [row for row in new_rows if row["disposition"] == "skipped-missed"]
if len(runnable) > 2 or not skipped:
    raise SystemExit(f"bounded catch-up exceeded its cap or failed to record older missed occurrences: {rows!r}")

print(json.dumps({
    "schemaVersion": "provision.dev/schedule-policy-live-acceptance/v1alpha1",
    "normalWallDueAt": normal_wall.strftime("%Y-%m-%dT%H:%M"),
    "skipWallDueAt": skip_wall.strftime("%Y-%m-%dT%H:%M"),
    "skipResult": skip_result,
    "catchupResults": [catchup_first, catchup_second],
    "recent": rows,
}, indent=2, sort_keys=True))
PY

echo "[3/3] verify structured Host status reports normal, skipped, and catch-up occurrences"
"$provision" host bootstrap check \
  --address "$target" --user "$target_user" --environment lab --operator "$target_user" \
  > "$work_dir/bootstrap-after-policy.json"

python3 - "$work_dir/policy-summary.json" "$work_dir/bootstrap-after-policy.json" <<'PY'
import json
import sys

summary = json.load(open(sys.argv[1], encoding="utf-8"))
inspection = json.load(open(sys.argv[2], encoding="utf-8"))
deployment = inspection.get("async", {}).get("deployment", {})
occurrences = deployment.get("occurrences", [])
invocations = deployment.get("taskInvocations", [])
if not occurrences or not invocations:
    raise SystemExit("structured Host status omitted Schedule occurrences or Task Invocations")
by_wall = {item.get("wallDueAt"): item for item in occurrences}
if summary["normalWallDueAt"] not in by_wall:
    raise SystemExit("structured status omitted the normal occurrence")
if not any(item.get("disposition") == "skipped-missed" for item in occurrences):
    raise SystemExit("structured status omitted the missed-run skipped disposition")
skip_wall = summary["skipWallDueAt"]
runnable = [item for item in summary["recent"] if item["wallDueAt"] > skip_wall and item["disposition"] in {"recorded", "running", "succeeded", "failed", "timed-out", "uncertain"}]
if len(runnable) < 1 or len(runnable) > 2:
    raise SystemExit("bounded catch-up summary does not show a capped runnable set")
print("Schedule policy live acceptance passed")
PY

echo "schedule policy acceptance passed"
echo "evidence: $work_dir"
