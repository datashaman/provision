#!/usr/bin/env bash
# Read-only architecture-spike probe for issue #36. It deliberately cannot
# install RabbitMQ or Podman, create accounts, pull images, or start services.

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: inspect-rabbitmq-packaging.sh --target local|USER@HOST --environment NAME --oci-image NAME@sha256:DIGEST

Observe the native-package and rootless Podman/Quadlet prerequisites for the
managed RabbitMQ packaging decision. Output is JSON. No host state is changed.
EOF
  exit 2
}

target=""
environment=""
oci_image=""
while (($#)); do
  case "$1" in
    --target) (($# >= 2)) || usage; target="$2"; shift 2 ;;
    --environment) (($# >= 2)) || usage; environment="$2"; shift 2 ;;
    --oci-image) (($# >= 2)) || usage; oci_image="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$target" && "$environment" =~ ^[a-z][a-z0-9-]{0,19}$ ]] || usage
[[ "$target" == local || "$target" =~ ^([a-z_][a-z0-9_-]*@)?[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$ ]] || {
  printf 'inspect-rabbitmq-packaging: --target must be local or a simple USER@HOST SSH destination\n' >&2
  exit 2
}
[[ "$oci_image" =~ ^[a-z0-9.-]+([:/][a-z0-9._-]+)+@sha256:[0-9a-f]{64}$ ]] || {
  printf 'inspect-rabbitmq-packaging: --oci-image must be a fully qualified digest reference; tags are not accepted\n' >&2
  exit 2
}

run_probe() {
  if [[ "$target" == local ]]; then
    bash -s -- "$environment" "$oci_image"
  else
    ssh -o BatchMode=yes -o StrictHostKeyChecking=yes "$target" bash -s -- "$environment" "$oci_image"
  fi
}

run_probe <<'REMOTE'
set -euo pipefail
PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH

environment="$1"
oci_image="$2"

python3 - "$environment" "$oci_image" <<'PY'
import json
import os
import platform
import re
import shutil
import subprocess
import sys


def run(*args):
    completed = subprocess.run(
        args,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
    )
    return completed.returncode, completed.stdout.strip()


def read_os_release():
    values = {}
    try:
        with open("/etc/os-release", encoding="utf-8") as handle:
            for line in handle:
                key, separator, value = line.rstrip().partition("=")
                if separator:
                    values[key] = value.strip('"')
    except OSError:
        pass
    return values


def apt_candidate(package):
    if shutil.which("apt-cache") is None:
        return {"available": False, "installedVersion": None, "candidateVersion": None,
                "repository": None, "packageSha256": None, "packagePath": None}

    _, policy = run("apt-cache", "policy", package)
    installed = None
    candidate = None
    repository = None
    lines = policy.splitlines()
    for index, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("Installed:"):
            value = stripped.partition(":")[2].strip()
            installed = None if value == "(none)" else value
        elif stripped.startswith("Candidate:"):
            value = stripped.partition(":")[2].strip()
            candidate = None if value == "(none)" else value
        elif candidate and stripped.startswith(candidate):
            for following in lines[index + 1:]:
                value = following.strip()
                if value.startswith("***") or value.startswith("Installed:"):
                    continue
                if re.search(r"https?://|file:/", value):
                    repository = value
                    break
                if value and not value[0].isdigit():
                    break
            if repository:
                break

    sha256 = None
    filename = None
    if candidate:
        _, details = run("apt-cache", "show", f"{package}={candidate}")
        paragraphs = details.split("\n\n")
        selected = paragraphs[0] if paragraphs else ""
        for line in selected.splitlines():
            key, separator, value = line.partition(":")
            if not separator:
                continue
            if key == "SHA256":
                sha256 = f"sha256:{value.strip()}"
            elif key == "Filename":
                filename = value.strip()

    return {
        "available": candidate is not None,
        "installedVersion": installed,
        "candidateVersion": candidate,
        "repository": repository,
        "packageSha256": sha256,
        "packagePath": filename,
    }


def account_observation(name):
    code, entry = run("getent", "passwd", name)
    if code != 0 or not entry:
        return {"name": name, "exists": False, "uid": None, "home": None,
                "shell": None, "subuid": False, "subgid": False, "linger": False}

    fields = entry.split(":")
    uid = fields[2] if len(fields) > 2 else None
    home = fields[5] if len(fields) > 5 else None
    shell = fields[6] if len(fields) > 6 else None

    def has_mapping(path):
        try:
            with open(path, encoding="utf-8") as handle:
                return any(line.startswith(f"{name}:") for line in handle)
        except OSError:
            return False

    linger = False
    if shutil.which("loginctl"):
        code, value = run("loginctl", "show-user", name, "-p", "Linger", "--value")
        linger = code == 0 and value == "yes"
    return {"name": name, "exists": True, "uid": uid, "home": home, "shell": shell,
            "subuid": has_mapping("/etc/subuid"), "subgid": has_mapping("/etc/subgid"),
            "linger": linger}


def systemctl_property(unit, property_name):
    if shutil.which("systemctl") is None:
        return None
    code, value = run("systemctl", "show", unit, f"-p{property_name}", "--value")
    return value or None if code == 0 else None


environment = sys.argv[1]
oci_image = sys.argv[2]
account = f"provision-{environment}"
os_release = read_os_release()
_, systemd_version = run("systemctl", "--version")
systemd_first_line = systemd_version.splitlines()[0] if systemd_version else None
podman_path = shutil.which("podman")
podman_version = None
if podman_path:
    _, podman_version = run("podman", "--version")

quadlet_paths = [
    "/usr/lib/systemd/system-generators/podman-system-generator",
    "/usr/lib/systemd/user-generators/podman-user-generator",
    "/usr/libexec/podman/quadlet",
]
quadlet_path = next((path for path in quadlet_paths if os.path.isfile(path)), None)
environment_account = account_observation(account)

result = {
    "schemaVersion": "provision.dev/rabbitmq-packaging-observation/v1alpha1",
    "decisionStatus": "awaiting-human-approval",
    "mutatingOperationsEnabled": False,
    "host": {
        "os": os_release.get("ID"),
        "osVersion": os_release.get("VERSION_ID"),
        "architecture": platform.machine(),
        "kernel": platform.release(),
        "systemdVersion": systemd_first_line,
        "cgroupV2": os.path.isfile("/sys/fs/cgroup/cgroup.controllers"),
    },
    "proposedQueueTopology": {
        "brokerNodes": 1,
        "queueType": "quorum",
        "queueMembers": 1,
        "hostFailureTolerance": 0,
        "delivery": "at-least-once-with-publisher-confirms-and-consumer-acknowledgements",
    },
    "nativePackage": {
        "product": apt_candidate("rabbitmq-server"),
        "service": {
            "unit": "rabbitmq-server.service",
            "loadState": systemctl_property("rabbitmq-server.service", "LoadState"),
            "activeState": systemctl_property("rabbitmq-server.service", "ActiveState"),
            "proposedIdentity": "rabbitmq",
            "scope": "host-wide",
        },
        "bootstrapBoundary": "root package and repository configuration",
        "upgradeBoundary": "root package transaction over the host-wide broker and its Erlang runtime",
        "ownedPaths": ["/etc/rabbitmq", "/var/lib/rabbitmq", "/var/log/rabbitmq"],
    },
    "ociQuadlet": {
        "image": oci_image,
        "imageDigestVerifiedByProbe": False,
        "runtimePackage": apt_candidate("podman"),
        "podmanInstalled": podman_path is not None,
        "podmanVersion": podman_version,
        "quadletInstalled": quadlet_path is not None,
        "quadletGenerator": quadlet_path,
        "service": {
            "unit": f"provision-{environment}-rabbitmq.service",
            "identity": account,
            "scope": "environment",
        },
        "environmentAccount": environment_account,
        "bootstrapBoundary": "root installs Podman and prepares rootless account, subids, linger, and owned state paths",
        "upgradeBoundary": "Plan changes an architecture-specific image digest; Queue data compatibility remains a separate gate",
        "ownedPaths": [
            f"/etc/containers/systemd/users/{environment_account['uid'] or '<uid>'}/provision-{environment}-rabbitmq.container",
            f"/var/lib/provision/environments/{environment}/services/rabbitmq",
        ],
    },
    "capabilityClaims": {
        "survivesBrokerRestart": "requires-live-proof",
        "survivesHostReboot": "requires-live-proof",
        "publisherConfirms": "requires-live-proof",
        "consumerAcknowledgements": "requires-live-proof",
        "quorumQueueObserved": "requires-live-proof",
        "hostLossTolerance": False,
        "credentialsViaSystemd": "requires-live-proof",
    },
}

print(json.dumps(result, indent=2, sort_keys=True))
PY
REMOTE
