#!/usr/bin/env bash
# End-to-end disposable-VM acceptance for issue #75.
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
target="${PROVISION_ACCEPTANCE_TARGET:-provision-acceptance.local}"
target_user="${PROVISION_ACCEPTANCE_USER:-marlinf}"
authority="${PROVISION_ACCEPTANCE_AUTHORITY:-$root/work/issue-39-live/authority}"
secret="${PROVISION_ACCEPTANCE_POSTGRESQL_SECRET:-$root/work/issue-75-live/postgresql-url.secret}"
remote="/tmp/provision-issue75-direct-local"
remote_evidence="/tmp/provision-issue75-evidence"
local_evidence="${PROVISION_ISSUE75_EVIDENCE:-$root/work/issue-75-live/evidence}"
ssh_opts=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)
scp_opts=(-q -o StrictHostKeyChecking=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=2 -o ConnectTimeout=15)

printf 'Provision issue #75 direct-local PostgreSQL acceptance\n'
printf 'Target: %s@%s (disposable acceptance VM)\n' "$target_user" "$target"
printf 'This restores the clean VM snapshot, bootstraps the Host Target, proves before/after interruption recovery for Database operations, copies evidence back, and restores the VM snapshot after success.\n'

cleanup_needed=false
preserve_remote_evidence_on_failure() {
  local destination
  destination="$local_evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$destination"
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side evidence path.
  if ssh "${ssh_opts[@]}" "$target_user@$target" "test -e '$remote_evidence'" >/dev/null 2>&1; then
    scp "${scp_opts[@]}" -r "$target_user@$target:$remote_evidence" "$destination/remote" >/dev/null 2>&1 || true
    printf 'Preserved remote failure evidence: %s/remote\n' "$destination" >&2
  fi
}

cleanup() {
  local status=$?
  if [[ "$cleanup_needed" == true ]]; then
    if [[ "$status" -ne 0 ]]; then
      preserve_remote_evidence_on_failure
    fi
    # PROVISION_ACCEPTANCE_KEEP_VM=1 leaves a failed VM in place for diagnosis.
    if [[ "$status" -eq 0 || -z "${PROVISION_ACCEPTANCE_KEEP_VM:-}" ]]; then
      "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes >/dev/null 2>&1 || true
    fi
  fi
  exit "$status"
}
trap cleanup EXIT

[[ -f "$authority/host-authority.pub" && -f "$authority/host-authority.key" ]] || {
  printf 'missing authority files; set PROVISION_ACCEPTANCE_AUTHORITY\n' >&2
  exit 2
}
if [[ ! -f "$secret" ]]; then
  mkdir -p "$(dirname "$secret")"
  umask 077
  printf 'postgresql://app:Issue75PostgreSQL_Secret_123456789@127.0.0.1:25432/app\n' >"$secret"
fi
[[ -f "$secret" && ! -L "$secret" ]] || {
  printf 'missing PostgreSQL secret file; set PROVISION_ACCEPTANCE_POSTGRESQL_SECRET\n' >&2
  exit 2
}

build_dir="$root/work/issue-75-live"
mkdir -p "$build_dir"
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision" ./cmd/provision
GOOS=linux GOARCH=amd64 go build -o "$build_dir/provision-host-executor" ./cmd/provision-host-executor
fixture_bundle="$build_dir/provision-example-database-http-linux-amd64.tar.gz"
build_database_http_fixture() {
  local fixture_dir="$build_dir/database-http-fixture"
  local fixture_binary="$fixture_dir/provision-example-database-http"
  mkdir -p "$fixture_dir"
  cat >"$fixture_dir/go.mod" <<'EOF'
module provision-issue75-database-http-fixture

go 1.26

require github.com/jackc/pgx/v5 v5.11.0
EOF
  cat >"$fixture_dir/main.go" <<'EOF'
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type deterministicRecord struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
}

type databaseBinding struct {
	LogicalID    string                `json:"logicalId"`
	GenerationID string                `json:"generationId"`
	Status       string                `json:"status"`
	Records      []deterministicRecord `json:"records,omitempty"`
	Reason       string                `json:"reason,omitempty"`
}

func main() {
	listen := getenv("PROVISION_HTTP_LISTEN", "127.0.0.1:0")
	revision := getenv("PROVISION_REVISION", "unknown")
	logicalID := os.Getenv("PROVISION_DATABASE_LOGICAL_ID")
	generationID := os.Getenv("PROVISION_DATABASE_GENERATION")
	databaseName := os.Getenv("PROVISION_DATABASE_NAME")
	namespace := logicalID + "/" + revision
	recordID := namespace + "/record-0001"

	http.HandleFunc("/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "live", "revision": revision})
	})
	http.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "revision": revision})
	})
	http.HandleFunc("/verify", func(w http.ResponseWriter, _ *http.Request) {
		records, err := verifyDatabase(recordID, namespace)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"revision": revision,
				"databaseBinding": databaseBinding{
					LogicalID: logicalID, GenerationID: generationID, Status: "failed", Reason: err.Error(),
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"revision": revision,
			"database": databaseName,
			"databaseBinding": databaseBinding{
				LogicalID: logicalID, GenerationID: generationID, Status: "verified", Records: records,
			},
		})
	})
	log.Fatal(http.ListenAndServe(listen, nil))
}

func verifyDatabase(recordID, namespace string) ([]deterministicRecord, error) {
	credentialPath := os.Getenv("PROVISION_DATABASE_URL_FILE")
	if credentialPath == "" {
		return nil, fmt.Errorf("PROVISION_DATABASE_URL_FILE is not set")
	}
	data, err := os.ReadFile(credentialPath)
	if err != nil {
		return nil, fmt.Errorf("read database credential file: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("open database connection: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS provision_deterministic_records (id text PRIMARY KEY, namespace text NOT NULL)`); err != nil {
		return nil, fmt.Errorf("create deterministic record table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provision_deterministic_records (id, namespace) VALUES ($1, $2) ON CONFLICT (id) DO UPDATE SET namespace = EXCLUDED.namespace`, recordID, namespace); err != nil {
		return nil, fmt.Errorf("write deterministic record: %w", err)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, namespace FROM provision_deterministic_records WHERE id = $1 AND namespace = $2 ORDER BY id`, recordID, namespace)
	if err != nil {
		return nil, fmt.Errorf("read deterministic record evidence: %w", err)
	}
	defer rows.Close()
	records := []deterministicRecord{}
	for rows.Next() {
		var record deterministicRecord
		if err := rows.Scan(&record.ID, &record.Namespace); err != nil {
			return nil, fmt.Errorf("scan deterministic record evidence: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deterministic record evidence: %w", err)
	}
	if len(records) != 1 || records[0].ID != recordID || records[0].Namespace != namespace {
		return nil, fmt.Errorf("deterministic record evidence does not match the expected identity")
	}
	return records, nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"status":"encoding-failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
EOF
  (cd "$fixture_dir" && GOWORK=off go mod tidy)
  (cd "$fixture_dir" && GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -buildid=" -o "$fixture_binary" .)
  python3 - "$fixture_binary" "$fixture_bundle" <<'PY'
import gzip
import io
import os
import stat
import sys
import tarfile
from pathlib import Path

binary = Path(sys.argv[1])
bundle = Path(sys.argv[2])
data = binary.read_bytes()
info = tarfile.TarInfo("provision-example-database-http")
info.size = len(data)
info.mode = 0o555
info.uid = 0
info.gid = 0
info.uname = "root"
info.gname = "root"
info.mtime = 0
buffer = io.BytesIO()
with gzip.GzipFile(filename="", mode="wb", fileobj=buffer, mtime=0) as gz:
    with tarfile.open(mode="w", fileobj=gz, format=tarfile.PAX_FORMAT) as archive:
        archive.addfile(info, io.BytesIO(data))
bundle.write_bytes(buffer.getvalue())
os.chmod(bundle, stat.S_IRUSR | stat.S_IWUSR)
PY
}
build_database_http_fixture
source_version="$(git -C "$root" rev-parse HEAD)"
if ! git -C "$root" diff --quiet HEAD --; then
  source_version="$source_version-dirty-$(git -C "$root" diff --binary HEAD -- | shasum -a 256 | awk '{print $1}')"
fi

run_local_checks() {
  local check_dir="$local_evidence/checks"
  mkdir -p "$check_dir"

  run_check() {
    local name="$1"
    shift
    printf '[check] %s\n' "$name"
    set +e
    "$@" >"$check_dir/$name.log" 2>&1
    local status=$?
    set -e
    printf '%s\n' "$status" >"$check_dir/$name.status"
    [[ "$status" == 0 ]] || {
      printf 'check failed: %s; see %s\n' "$name" "$check_dir/$name.log" >&2
      exit "$status"
    }
  }

  run_check bash-n bash -n \
    "$root/scripts/test-direct-local-postgresql-host.sh" \
    "$root/scripts/run-direct-local-postgresql-acceptance.sh" \
    "$root/scripts/direct-local-fault-bin/sudo"
  if command -v shellcheck >/dev/null 2>&1; then
    run_check shellcheck shellcheck \
      "$root/scripts/test-direct-local-postgresql-host.sh" \
      "$root/scripts/run-direct-local-postgresql-acceptance.sh" \
      "$root/scripts/direct-local-fault-bin/sudo"
  fi
  run_check go-test-database-planner go test ./cmd/provision -run TestDatabasePlanPreviewIsDeterministicAndFailsClosedForUnqualifiedTransitions

  python3 - "$check_dir" "$source_version" <<'PY'
import json
from pathlib import Path
import sys

check_dir = Path(sys.argv[1])
source_version = sys.argv[2]
checks = []
for status_path in sorted(check_dir.glob("*.status")):
    checks.append({
        "name": status_path.stem,
        "exitStatus": int(status_path.read_text(encoding="utf-8").strip()),
        "log": f"{status_path.stem}.log",
    })
(check_dir / "summary.json").write_text(json.dumps({
    "schemaVersion": "provision.dev/direct-local-postgresql-acceptance-checks/v1alpha1",
    "sourceVersion": source_version,
    "checks": checks,
}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY
}

reset_and_bootstrap() {
  "$root/work/acceptance-host/reset-acceptance-vm.sh" --yes

  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "if [ -e '$remote' ]; then mv '$remote' '$remote.failed-$(date -u +%Y%m%dT%H%M%SZ)'; fi; mkdir -p '$remote' && chmod 0700 '$remote'"
  scp "${scp_opts[@]}" \
    "$build_dir/provision" \
    "$build_dir/provision-host-executor" \
    "$fixture_bundle" \
    "$authority/host-authority.pub" \
    "$authority/host-authority.key" \
    "$secret" \
    "$target_user@$target:$remote/"
  scp "${scp_opts[@]}" -r \
    "$root/scripts" \
    "$root/examples" \
    "$target_user@$target:$remote/"
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "chmod 0600 '$remote/host-authority.key' '$remote/$(basename "$secret")'"

  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "sudo -n bash '$remote/scripts/bootstrap-host.sh' --apply --replace-executor --environment lab --operator '$target_user' --binary '$remote/provision-host-executor' --authority-public-key '$remote/host-authority.pub'"
}

run_fault_mode() {
  local mode="$1"
  local destination="$local_evidence/$mode"
  local remote_destination="$remote_evidence/$mode"

  cleanup_needed=true
  reset_and_bootstrap
  # shellcheck disable=SC2029 # Remote command intentionally receives client-side acceptance paths and source identity.
  ssh "${ssh_opts[@]}" "$target_user@$target" \
    "if [ -e '$remote_destination' ]; then mv '$remote_destination' '$remote_destination.failed-$(date -u +%Y%m%dT%H%M%SZ)'; fi; mkdir -p '$remote_evidence'; cd '$remote' && ./scripts/test-direct-local-postgresql-host.sh --provision ./provision --signing-key ./host-authority.key --secret-file ./$(basename "$secret") --artifact-bundle ./$(basename "$fixture_bundle") --work-dir '$remote_destination' --provision-version '$source_version' --confirm-disposable-host provision-acceptance --fault-mode '$mode'"
  mkdir -p "$(dirname "$destination")"
  scp "${scp_opts[@]}" -r "$target_user@$target:$remote_destination" "$destination"
}

if [[ -e "$local_evidence" ]]; then
  mv "$local_evidence" "$local_evidence.failed-$(date -u +%Y%m%dT%H%M%SZ)"
fi
mkdir -p "$local_evidence"

run_local_checks
for mode in ${PROVISION_ACCEPTANCE_MODES:-before after undecidable}; do
  run_fault_mode "$mode"
done
"$root/work/acceptance-host/reset-acceptance-vm.sh" --yes
cleanup_needed=false
trap - EXIT

printf '\nDirect-local PostgreSQL acceptance passed.\n'
printf 'evidence: %s\n' "$local_evidence"
