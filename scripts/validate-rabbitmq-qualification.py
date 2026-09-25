#!/usr/bin/env python3
"""Validate issue #36 observations and emit the non-secret qualification."""

import argparse
import hashlib
import json
import os
import platform
import re
import sys


def fail(message):
    raise SystemExit(f"validate-rabbitmq-qualification: {message}")


def read(root, name):
    try:
        with open(os.path.join(root, name), encoding="utf-8") as handle:
            return handle.read().strip()
    except OSError as error:
        fail(f"cannot read {name}: {error}")


def read_json(root, name):
    try:
        with open(os.path.join(root, name), encoding="utf-8") as handle:
            return json.load(handle)
    except (OSError, json.JSONDecodeError) as error:
        fail(f"cannot parse {name}: {error}")


def digest(root, name):
    value = hashlib.sha256()
    try:
        with open(os.path.join(root, name), "rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                value.update(chunk)
    except OSError as error:
        fail(f"cannot digest {name}: {error}")
    return "sha256:" + value.hexdigest()


def canonical_digest(value, label):
    if not isinstance(value, str):
        fail(f"{label} is not a digest")
    raw = value.removeprefix("sha256:")
    if not re.fullmatch(r"[0-9a-f]{64}", raw):
        fail(f"{label} is not a sha256 digest")
    return "sha256:" + raw


def one_object(value, label):
    if not isinstance(value, list) or len(value) != 1 or not isinstance(value[0], dict):
        fail(f"{label} is not an exact one-item inventory")
    return value[0]


def cluster_node_inventories(cluster):
    if not isinstance(cluster, dict):
        fail("cluster observation is not an object")

    disk_nodes = cluster.get("disk_nodes")
    running_nodes = cluster.get("running_nodes")
    versions = cluster.get("versions")
    if not isinstance(disk_nodes, list) or not isinstance(running_nodes, list):
        fail("cluster observation does not contain node inventory lists")
    if not isinstance(versions, dict):
        fail("cluster observation does not contain the node version inventory")

    inventories = {
        "disk node": disk_nodes,
        "running node": running_nodes,
        "version node": list(versions),
    }
    for label, values in inventories.items():
        if not values or any(
            not isinstance(value, str)
            or not re.fullmatch(r"rabbit@[A-Za-z0-9._-]+", value)
            for value in values
        ):
            fail(f"cluster observation contains an invalid {label} identity")

    return tuple(sorted(set(values)) for values in inventories.values())


def os_release():
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


parser = argparse.ArgumentParser()
parser.add_argument("--evidence-dir", required=True)
parser.add_argument("--approval-record", required=True)
parser.add_argument("--environment", required=True)
parser.add_argument("--operator", required=True)
parser.add_argument("--image", required=True)
parser.add_argument("--expected-podman-version", required=True)
parser.add_argument("--unit", required=True)
parser.add_argument("--container", required=True)
parser.add_argument("--queue", required=True)
args = parser.parse_args()

with open(args.approval_record, encoding="utf-8") as handle:
    approval = json.load(handle)
approved = {
    "schemaVersion": "provision.dev/rabbitmq-packaging-approval/v1alpha1",
    "issue": 36,
    "decision": "digest-pinned-rootless-oci-quadlet",
    "environment": args.environment,
    "operator": args.operator,
    "product": {"name": "RabbitMQ", "version": "4.3.6"},
    "image": args.image,
    "topology": {
        "brokerNodes": 1,
        "queueType": "quorum",
        "queueMembers": 1,
        "hostFailureTolerance": 0,
    },
}
for key, expected in approved.items():
    if approval.get(key) != expected:
        fail(f"approval record does not bind expected {key}")

container = one_object(read_json(args.evidence_dir, "after-reboot-container.json"), "container observation")
image = one_object(read_json(args.evidence_dir, "after-reboot-image.json"), "image observation")
approved_manifest = canonical_digest(args.image.rsplit("@", 1)[1], "approved image")
observed_manifest = canonical_digest(container.get("ImageDigest"), "container image digest")
image_manifest = canonical_digest(image.get("Digest"), "image manifest digest")
container_config = canonical_digest(container.get("Image"), "container image configuration")
image_config = canonical_digest(image.get("Id"), "image configuration")
if observed_manifest != approved_manifest or image_manifest != approved_manifest:
    fail("running container does not use the approved platform manifest")
if container_config != image_config:
    fail("running container does not use the observed image configuration")
if container.get("ImageName") != args.image or args.image not in image.get("RepoDigests", []):
    fail("running container image name is not the approved digest reference")
if container.get("Name") != args.container:
    fail("running container identity differs from the approved identity")
state = container.get("State", {})
if state.get("Status") != "running" or state.get("Running") is not True:
    fail("container is not running")
if state.get("Health", {}).get("Status") != "healthy":
    fail("container health is not healthy")

queues = read_json(args.evidence_dir, "after-reboot-queues.json")
queue = one_object(queues, "Queue observation")
if queue.get("name") != args.queue or queue.get("type") != "quorum":
    fail("Queue identity or type differs from the approved topology")
if queue.get("durable") is not True:
    fail("Queue is not durable")
if queue.get("messages") != 0:
    fail("Queue is not empty after the acknowledged reboot delivery")

members = read_json(args.evidence_dir, "after-reboot-quorum-members.json")
if members.get("queue") != args.queue or not isinstance(members.get("members"), list):
    fail("quorum member observation does not identify the approved Queue")
member_names = members["members"]
if len(member_names) != 1 or len(set(member_names)) != 1:
    fail("quorum member count differs from the approved topology")

node = read(args.evidence_dir, "after-reboot-node.txt").strip("'")
cluster = read_json(args.evidence_dir, "after-reboot-cluster.json")
disk_nodes, running_nodes, version_nodes = cluster_node_inventories(cluster)
for label, observed_nodes in (
    ("disk", disk_nodes),
    ("running", running_nodes),
    ("version", version_nodes),
):
    if observed_nodes != [node]:
        fail(
            f"broker {label} node inventory {observed_nodes!r} differs from "
            f"the approved topology {[node]!r}"
        )
if member_names != [node]:
    fail("quorum member identity differs from the observed broker node")

account_entry = read(args.evidence_dir, "after-reboot-account.txt").split(":")
if len(account_entry) != 7:
    fail("service account observation is malformed")
account = f"provision-{args.environment}"
if account_entry[0] != account:
    fail("service account name differs from the Environment identity")
try:
    account_uid = int(account_entry[2])
    process_uid = int(read(args.evidence_dir, "after-reboot-main-pid-owner.txt"))
except ValueError:
    fail("service identity UID observation is malformed")
if process_uid != account_uid:
    fail("service process does not run as the Environment account")
if account_entry[5] != f"/var/lib/provision/runtime/{args.environment}":
    fail("service account home differs from the Environment runtime path")
if account_entry[6] != "/usr/sbin/nologin":
    fail("service account unexpectedly has a login shell")
observed_unit = read(args.evidence_dir, "after-reboot-unit-name.txt")
if observed_unit != args.unit:
    fail("observed unit identity differs from the approved unit")

podman_version = read(args.evidence_dir, "after-reboot-podman-version.txt")
rabbitmq_version = read(args.evidence_dir, "after-reboot-rabbitmq-version.txt")
if podman_version != args.expected_podman_version:
    fail("observed Podman version differs from the approved version")
if rabbitmq_version != approval["product"]["version"]:
    fail("observed RabbitMQ version differs from the approved product identity")

roundtrip = read_json(args.evidence_dir, "before-reboot-roundtrip.json")
redelivery = read_json(args.evidence_dir, "before-reboot-redelivery.json")
persistent = read_json(args.evidence_dir, "before-reboot-persistent.json")
consumed = read_json(args.evidence_dir, "after-reboot-consume.json")
if roundtrip.get("publisherConfirmed") is not True or roundtrip.get("consumerAcknowledged") is not True:
    fail("roundtrip did not prove publisher confirmation and manual acknowledgement")
if not all((
    redelivery.get("publisherConfirmed") is True,
    redelivery.get("consumerAcknowledged") is True,
    redelivery.get("redelivered") is True,
    redelivery.get("messagesAfterOperation") == 0,
)):
    fail("stable-ID redelivery after an unacknowledged channel close was not proved")
if persistent.get("publisherConfirmed") is not True or persistent.get("messagesAfterOperation") != 1:
    fail("pre-reboot persistent publish was not confirmed and retained")
if consumed.get("consumerAcknowledged") is not True or consumed.get("messagesAfterOperation") != 0:
    fail("post-reboot delivery was not acknowledged")
if persistent.get("messageId") != consumed.get("messageId"):
    fail("stable message identity did not survive reboot")
if read(args.evidence_dir, "before-reboot-boot-id.txt") == read(args.evidence_dir, "after-reboot-boot-id.txt"):
    fail("Host boot identity did not change")

alarms = read_json(args.evidence_dir, "after-reboot-alarms.json")
ping = read_json(args.evidence_dir, "after-reboot-ping.json")
features = read_json(args.evidence_dir, "after-reboot-features.json")
if alarms.get("localAlarmCheckPassed") is not True:
    fail("local RabbitMQ alarm check did not pass")
if ping.get("diagnosticsPingPassed") is not True:
    fail("RabbitMQ diagnostics ping did not pass")
if not isinstance(features, list) or not all(isinstance(item, dict) for item in features):
    fail("feature flag observation is not a JSON inventory")
enabled_features = sorted(
    item["name"] for item in features
    if isinstance(item.get("name"), str) and item.get("state") in ("enabled", True)
)

host_os = os_release()
result = {
    "schemaVersion": "provision.dev/rabbitmq-packaging-qualification/v1alpha2",
    "result": "passed",
    "decision": approval["decision"],
    "approval": {
        "recordSchemaVersion": approval["schemaVersion"],
        "operator": approval["operator"],
        "source": approval["source"]["kind"],
        "recordDigest": digest(args.evidence_dir, "approval-record.json"),
    },
    "environment": approval["environment"],
    "host": {
        "os": host_os.get("ID"),
        "osVersion": host_os.get("VERSION_ID"),
        "architecture": platform.machine(),
        "kernel": platform.release(),
        "bootIdChanged": True,
    },
    "runtime": {
        "podmanVersion": podman_version,
        "image": container["ImageName"],
        "imageManifest": observed_manifest,
        "imageConfig": image_config,
        "rabbitmqVersion": rabbitmq_version,
        "node": node,
    },
    "service": {
        "identity": account_entry[0],
        "uid": account_uid,
        "observedMainPidUid": process_uid,
        "unit": observed_unit,
        "container": container["Name"],
        "rootless": True,
        "activeAfterReboot": True,
        "credential": "encrypted-user-scoped-systemd-credential",
        "plaintextRecorded": False,
    },
    "topology": {
        "brokerNodes": len(disk_nodes),
        "queue": queue["name"],
        "queueType": queue["type"],
        "queueDurable": queue["durable"],
        "queueMembers": len(member_names),
        "hostFailureTolerance": 0,
    },
    "queueContract": {
        "delivery": {
            "status": "qualified",
            "value": "at-least-once",
            "evidence": "stable-ID redelivery after unacknowledged channel close",
        },
        "publisherConfirm": {"status": "qualified"},
        "consumerAcknowledgement": {"status": "qualified", "value": "manual"},
        "stableMessageIdentityAcrossRestart": {"status": "qualified"},
        "retry": {"status": "unqualified", "reason": "not exercised by packaging spike"},
        "deadLetter": {"status": "unqualified", "reason": "not configured or exercised"},
        "retention": {"status": "unqualified", "reason": "no retention policy exercised"},
        "ordering": {"status": "unqualified", "reason": "no concurrent ordering scenario exercised"},
        "deduplication": {"status": "unqualified", "reason": "stable identity observed; broker deduplication not configured"},
    },
    "observations": {
        "alarms": {"status": "observed", "localAlarmCheckPassed": True},
        "features": {"status": "observed", "enabled": enabled_features},
        "health": {"status": "observed", "value": state["Health"]["Status"], "diagnosticsPingPassed": True},
    },
    "evidenceDigests": {
        "packageInventory": digest(args.evidence_dir, "packages-after-reboot.txt"),
        "unitDefinition": digest(args.evidence_dir, "after-reboot-unit-definition.txt"),
        "imageObservation": digest(args.evidence_dir, "after-reboot-image.json"),
        "containerObservation": digest(args.evidence_dir, "after-reboot-container.json"),
        "queueObservation": digest(args.evidence_dir, "after-reboot-queues.json"),
        "quorumMembers": digest(args.evidence_dir, "after-reboot-quorum-members.json"),
        "probeBinary": read(args.evidence_dir, "probe.sha256").split()[0],
        "credentialEntrypoint": read(args.evidence_dir, "credential-entrypoint.sha256").split()[0],
    },
}

json.dump(result, sys.stdout, indent=2, sort_keys=True)
sys.stdout.write("\n")
