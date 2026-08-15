#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

mapfile -t production_shell < <(find bootstrap -type f \( -name '*.sh' -o -name atlas -o -name atlas-recovery -o -name atlas-kind-drill \) | sort)
mapfile -t all_shell < <(find bootstrap tests -type f \( -name '*.sh' -o -name atlas -o -name atlas-recovery -o -name atlas-kind-drill \) | sort)
expected_production_shell=(
  bootstrap/argocd/handoff.sh
  bootstrap/argocd/render.sh
  bootstrap/argocd/seed.sh
  bootstrap/argocd/status.sh
  bootstrap/atlas
  bootstrap/cluster/kind.sh
  bootstrap/drill/atlas-kind-drill
  bootstrap/drill/contract.sh
  bootstrap/drill/evidence.sh
  bootstrap/drill/lifecycle.sh
  bootstrap/drill/lock.sh
  bootstrap/host/doctor.sh
  bootstrap/lib/config.sh
  bootstrap/lib/lock.sh
  bootstrap/lib/runtime.sh
  bootstrap/recovery/atlas-recovery
  bootstrap/recovery/audit.sh
  bootstrap/registry/local.sh
)

[[ ${production_shell[*]} == "${expected_production_shell[*]}" ]] || test::fail "production Shell layout differs from its domain contract"

bash -n "${all_shell[@]}"
shellcheck -x "${all_shell[@]}"
shfmt -d -i 2 -ci -sr "${all_shell[@]}"

test::pass "Shell syntax, ShellCheck, and shfmt"
