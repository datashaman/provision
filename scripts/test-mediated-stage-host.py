#!/usr/bin/env python3
"""Root supervisor for isolated accounts on a disposable Linux controller; never run on a workstation."""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import pwd
import shutil
import ssl
import subprocess
import threading
import time

parser = argparse.ArgumentParser()
parser.add_argument('--target', required=True)
parser.add_argument('--target-user', required=True)
parser.add_argument('--controller-address', required=True)
parser.add_argument('--provision-version', required=True)
parser.add_argument('--work-dir', required=True)
parser.add_argument('--confirm-disposable-host', required=True)
a = parser.parse_args()
assert os.geteuid() == 0 and os.uname().nodename == 'provision-mediation-controller', 'requires the disposable controller root supervisor'
root = Path('/srv/mediation')
private = root / 'private'
evidence = Path(a.work_dir)
evidence.mkdir(mode=0o700)
commands = []
cases = []
operator = 'authority'
agent = 'requestbot'
other = 'requestother'
binary = '/usr/local/bin/provision'
registry = private / 'registry.json'
state = private / 'state.db'
signing = private / 'authority.key'
ssh = ['ssh', '-o', 'BatchMode=yes', '-o', 'PasswordAuthentication=no', '-o', 'StrictHostKeyChecking=yes', '-o', 'ConnectTimeout=5', f'{a.target_user}@{a.target}']


def run(account, argv, expected=0, label=None, extra_env=None):
    home = pwd.getpwnam(account).pw_dir
    cmd = ['runuser', '-u', account, '--', 'env', '-i', f'HOME={home}', 'PATH=/usr/local/bin:/usr/bin:/bin']
    if extra_env:
        cmd += [f'{k}={v}' for k, v in extra_env.items()]
    cmd += list(map(str, argv))
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=180)
    index = len(commands) + 1
    label = label or f'command-{index}'
    output = evidence / f'{index:03}-{label}.txt'
    output.write_text(result.stdout + result.stderr)
    commands.append({'principal': account, 'argv': cmd, 'expected': expected, 'exitCode': result.returncode, 'output': output.name})
    (evidence / 'commands.json').write_text(json.dumps(commands, indent=2) + '\n')
    if (expected == 0 and result.returncode != 0) or (expected == 'nonzero' and result.returncode == 0):
        raise AssertionError(f'{label}: exit {result.returncode}, expected {expected}: {result.stdout}{result.stderr}')
    return result.stdout


def request(command, *args, expected=0, label=None, extra_env=None):
    return run(operator, [binary, 'request', command, '--registry', registry, '--state', state, *args], expected, label, extra_env)


def remote(command):
    return run(operator, [*ssh, command])


def check(name):
    cases.append(name)
    print(f'PASS {name}', flush=True)


def owned(path, data, account=operator, group=None, mode=0o600):
    path = Path(path)
    path.write_bytes(data if isinstance(data, bytes) else data.encode())
    info = pwd.getpwnam(account)
    os.chown(path, info.pw_uid, group if group is not None else info.pw_gid)
    path.chmod(mode)


def load(text):
    return json.loads(text)


def submit(identity, environment='lab', revision='mediated-v1', principal=agent):
    data = {'schemaVersion': 'provision.dev/operation-request/v1alpha1', 'id': identity, 'application': 'provision-example-http', 'environment': environment, 'revision': revision, 'component': 'web', 'operation': 'stageArtifact'}
    source = Path(pwd.getpwnam(principal).pw_dir) / f'{identity}.json'
    owned(source, json.dumps(data, indent=2), principal)
    run(principal, [binary, 'request', 'submit', '--inbox', root / 'inbox' / principal, '--file', source])
    snapshot = load(request('import', '--requester', principal, '--name', f'{identity}.json'))
    return snapshot['capture']['id']


def plan(identity):
    snapshot = load(request('plan', '--request', identity))
    reviewed = load(request('review', '--request', identity))
    assert snapshot['planId'] == reviewed['plan']['id']
    assert reviewed['request']['operationId'] == 'op-01'
    assert reviewed['plan']['operations'][1]['kind'] == 'installGeneration'
    return snapshot


def approve(identity, snapshot, expiry='2m'):
    return load(request('approve', '--request', identity, '--plan', snapshot['planId'], '--operation', snapshot['operationId'], '--actor', operator, '--expires-after', expiry))


def execute(identity, expected=0, command='execute', extra_env=None):
    return request(command, '--request', identity, '--signing-key', signing, expected=expected, extra_env=extra_env)


def host_state():
    return remote("sudo -n /usr/local/libexec/provision-host-executor inspect --environment lab --operator " + a.target_user)


def reset_artifact():
    admin_ssh = ssh[:-1] + [f"marlinf@{a.target}", f"sudo -n rm -- /var/lib/provision/artifacts/sha256/{artifact_digest[7:]}"]
    return run("root", admin_ssh)


def cache_digest():
    return remote('find /var/lib/provision/artifacts/sha256 -maxdepth 1 -type f -printf "%f %s\\n" | sort')


assert remote('hostname').strip() == a.confirm_disposable_host
initial_host = host_state()
(evidence / 'host-before.json').write_text(initial_host)
initial_cache = cache_digest()
initial_host_json = load(initial_host)
assert not initial_cache.strip(), 'requires an empty disposable Artifact cache'

# Controlled HTTPS origin supplies byte-identical data and deterministic failure/expiry timing.
server_root = private / 'origin'
server_root.mkdir()
fixture = b'mediated stageArtifact acceptance\n'
artifact_digest = 'sha256:' + hashlib.sha256(fixture).hexdigest()
(server_root / 'artifact.tar.gz').write_bytes(fixture)
(server_root / 'bad.tar.gz').write_bytes(b'wrong digest\n')
(server_root / 'slow.tar.gz').write_bytes(fixture)
(server_root / 'received').unlink(missing_ok=True)


class Origin(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(server_root), **kwargs)

    def do_GET(self):
        if self.path == '/slow.tar.gz':
            (server_root / 'received').write_text(str(time.time()))
            time.sleep(10)
        super().do_GET()

    def log_message(self, *_):
        pass


server = http.server.ThreadingHTTPServer(('0.0.0.0', 8443), Origin)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(private / 'origin.crt', private / 'origin.key')
server.socket = context.wrap_socket(server.socket, server_side=True)
threading.Thread(target=server.serve_forever, daemon=True).start()

registrations = {}
for principal in (agent, other):
    info = pwd.getpwnam(principal)
    registrations[principal] = {'account': principal, 'uid': info.pw_uid, 'gid': info.pw_gid, 'inbox': str(root / 'inbox' / principal), 'results': str(root / 'results' / principal), 'grants': [{'application': 'provision-example-http', 'environment': 'lab', 'operations': ['stageArtifact']}]}
registration = {'schemaVersion': 'provision.dev/request-registry/v1alpha1', 'operator': operator, 'requesters': registrations, 'configurations': []}
owned(registry, json.dumps(registration))
config_dir = private / 'configuration'
config_dir.mkdir();shutil.chown(config_dir, operator)
fixtures = Path('/opt/provision/testdata/direct-local-matrix')
owned(config_dir / 'root.yaml', (fixtures / 'root.yaml').read_bytes())
owned(config_dir / 'application.yaml', (fixtures / 'application.yaml').read_bytes())
environment = (fixtures / 'environment-remote.yaml.tmpl').read_text().replace('@OPERATOR@', a.target_user).replace('@ADDRESS@', a.target)
owned(config_dir / 'environment.yaml', environment)


def register(revision, source):
    revision_text = f'''schemaVersion: provision.dev/v1alpha1
kind: Revision
name: {revision}
application: provision-example-http
artifacts:
  web:
    source: https://{a.controller_address}:8443/{source}
    digest: {artifact_digest}
'''
    owned(config_dir / 'revision.yaml', revision_text)
    request('register', '--file', config_dir / 'root.yaml')


register('mediated-v1', 'artifact.tar.gz')
register('mediated-failure', 'bad.tar.gz')
register('mediated-slow', 'slow.tar.gz')

outscope = submit('out-of-scope', environment='forbidden')
assert load(request('status', '--request', outscope))['status'] == 'scope-or-registration-denied'
request('plan', '--request', outscope, expected='nonzero')
assert cache_digest() == initial_cache
assert load(host_state()) == initial_host_json
check('out-of-scope denied before planning or approval; no Host mutation')

register('mediated-v1', 'artifact.tar.gz')
ordinary_plan = load(run(operator, [binary, 'plan', 'preview', '--file', config_dir / 'root.yaml', '--state', state]))
run(operator, [binary, 'plan', 'approve', '--file', config_dir / 'root.yaml', '--plan', ordinary_plan['id'], '--actor', operator, '--state', state])
identity = submit('approved-stage')
snapshot = plan(identity)
assert snapshot['planId'] == ordinary_plan['id']
execute(identity, expected='nonzero')
assert cache_digest() == initial_cache
assert load(host_state()) == initial_host_json
check('in-scope request without mediated approval cannot dispatch')

for argv in (["cat", signing], ['cat', Path(pwd.getpwnam(operator).pw_dir) / '.ssh/id_ed25519'], ['cat', state], ['sh', '-c', f'echo compromised >> {registry}'], ['sh', '-c', f'echo compromised >> {state}'], ['sh', '-c', f'echo compromised >> {config_dir / "environment.yaml"}'], ['sudo', '-n', '-u', operator, binary, 'request', 'approve']):
    run(agent, argv, expected='nonzero')
run(agent, [binary, 'request', 'approve', '--registry', registry, '--state', state, '--request', identity, '--plan', snapshot['planId'], '--operation', 'op-01', '--actor', operator], expected='nonzero')
run(agent, ['ssh', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=no', '-o', 'UserKnownHostsFile=/dev/null', '-o', 'IdentityAgent=none', '-o', 'IdentitiesOnly=yes', '-o', 'IdentityFile=/nonexistent', '-o', 'ConnectTimeout=5', f'{a.target_user}@{a.target}', 'sudo -n /usr/local/libexec/provision-host-executor execute --environment lab --operator ' + a.target_user], expected='nonzero')
run(agent, ['test', '!', '-w', binary])
trusted_process = subprocess.Popen(['runuser', '-u', operator, '--', 'sleep', '30'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    time.sleep(0.1)
    pid = run('root', ['pgrep', '-u', operator, '-x', 'sleep']).strip().splitlines()[0]
    run(agent, ['kill', '-0', pid], expected='nonzero')
finally:
    trusted_process.terminate()
    trusted_process.wait(timeout=5)
assert cache_digest() == initial_cache
check('requester cannot read private state/keys, change registration, impersonate approver or reach executor')

# Filesystem attacks run under the actual requester identity, not the supervisor.
inbox = root / 'inbox' / agent
run(agent, ['ln', '-s', str(registry), str(inbox / 'symlink.json')])
request('import', '--requester', agent, '--name', 'symlink.json', expected='nonzero')
run(agent, ['ln', str(Path(pwd.getpwnam(agent).pw_dir) / 'approved-stage.json'), str(inbox / 'hardlink.json')])
request('import', '--requester', agent, '--name', 'hardlink.json', expected='nonzero')
request('import', '--requester', agent, '--name', '../private/registry.json', expected='nonzero')
owned(inbox / 'foreign.json', '{}')
request('import', '--requester', agent, '--name', 'foreign.json', expected='nonzero')
check('real-account importer rejects symlink, hardlink, traversal and foreign ownership')

# Same configured grant is required again at dispatch, even after human approval.
approved = approve(identity, snapshot)
saved_registry = registry.read_bytes()
changed_registry = json.loads(saved_registry)
changed_registry['requesters'][agent]['grants'] = []
owned(registry, json.dumps(changed_registry))
execute(identity, expected='nonzero')
owned(registry, saved_registry)
assert cache_digest() == initial_cache
check('revoked requester grant blocks an already approved dispatch')

# A mutable source YAML or submission cannot replace the trusted capture.
owned(config_dir / 'revision.yaml', 'changed original file\n')
owned(inbox / 'approved-stage.json', '{"operation":"installGeneration"}', agent)
assert load(request('review', '--request', identity))['request']['capture']['configuration']['revision']['artifacts']['web']['digest'] == artifact_digest
result = load(execute(identity))
assert result['status'] == 'succeeded' and len(result['attempts']) == 1
assert remote(f'sha256sum /var/lib/provision/artifacts/sha256/{artifact_digest[7:]}').split()[0] == artifact_digest[7:]
check('approved stage succeeds with captured inputs and matching Host bytes')

for operation in ('op-01', 'op-02'):
    run(operator, [binary, 'deployment', 'execute', '--plan', snapshot['planId'], '--operation', operation, '--state', state, '--signing-key', signing], expected='nonzero')
run(operator, [binary, 'deployment', 'resume', '--plan', snapshot['planId'], '--state', state, '--signing-key', signing], expected='nonzero')
assert len(load(request('status', '--request', identity))['attempts']) == 1
check('generic execution and recovery cannot use mediated approval for installation or repeat staging')

# Restore identical bytes and re-import the completed submission: the recorded outcome wins.
owned(inbox / 'approved-stage.json', (Path(pwd.getpwnam(agent).pw_dir) / 'approved-stage.json').read_bytes(), agent, mode=0o640)
assert load(request('import', '--requester', agent, '--name', 'approved-stage.json'))['capture']['id'] == identity
assert len(load(execute(identity))['attempts']) == 1
check('completed duplicate returns recorded outcome without another attempt')

own_result = root / 'results' / agent / f'{identity[7:]}.json'
published = load(run(agent, ['cat', own_result]))
assert published == load(request('status', '--request', identity))
run(agent, ['sh', '-c', f'echo altered >> {own_result}'], expected='nonzero')
other_id = submit('other-requester', environment='forbidden', principal=other)
run(agent, ['cat', root / 'results' / other / f'{other_id[7:]}.json'], expected='nonzero')
assert 'configuration' not in published and 'observation' not in published and 'source' not in published
check('own sanitized results are readable and immutable; another requester results are inaccessible')

# Expired approval permits observation but no new dispatch.
expired = submit('expiry')
expired_snapshot = plan(expired)
approve(expired, expired_snapshot, '1s')
time.sleep(2)
execute(expired, expected='nonzero')
assert load(request('observe', '--request', expired))['observation']['State'] == 'satisfied'
assert load(request('status', '--request', expired))['status'] == 'expired'
check('expired approval blocks dispatch but allows read-only observation')

# Remove only the test's digest from this disposable Host to exercise actual staging again.
reset_artifact()
slow = submit('expiry-after-dispatch', revision='mediated-slow')
slow_snapshot = plan(slow)
slow_approval = approve(slow, slow_snapshot, '6s')['approval']
slow_result = load(execute(slow))
assert slow_result['status'] == 'succeeded'
from datetime import datetime
assert datetime.fromisoformat(slow_result['attempts'][-1]['occurredAt'].replace('Z', '+00:00')) > datetime.fromisoformat(slow_approval['expiresAt'].replace('Z', '+00:00'))
check('bounded dispatched work finishes and journals after approval expiry')

reset_artifact()
failed = submit('known-failure', revision='mediated-failure')
failed_snapshot = plan(failed)
approve(failed, failed_snapshot)
execute(failed, expected='nonzero')
assert load(request('status', '--request', failed))['status'] == 'failed'
execute(failed, expected='nonzero')
assert len(load(request('status', '--request', failed))['attempts']) == 1
(server_root / 'bad.tar.gz').write_bytes(fixture)
approve(failed, failed_snapshot)
assert load(execute(failed))['status'] == 'succeeded'
check('known failure cannot retry until a fresh explicit human decision')

reset_artifact()
interrupted = submit('uncertain-stage')
interrupted_snapshot = plan(interrupted)
approve(interrupted, interrupted_snapshot)
marker = private / 'lost-response'
fault_env = {'PATH': '/opt/provision/scripts/remote-ssh-fault-bin:/usr/local/bin:/usr/bin:/bin', 'PROVISION_REAL_SSH': '/usr/bin/ssh', 'PROVISION_FAULT_MODE': 'uncertain', 'PROVISION_FAULT_MARKER': str(marker)}
execute(interrupted, expected='nonzero', extra_env=fault_env)
assert load(request('status', '--request', interrupted))['status'] == 'uncertain'
execute(interrupted, expected='nonzero')
resumed = load(execute(interrupted, command='resume'))
assert resumed['status'] == 'succeeded' and len(resumed['attempts']) == 2
journal = load(run(operator, [binary, 'deployment', 'status', '--plan', interrupted_snapshot['planId'], '--state', state]))
ours = [e for e in journal['events'] if e['attemptId'] in {x['attemptId'] for x in resumed['attempts']}]
assert ours[-2]['observation']['resumeOfAttemptId'] == resumed['attempts'][0]['attemptId']
assert all(e['operationId'] == 'op-01' for e in ours)
check('lost response and observation preserve fencing and resume only the bound operation')

# No further mutation under expired approval, including a retry of uncertain work.
reset_artifact()
uncertain_expired = submit('expired-uncertain')
ue_snapshot = plan(uncertain_expired)
approve(uncertain_expired, ue_snapshot, '3s')
marker.unlink(missing_ok=True)
execute(uncertain_expired, expected='nonzero', extra_env=fault_env)
time.sleep(4)
execute(uncertain_expired, expected='nonzero', command='resume')
request('observe', '--request', uncertain_expired)
assert len(load(request('status', '--request', uncertain_expired))['attempts']) == 1
check('expired approval blocks uncertain redispatch while observation remains available')

final_host = load(host_state())
assert final_host['deployment'] == initial_host_json['deployment'], 'staging changed application authority'
assert remote("find /etc/systemd/system -maxdepth 1 -name 'provision-lab-web-*' -printf '%f\\n'").strip() == ''
assert remote('find /var/lib/provision/environments/lab/releases -mindepth 1 -maxdepth 1 -printf "%f\\n"').strip() == ''
(evidence / 'host-after.json').write_text(json.dumps(final_host, indent=2) + '\n')
check('no installation, service activation or Endpoint mutation in any staging request')

summary = {'schemaVersion': 'provision.dev/mediated-stage-acceptance/v1alpha1', 'result': 'passed', 'sourceVersion': a.provision_version, 'controller': {'hostname': os.uname().nodename, 'kernel': os.uname().release, 'os': Path('/etc/os-release').read_text(), 'binarySha256': 'sha256:' + hashlib.sha256(Path(binary).read_bytes()).hexdigest(), 'accounts': {x: {'uid': pwd.getpwnam(x).pw_uid, 'gid': pwd.getpwnam(x).pw_gid} for x in (operator, agent, other)}}, 'target': {'address': a.target, 'hostname': a.confirm_disposable_host, 'operator': a.target_user, 'os': final_host['os'], 'osVersion': final_host['osVersion'], 'executorDigest': final_host['executorDigest'], 'authorityKeyId': final_host['authorityKeyId'], 'sshHostKeyFingerprint': final_host['sshHostKeyFingerprint']}, 'artifactDigest': artifact_digest, 'cases': cases, 'commands': len(commands), 'allExpectedExitCodesMatched': True}
(evidence / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
server.shutdown()
print(f'All {len(cases)} isolated-account remote-Host cases passed; evidence: {evidence}', flush=True)
