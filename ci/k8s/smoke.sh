#!/usr/bin/env bash
# Smoke test of the Kubernetes manifests in k8s/: deploys them, through the
# overlay ci/k8s, on a single-node kind cluster, and checks simota over its API
# Service and across a restart.
#
# Needs docker, kind, kubectl, kubeconform, curl and jq. Environment:
#   IMAGE  the image under test, in the local Docker (simota:latest)
#   KEEP   set to keep the cluster afterwards
set -euo pipefail

IMAGE="${IMAGE:-simota:latest}"

CLUSTER=simota-smoke
NODE="${CLUSTER}-control-plane"
NS=ella
ICCID=8949440000001659208

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Where this host reaches the API, set once simota is deployed.
API=""

k() { kubectl -n "${NS}" "$@"; }

step() { echo "::group::$*"; }
endstep() { echo "::endgroup::"; }

fail() {
  echo "::error::$*"
  exit 1
}

cleanup() {
  status=$?
  if [ "${status}" -ne 0 ]; then
    echo "== Diagnostics"
    k get all,pvc -o wide || true
    k describe pod simota-0 || true
    k logs simota-0 --tail=100 || true
    k logs simota-0 --previous --tail=100 2>/dev/null || true
  fi
  if [ -z "${KEEP:-}" ]; then
    kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
  fi
  exit "${status}"
}
trap cleanup EXIT

api() { curl -fsS --max-time 10 "${API}/api/v1$1" "${@:2}"; }

# Waits for simota-0 to be ready, not being deleted, and another pod than the
# one of UID $1, if given: the replacement of a deleted pod has its name.
wait_ready() {
  k rollout status statefulset/simota --timeout=180s
  local deadline=$((SECONDS + 120)) uid deleting ready
  while [ "${SECONDS}" -lt "${deadline}" ]; do
    IFS='|' read -r uid deleting ready < <(k get pod simota-0 \
      -o jsonpath='{.metadata.uid}|{.metadata.deletionTimestamp}|{.status.conditions[?(@.type=="Ready")].status}{"\n"}' 2>/dev/null || echo '||')
    if [ -n "${uid}" ] && [ "${uid}" != "${1:-}" ] && [ -z "${deleting}" ] && [ "${ready}" = True ]; then
      return 0
    fi
    sleep 1
  done
  fail "simota-0 did not become ready"
}

# Waits for the API to answer through the Service.
wait_api() {
  for _ in $(seq 30); do
    api /status >/dev/null 2>&1 && return 0
    sleep 1
  done
  fail "the API does not answer at ${API}"
}

step "Lint the manifests"
for dir in k8s ci/k8s; do
  kubectl kustomize "${ROOT}/${dir}" | kubeconform -strict -summary
done
endstep

step "Create the cluster"
kind create cluster --name "${CLUSTER}" --wait 120s
endstep

step "Deploy ${IMAGE}"
docker tag "${IMAGE}" simota:ci
kind load docker-image simota:ci --name "${CLUSTER}"
kubectl apply -k "${ROOT}/ci/k8s"
wait_ready
node_ip="$(kubectl get node "${NODE}" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')"
port="$(k get service simota -o jsonpath='{.spec.ports[?(@.name=="http")].nodePort}')"
API="http://${node_ip}:${port}"
wait_api
endstep

step "Check the pod"
sleep 30
restarts="$(k get pod simota-0 -o jsonpath='{.status.containerStatuses[0].restartCount}')"
[ "${restarts}" = 0 ] || fail "simota restarted ${restarts} times"
k get pod simota-0 -o wide
endstep

step "Check the API and the UI through the Service"
version="$(sed -n 's/^\tversion = "\(.*\)"$/\1/p' "${ROOT}/cmd/simota/main.go")"
running="$(api /status | jq -r .result.version)"
[ "${running}" = "${version}" ] || fail "simota reports version ${running}, want ${version}"
curl -fsS --max-time 10 "${API}/" | grep -q '<title>' || fail "the UI is not served at ${API}"
echo "version ${running} at ${API}"
endstep

step "Keep a card across a restart"
api /cards -X POST -H 'Content-Type: application/json' -d "$(jq -n --arg iccid "${ICCID}" '{
  iccid: $iccid, msisdn: "+15550000001", description: "smoke test", tar: "b00011", kvn: 1,
  key_sets: [{version: 1, algorithm: "3des", kic: "00112233445566778899aabbccddeeff", kid: "00112233445566778899aabbccddeeff"}]
}')" >/dev/null
old="$(k get pod simota-0 -o jsonpath='{.metadata.uid}')"
k rollout restart statefulset/simota
wait_ready "${old}"
wait_api
api "/cards/${ICCID}" | jq -e --arg iccid "${ICCID}" '.result.iccid == $iccid' >/dev/null \
  || fail "the card stored before the restart is gone"
echo "card ${ICCID} kept"
endstep
