#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-mutation-lock-test.XXXXXX")
test_workspace=$(cd "$test_workspace" && pwd -P)
trap 'rm -rf "$test_workspace"' EXIT

mutation_log="${test_workspace}/mutations.log"
repository_root="${test_workspace}/repository"
mkdir -m 0700 "$repository_root"

ATLAS_TEST_MUTATION_LOG=$mutation_log bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  source bootstrap/lib/lock.sh
  source bootstrap/cluster/kind.sh
  source bootstrap/registry/local.sh
  source bootstrap/argocd/seed.sh
  source bootstrap/argocd/handoff.sh

  ATLAS_ROOT_DIR=$1
  ATLAS_STATE_DIR="${ATLAS_ROOT_DIR}/.state"
  mkdir -m 0700 "${ATLAS_ROOT_DIR}/gitops" "$ATLAS_STATE_DIR"
  mkdir -m 0700 "${ATLAS_STATE_DIR}/rendered"
  printf "seed\n" > "${ATLAS_STATE_DIR}/rendered/argocd-seed.yaml"
  printf "root\n" > "${ATLAS_STATE_DIR}/rendered/root-app.yaml"
  chmod 0600 "${ATLAS_STATE_DIR}/rendered/argocd-seed.yaml" "${ATLAS_STATE_DIR}/rendered/root-app.yaml"

  config::get() {
    case "$1" in
      ATLAS_CLUSTER_NAME) printf "atlas-lock-test\n" ;;
      ATLAS_KIND_CONFIG) printf "kind.yaml\n" ;;
      ATLAS_READY_TIMEOUT) printf "1s\n" ;;
      ATLAS_REGISTRY_NAME) printf "atlas-lock-registry\n" ;;
      ATLAS_REGISTRY_HOST) printf "registry.local\n" ;;
      ATLAS_REGISTRY_PORT) printf "5001\n" ;;
      ATLAS_ROOT_NAME) printf "root-app\n" ;;
      ATLAS_GIT_REPO_URL) printf "https://github.com/snkio027/atlas.git\n" ;;
      *) return 1 ;;
    esac
  }
  config::version() {
    case "$1" in
      KIND_NODE_IMAGE) printf "kindest/node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" ;;
      REGISTRY_IMAGE) printf "registry@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" ;;
      *) return 1 ;;
    esac
  }
  cluster::_parse_kind_node_roles() { :; }
  runtime::assert_docker_authority() { :; }
  runtime::docker_image_present() { :; }
  runtime::kind_cluster_exists() { return 1; }
  runtime::kind() { printf "KIND_MUTATION %s\n" "$*" >> "$ATLAS_TEST_MUTATION_LOG"; }
  registry::_presence_state() { printf "ABSENT\n"; }
  runtime::docker() {
    if [[ $1 == network && $2 == inspect && $3 == kind ]]; then
      return 0
    fi
    printf "DOCKER_MUTATION %s\n" "$*" >> "$ATLAS_TEST_MUTATION_LOG"
  }
  runtime::kubectl() {
    if [[ $1 == get && $2 == namespace && $3 == argocd ]]; then
      return 1
    fi
    if [[ $1 == get && $2 == application ]]; then
      return 1
    fi
    printf "KUBERNETES_MUTATION %s\n" "$*" >> "$ATLAS_TEST_MUTATION_LOG"
  }

  lock::acquire "$ATLAS_STATE_DIR" "$ATLAS_ROOT_DIR"
  rm "$ATLAS_LOCK_OWNER_FILE"
  rmdir "$ATLAS_LOCK_DIR"

  if cluster::ensure_kind > /dev/null 2>&1; then
    printf "cluster accepted a lost lock\n" >&2
    exit 1
  fi
  if registry::ensure_local > /dev/null 2>&1; then
    printf "Registry accepted a lost lock\n" >&2
    exit 1
  fi
  if argocd::install_seed > /dev/null 2>&1; then
    printf "Seed accepted a lost lock\n" >&2
    exit 1
  fi
  if argocd::instantiate_root > /dev/null 2>&1; then
    printf "Tier-0 Root accepted a lost lock\n" >&2
    exit 1
  fi
' _ "$repository_root"

[[ ! -s $mutation_log ]] || test::fail "a lost lifecycle lock reached Cluster, Registry, Seed, or Tier-0 mutation"
test::pass "lost lifecycle lock denies every Normal Bootstrap mutation domain"

handoff_root="${test_workspace}/handoff"
mkdir -m 0700 "$handoff_root"
ATLAS_TEST_MUTATION_LOG=$mutation_log bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  source bootstrap/lib/lock.sh
  source bootstrap/argocd/handoff.sh

  ATLAS_ROOT_DIR=$1
  ATLAS_STATE_DIR="${ATLAS_ROOT_DIR}/.state"
  lock::acquire "$ATLAS_STATE_DIR" "$ATLAS_ROOT_DIR"
  argocd::_root_source_ready() { :; }
  argocd::render() {
    rm "$ATLAS_LOCK_OWNER_FILE"
    rmdir "$ATLAS_LOCK_DIR"
  }
  argocd::_ensure_seed_authority() {
    printf "SEED_MUTATION_REACHED\n" >> "$ATLAS_TEST_MUTATION_LOG"
  }
  runtime::kubectl() {
    printf "TIER0_MUTATION_REACHED %s\n" "$*" >> "$ATLAS_TEST_MUTATION_LOG"
  }

  if argocd::handoff true > /dev/null 2>&1; then
    printf "handoff accepted a lock removed after render\n" >&2
    exit 1
  fi
' _ "$handoff_root"

[[ ! -s $mutation_log ]] || test::fail "handoff continued to Seed or Tier-0 mutation after Render lost the lock"
test::pass "handoff revalidates the lock immediately after Render"

render_failure_root="${test_workspace}/render-failure"
render_failure_mutations="${test_workspace}/render-failure-mutations.log"
mkdir -m 0700 "$render_failure_root"
mkdir -m 0700 \
  "${render_failure_root}/.state" \
  "${render_failure_root}/.state/rendered" \
  "${render_failure_root}/vendor"
printf 'locked chart\n' > "${render_failure_root}/vendor/argocd.tgz"
printf '%s\n' \
  'kind: CustomResourceDefinition' \
  'metadata:' \
  '  name: applications.argoproj.io' \
  '---' \
  'kind: Deployment' \
  'metadata:' \
  '  name: atlas-argocd-server' \
  'spec:' \
  '  template:' \
  '    spec:' \
  '      containers:' \
  '        - image: argocd.test/argocd@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
  '        - image: redis.test/redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
  > "${render_failure_root}/.state/rendered/argocd-seed.yaml"
printf 'stale root\n' > "${render_failure_root}/.state/rendered/root-app.yaml"
chmod 0600 \
  "${render_failure_root}/vendor/argocd.tgz" \
  "${render_failure_root}/.state/rendered/argocd-seed.yaml" \
  "${render_failure_root}/.state/rendered/root-app.yaml"
render_failure_seed_sha=$(shasum -a 256 "${render_failure_root}/.state/rendered/argocd-seed.yaml" | awk '{print $1}')
render_failure_chart_sha=$(shasum -a 256 "${render_failure_root}/vendor/argocd.tgz" | awk '{print $1}')

ATLAS_TEST_MUTATION_LOG=$render_failure_mutations bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  source bootstrap/lib/lock.sh
  source bootstrap/argocd/render.sh
  source bootstrap/argocd/handoff.sh

  ATLAS_ROOT_DIR=$1
  ATLAS_STATE_DIR="${ATLAS_ROOT_DIR}/.state"
  ATLAS_TEST_CHART_SHA=$2
  config::version() {
    case "$1" in
      ARGOCD_CHART_FILE) printf "vendor/argocd.tgz\n" ;;
      ARGOCD_CHART_SHA256) printf "%s\n" "$ATLAS_TEST_CHART_SHA" ;;
      ARGOCD_IMAGE) printf "argocd.test/argocd@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" ;;
      REDIS_IMAGE) printf "redis.test/redis@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" ;;
      *) return 1 ;;
    esac
  }
  argocd::_root_source_ready() { :; }
  argocd::render_seed() {
    printf "seed renderer failed\n" >&2
    return 42
  }
  argocd::_ensure_seed_authority() {
    printf "SEED_MUTATION_REACHED\n" >> "$ATLAS_TEST_MUTATION_LOG"
  }
  runtime::kubectl() {
    printf "KUBERNETES_MUTATION_REACHED %s\n" "$*" >> "$ATLAS_TEST_MUTATION_LOG"
  }

  lock::acquire "$ATLAS_STATE_DIR" "$ATLAS_ROOT_DIR"
  argocd::_validate_rendered_seed "${ATLAS_STATE_DIR}/rendered/argocd-seed.yaml"
  if argocd::handoff true > /dev/null 2>&1; then
    printf "handoff accepted a failed Seed renderer\n" >&2
    exit 1
  fi
  lock::release
' _ "$render_failure_root" "$render_failure_chart_sha"

[[ $(shasum -a 256 "${render_failure_root}/.state/rendered/argocd-seed.yaml" | awk '{print $1}') == "$render_failure_seed_sha" ]] || test::fail "failed rendering replaced the last valid Seed artifact"
[[ ! -s $render_failure_mutations ]] || test::fail "failed rendering reached Seed, AppProject, or External Root mutation"
test::pass "conditional handoff propagates renderer failure without accepting stale artifacts"

wait_failure_log="${test_workspace}/wait-failure.log"
wait_failure_mutations="${test_workspace}/wait-failure-mutations.log"
ATLAS_TEST_WAIT_LOG=$wait_failure_log ATLAS_TEST_MUTATION_LOG=$wait_failure_mutations bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  source bootstrap/argocd/seed.sh
  source bootstrap/argocd/handoff.sh

  config::get() {
    [[ $1 == ATLAS_READY_TIMEOUT ]] || return 1
    printf "1s\n"
  }
  lock::assert_held() { :; }
  argocd::_root_source_ready() { :; }
  argocd::render() { :; }
  argocd::_ensure_seed_authority() { argocd::_wait_seed; }
  runtime::kubectl() {
    if [[ $1 == wait ]]; then
      printf "%s\n" "$*" >> "$ATLAS_TEST_WAIT_LOG"
      if [[ $2 == customresourcedefinition/applications.argoproj.io ]]; then
        return 1
      fi
      return 0
    fi
    printf "KUBERNETES_MUTATION_REACHED %s\n" "$*" >> "$ATLAS_TEST_MUTATION_LOG"
  }

  if argocd::handoff true > /dev/null 2>&1; then
    printf "handoff accepted an early Seed readiness failure\n" >&2
    exit 1
  fi
'

[[ $(wc -l < "$wait_failure_log" | tr -d ' ') == 1 ]] || test::fail "Seed readiness continued after the first failed wait"
[[ ! -s $wait_failure_mutations ]] || test::fail "failed Seed readiness reached AppProject or External Root mutation"
test::pass "conditional handoff stops at the first Seed readiness failure"
