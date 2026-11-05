#!/usr/bin/env bash
# Kubernetes smoke test (docs/deploy/kubernetes.md): a throwaway kind cluster with the image built
# from the checkout loaded into it and the CRDs of the compatibility matrix (design J.18) applied,
# Gateway API v1 (standard channel) and Flux v2, then both install paths, each in its own namespace
# under the restricted Pod Security Standard:
#   - helm install of charts/dash with the actions value set (patch RBAC and the guardrail
#     policy), checking with server-side dry runs as dash's ServiceAccount that the policy admits
#     the restart patch and the flux plugin's reconcile request on a real Kustomization, and
#     denies everything else it is there for;
#   - kubectl apply -k of examples/kubernetes, through an overlay that only swaps in the image.
# Each must reach ready and pass `dash healthcheck` in the pod. Everything it creates (the
# cluster, its Docker network, the image, the scratch directory) is removed on exit.
#
#   scripts/ci/kind-smoke.sh [image]     # default: build dash:kind-<name> from the checkout
#
# Needs docker (with buildx), kind, kubectl and helm (mise.toml pins them), curl and openssl.
# KIND_NODE_IMAGE overrides the node image (CI pins one per Kubernetes minor, by digest).
# DASH_KIND_NAME names the cluster, its network and the built image (dash:kind-<name>), so CI can
# remove them in a step of its own when the job is cancelled; default dash-kind-<pid>-<random>.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
image=${1:-}
name=${DASH_KIND_NAME:-dash-kind-$$-$RANDOM}
node=${KIND_NODE_IMAGE:-kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}
work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/dash-kind.XXXXXX")
export KUBECONFIG=$work/kubeconfig
# A Docker network of the cluster's own instead of kind's shared "kind" network, removed on exit.
export KIND_EXPERIMENTAL_DOCKER_NETWORK=$name

# The release manifests the CRDs come from, pinned by sha256. Only Flux's CRDs are applied, not
# its controllers: dash lists Flux objects and patches their reconcile annotation, and the API
# server, not a controller, decides both.
GATEWAY_API_VERSION=1.7.0
GATEWAY_API_SHA256=4e2602c4cbe1afbc394d86f4ac6522e1a5263b01133bba1e40a6d1a549e753a1
FLUX_VERSION=2.10.0
FLUX_SHA256=a8c838ccd9be5d30eba9df87cd229b7c4eb80c0049fba1bc3b9840829f267f28

built=
cluster=

say() { printf '%s kind: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { say "FAIL: $*"; exit 1; }

cleanup() {
	status=$?
	if [[ -n $cluster ]]; then
		if ((status != 0)); then
			say "state at failure (the setup token is redacted):"
			kubectl get pods -A -o wide 2>&1 | tail -n 30 || true
			for ns in dash-helm dash; do
				kubectl -n "$ns" describe pods 2>&1 | tail -n 40 || true
				kubectl -n "$ns" logs deploy/dash -c dash --tail=40 2>&1 |
					sed -E 's/dash_setup_[A-Za-z0-9_-]+/dash_setup_[redacted]/g' || true
			done
		fi
		kind delete cluster --name "$name" >/dev/null 2>&1 || true
		docker network rm "$name" >/dev/null 2>&1 || true
		say "deleted cluster $name and its network"
	fi
	if [[ -n $built ]]; then
		docker image rm "$built" >/dev/null 2>&1 || true
		say "removed image $built"
	fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT

if [[ -z $image ]]; then
	built=dash:kind-$name
	image=$built
	say "building $image"
	docker buildx build --load --quiet --tag "$image" "$root" >/dev/null
fi
repository=${image%:*}
tag=${image##*:}

cluster=1
say "creating cluster $name ($node)"
kind create cluster --quiet --name "$name" --image "$node" --wait 120s
kind load docker-image --quiet "$image" --name "$name"
say "loaded $image"

# manifest URL SHA256 FILE: downloads URL to FILE and checks its sha256.
manifest() {
	curl -fsSL --retry 3 -o "$3" "$1"
	[[ $(sha256sum "$3" | cut -d' ' -f1) == "$2" ]] || fail "$1 does not have sha256 $2"
}
manifest "https://github.com/kubernetes-sigs/gateway-api/releases/download/v$GATEWAY_API_VERSION/standard-install.yaml" \
	"$GATEWAY_API_SHA256" "$work/gateway-api.yaml"
manifest "https://github.com/fluxcd/flux2/releases/download/v$FLUX_VERSION/install.yaml" \
	"$FLUX_SHA256" "$work/flux.yaml"
kubectl apply --server-side -f "$work/gateway-api.yaml" >/dev/null
# The CustomResourceDefinition documents of Flux's install manifest.
awk 'BEGIN { RS = "\n---\n" } /(^|\n)kind: CustomResourceDefinition(\n|$)/ { printf "---\n%s\n", $0 }' \
	"$work/flux.yaml" | kubectl apply --server-side -f - >/dev/null
kubectl wait --for=condition=Established crd --all --timeout=60s >/dev/null
kubectl get crd kustomizations.kustomize.toolkit.fluxcd.io httproutes.gateway.networking.k8s.io >/dev/null
say "applied the Gateway API v$GATEWAY_API_VERSION standard and Flux v$FLUX_VERSION CRDs"

# A namespace under the restricted Pod Security Standard, with dash's secret key, which reaches
# kubectl on stdin, never in an argv.
namespace() {
	kubectl create namespace "$1" >/dev/null
	kubectl label namespace "$1" pod-security.kubernetes.io/enforce=restricted \
		pod-security.kubernetes.io/warn=restricted >/dev/null
	openssl rand -base64 32 |
		kubectl -n "$1" create secret generic dash-secret-key --from-file=secretKey=/dev/stdin >/dev/null
}

# Waits for the Deployment dash of a namespace and runs `dash healthcheck` in its pod.
ready() {
	local start=$SECONDS
	kubectl -n "$1" rollout status deploy/dash --timeout=180s >/dev/null || fail "$1: dash not ready within 180s"
	say "$1: ready after $((SECONDS - start))s"
	kubectl -n "$1" exec deploy/dash -c dash -- /dash healthcheck || fail "$1: dash healthcheck failed"
	say "$1: dash healthcheck passed"
}

say "helm install charts/dash (actions value set) into dash-helm"
namespace dash-helm
helm install dash "$root/charts/dash" --namespace dash-helm \
	-f "$root/charts/dash/ci/actions-values.yaml" \
	--set image.repository="$repository" --set image.tag="$tag" >/dev/null
ready dash-helm

# The guardrail, through server-side dry runs as dash's ServiceAccount (they pass admission and
# change nothing). A probe Role grants what the chart does not (scale, delete), so those denials
# too must come from the policy, not from RBAC.
sa=(--as=system:serviceaccount:dash-helm:dash -n dash-helm --dry-run=server)
kubectl -n dash-helm create role guardrail-probe --verb=get,patch,update --resource=deployments/scale >/dev/null
kubectl -n dash-helm create role guardrail-probe-delete --verb=delete --resource=deployments >/dev/null
for role in guardrail-probe guardrail-probe-delete; do
	kubectl -n dash-helm create rolebinding "$role" --role="$role" --serviceaccount=dash-helm:dash >/dev/null
done
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl "${sa[@]}" patch deploy/dash \
	-p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"kubectl.kubernetes.io/restartedAt\":\"$now\"}}}}}" >/dev/null ||
	fail "the guardrail denied the restart it should admit"
denied() { # denied DESCRIPTION kubectl-args...: the request must fail on the guardrail policy
	local what=$1 out
	shift
	if out=$(kubectl "${sa[@]}" "$@" 2>&1); then
		fail "dash's ServiceAccount could $what"
	fi
	[[ $out == *"ValidatingAdmissionPolicy"* ]] || fail "$what was refused, but not by the guardrail: $out"
	say "guardrail: $what denied"
}
denied "scale through the scale subresource" patch deploy/dash --subresource=scale -p '{"spec":{"replicas":3}}'
denied "change replicas" patch deploy/dash -p '{"spec":{"replicas":3}}'
denied "add a label" patch deploy/dash -p '{"metadata":{"labels":{"probe":"x"}}}'
denied "add another template annotation" patch deploy/dash -p '{"spec":{"template":{"metadata":{"annotations":{"probe":"x"}}}}}'
denied "set the DaemonSet generation annotation on a Deployment" \
	patch deploy/dash -p '{"metadata":{"annotations":{"deprecated.daemonset.template.generation":"9"}}}'
denied "delete its Deployment" delete deploy/dash
say "guardrail: restart admitted"

# The flux plugin's reconcile action on a real Kustomization: its merge patch (the requestedAt
# annotation and nothing else) is admitted; a change to the spec or to another annotation is not.
kubectl -n dash-helm apply -f - >/dev/null <<EOF
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: probe
spec:
  interval: 10m
  path: ./probe
  prune: false
  sourceRef:
    kind: GitRepository
    name: probe
EOF
kubectl "${sa[@]}" patch kustomization probe --type=merge \
	-p "{\"metadata\":{\"annotations\":{\"reconcile.fluxcd.io/requestedAt\":\"$now\"}}}" >/dev/null ||
	fail "the guardrail denied the reconcile request it should admit"
denied "change a Kustomization's spec" patch kustomization probe --type=merge -p '{"spec":{"suspend":true}}'
denied "add another annotation to a Kustomization" \
	patch kustomization probe --type=merge -p '{"metadata":{"annotations":{"probe":"x"}}}'
say "guardrail: reconcile admitted"

say "kubectl apply -k examples/kubernetes into dash"
namespace dash
mkdir "$work/overlay"
# kustomize takes the base by relative path only.
cat >"$work/overlay/kustomization.yaml" <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - $(realpath --relative-to="$work/overlay" "$root/examples/kubernetes")
images:
  - name: ghcr.io/techdufus/dash
    newName: $repository
    newTag: "$tag"
EOF
kubectl apply -k "$work/overlay" >/dev/null
ready dash
say "PASS"
