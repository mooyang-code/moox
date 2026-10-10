#!/usr/bin/env bash
set -euo pipefail

[[ "$(uname -s)" == Linux ]] || { echo 'host bundle gate must execute on Linux' >&2; exit 1; }
for variable in MOOX_UNIT_BUNDLE_TEST_BINARY MOOX_ADMIN_CLI_BINARY MOOX_RUNTIME_BINARY; do
  [[ -n "${!variable:-}" && "${!variable}" == /* && -f "${!variable}" && -x "${!variable}" ]] || {
    echo "${variable} must select an existing prebuilt absolute executable" >&2
    exit 1
  }
done
task_root="$(mktemp -d /tmp/moox-unit-bundle-gate.XXXXXXXX)"
trap 'rm -rf "${task_root}"' EXIT
# An optional caller-owned private directory retains this synthetic fixture
# for a subsequent native SSH download test. No production files are used.
fixture_root="${MOOX_HOST_MATERIAL_FIXTURE_ROOT:-${task_root}/fixture}"
[[ "${fixture_root}" == /* && ! -e "${fixture_root}" ]] || { echo 'fixture root must be a new absolute directory' >&2; exit 1; }
export MOOX_HOST_MATERIAL_FIXTURE="${fixture_root}/fixtures.json" MOOX_RUNTIME_BINARY
python3 - "${fixture_root}" "${MOOX_ADMIN_CLI_BINARY}" <<'PY'
import json,os,pathlib,subprocess,sys
root=pathlib.Path(sys.argv[1]); root.mkdir(mode=0o700)
hosts=[
    dict(host_id='control',address='192.0.2.1',private_address='control.internal.example.test',components=['admin','console-proxy','web-host','collector']),
    dict(host_id='storage',address='192.0.2.2',private_address='10.0.0.2',components=['storage-primary','storage-node','storage-view']),
    dict(host_id='compute1',address='192.0.2.3',private_address='10.0.0.3',components=['access','egress-proxy','trade']),
]
topology=root/'topology.json'
topology.write_text(json.dumps(dict(version=1,control_host_id='control',hosts=hosts)));topology.chmod(0o600)
common=['--db-path',str(root/'data/admin.db'),'--encryption-key-file',str(root/'secrets/admin.key'),'--pki-dir',str(root/'secrets/pki'),'--output-dir',str(root/'bundles')]
def invoke(command):
    result=subprocess.run([sys.argv[2]]+command+common,capture_output=True,check=False)
    if result.returncode: raise SystemExit('isolated Admin fixture command failed')
    return json.loads(result.stdout)
bootstrap=invoke(['bootstrap','--topology-file',str(topology)])
repeat=invoke(['bootstrap','--topology-file',str(topology)])
assert bootstrap['ca']['sha256']==repeat['ca']['sha256']
assert bootstrap['certificate']['sha256']!=repeat['certificate']['sha256']
assert bootstrap['credentials']==repeat['credentials']
fixtures=[]
def add(host,metadata,operator):
    fixtures.append(dict(metadata=metadata,options=dict(HostID=host['host_id'],ControlHostID='control',Address=host['address'],PrivateAddress=host['private_address'],ControlAddress=hosts[0]['address'],Components=host['components'],ExpectedCA=bootstrap['ca']['sha256'],ExpectedHash=metadata['expected_hash'],AllowOperator=operator)))
add(hosts[0],bootstrap,True)
for host in hosts:
    metadata=invoke(['host-bundle','--host-id',host['host_id'],'--control-host-id','control'])
    assert metadata['ca']['sha256']==bootstrap['ca']['sha256']
    assert not any(c['caller']=='moox-cli' for c in metadata['credentials'])
    add(host,metadata,False)
out=root/'fixtures.json';out.write_text(json.dumps(fixtures)+'\n');out.chmod(0o600)
print('isolated real Admin producer: bootstrap, repeat and three host bundles prepared')
PY
"${MOOX_UNIT_BUNDLE_TEST_BINARY}" -test.v -test.count=1 -test.timeout=6m | tee "${task_root}/tests.log"
python3 - "${task_root}/tests.log" <<'PY'
from pathlib import Path
import re,sys
log=Path(sys.argv[1]).read_text()
required=[
    'TestHostMaterialSeparatesTargetAndOperatorIdentities',
    'TestHostMaterialRejectsWrongTopologyAndTrust',
    'TestHostMaterialRejectsInvalidContentsWithMatchingInventory',
    'TestHostMaterialRejectsAccessIdentityEscapes',
    'TestHostMaterialRejectsUnsafeFilesAndNoncanonicalJSON',
    'TestHostMaterialFetchPinsAndBoundsEveryTransfer',
    'TestHostMaterialPublicationPreservesExistingObjectsAndConcurrentWinner',
    'TestHostMaterialConsumesActualAdminProducer',
]
for name in required:
    if not re.search(r'^--- PASS: '+re.escape(name)+r' \(',log,re.M):
        raise SystemExit('required host bundle scenario did not pass: '+name)
if re.search(r'^[ \t]*--- (SKIP|FAIL):|^FAIL$',log,re.M):
    raise SystemExit('host bundle Linux gate must not skip or fail a scenario')
print('host bundle Linux gate passed: 8 required consumer/real-producer scenarios, no compilation')
PY
