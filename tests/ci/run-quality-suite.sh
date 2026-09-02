#!/usr/bin/env bash
set -Eeuo pipefail

ci_quality::_fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

ci_quality::_repository_root() {
  local script_dir

  script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P) || return 1
  CDPATH='' cd -- "$script_dir/../.." && pwd -P
}

ci_quality::_validate_workflow_topology() {
  local workflow=$1
  local actual_jobs expected_jobs job task_name count
  local checkout_pin='actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1'
  local aqua_pin='aquaproj/aqua-installer@96a9bc20066c5bf5e275b41019cfc165b25f4e2e'
  local -a mandatory_jobs=(
    bootstrap
    recovery-drill
    gitops-core
    authorization-probe
    personal-local-static
    target-materialization
    final-v2-preflight
    server-phase0
    server-phase1a
  )

  [[ -f "$workflow" && ! -L "$workflow" ]] ||
    ci_quality::_fail "quality workflow must be a regular non-symlink file"

  actual_jobs=$(awk '
    /^jobs:$/ { in_jobs = 1; next }
    in_jobs && /^  [a-z0-9][a-z0-9-]*:$/ {
      job = $0
      sub(/^  /, "", job)
      sub(/:$/, "", job)
      print job
    }
  ' "$workflow") || return 1
  expected_jobs=$(printf '%s\n' "${mandatory_jobs[@]}" quality)
  [[ "$actual_jobs" == "$expected_jobs" ]] ||
    ci_quality::_fail "quality workflow job topology differs from the mandatory shard set"

  for job in "${mandatory_jobs[@]}"; do
    task_name="quality:$job"
    count=$(grep -Fxc "        run: task $task_name" "$workflow" || true)
    [[ "$count" == "1" ]] ||
      ci_quality::_fail "workflow must invoke task $task_name exactly once"
    count=$(grep -Fxc "      - $job" "$workflow" || true)
    [[ "$count" == "1" ]] ||
      ci_quality::_fail "quality aggregator must depend on $job exactly once"
  done

  [[ $(grep -Fc "uses: $checkout_pin" "$workflow" || true) == "9" ]] ||
    ci_quality::_fail "every substantive shard must use the locked checkout action"
  [[ $(grep -Fc "uses: $aqua_pin" "$workflow" || true) == "9" ]] ||
    ci_quality::_fail "every substantive shard must use the locked Aqua installer"
  [[ $(grep -Fxc '    name: quality' "$workflow" || true) == "1" ]] ||
    ci_quality::_fail "the final required job display name must be quality"
  [[ $(grep -Fxc '    if: always()' "$workflow" || true) == "1" ]] ||
    ci_quality::_fail "the quality aggregator must use if: always()"
  [[ $(grep -Fxc '      - name: Require every mandatory shard' "$workflow" || true) == "1" ]] ||
    ci_quality::_fail "the quality aggregator must contain its result check"
}

ci_quality::_validate_inventory() {
  local inventory=$1
  local line_number=0 total=0 repository_total=0 server_total=0
  local seen_commands='' suite legacy_group fast mode command extra

  [[ -f "$inventory" && ! -L "$inventory" ]] ||
    ci_quality::_fail "quality inventory must be a regular non-symlink file"

  while IFS='|' read -r suite legacy_group fast mode command extra; do
    line_number=$((line_number + 1))
    [[ -z "$suite" || "$suite" == \#* ]] && continue

    [[ -n "$command" && -z "${extra:-}" ]] ||
      ci_quality::_fail "invalid inventory field count at line $line_number"
    case "$suite" in
      bootstrap | recovery-drill | gitops-core | authorization-probe | personal-local-static | target-materialization | final-v2-preflight | server-phase0 | server-phase1a) ;;
      *) ci_quality::_fail "unknown suite at inventory line $line_number: $suite" ;;
    esac
    case "$legacy_group" in
      lint | test | conformance | server) ;;
      *) ci_quality::_fail "unknown legacy group at inventory line $line_number: $legacy_group" ;;
    esac
    [[ "$fast" == "0" || "$fast" == "1" ]] ||
      ci_quality::_fail "invalid fast marker at inventory line $line_number"
    case "$mode" in
      direct | git-head | server-phase0 | server-phase1a) ;;
      *) ci_quality::_fail "unknown execution mode at inventory line $line_number: $mode" ;;
    esac
    [[ "$command" == tests/* && "$command" != *'..'* && "$command" != *[[:space:]]* ]] ||
      ci_quality::_fail "unsafe command path at inventory line $line_number"
    [[ -f "$command" && -x "$command" && ! -L "$command" ]] ||
      ci_quality::_fail "inventory command must be an executable non-symlink file: $command"
    [[ $'\n'"$seen_commands" != *$'\n'"$command"$'\n'* ]] ||
      ci_quality::_fail "duplicate mandatory command in inventory: $command"
    seen_commands+="$command"$'\n'

    case "$mode:$legacy_group:$suite" in
      direct:lint:* | direct:test:* | direct:conformance:* | git-head:conformance:* | server-phase0:server:server-phase0 | server-phase1a:server:server-phase1a) ;;
      *) ci_quality::_fail "invalid suite/group/mode combination at inventory line $line_number" ;;
    esac

    total=$((total + 1))
    if [[ "$legacy_group" == "server" ]]; then
      server_total=$((server_total + 1))
    else
      repository_total=$((repository_total + 1))
    fi
  done < "$inventory"

  [[ "$total" == "33" ]] || ci_quality::_fail "mandatory inventory must contain exactly 33 commands"
  [[ "$repository_total" == "31" ]] ||
    ci_quality::_fail "repository-only inventory must contain exactly 31 commands"
  [[ "$server_total" == "2" ]] ||
    ci_quality::_fail "server inventory must contain exactly 2 commands"
}

ci_quality::_selector_is_valid() {
  case "$1" in
    check | repository-all | legacy-lint | legacy-test | legacy-conformance | bootstrap | recovery-drill | gitops-core | authorization-probe | personal-local-static | target-materialization | final-v2-preflight | server-phase0 | server-phase1a) ;;
    *) ci_quality::_fail "unknown quality selector: $1" ;;
  esac
}

ci_quality::_selected() {
  local selector=$1 suite=$2 legacy_group=$3 fast=$4

  case "$selector" in
    check) [[ "$fast" == "1" && "$legacy_group" != "server" ]] ;;
    repository-all) [[ "$legacy_group" != "server" ]] ;;
    legacy-lint) [[ "$legacy_group" == "lint" ]] ;;
    legacy-test) [[ "$legacy_group" == "test" ]] ;;
    legacy-conformance) [[ "$legacy_group" == "conformance" ]] ;;
    *) [[ "$suite" == "$selector" ]] ;;
  esac
}

ci_quality::_git_head() {
  env -i PATH="$PATH" LC_ALL=C git --no-replace-objects \
    -c core.fsmonitor=false \
    -c core.ignoreStat=false \
    rev-parse --verify 'HEAD^{commit}'
}

ci_quality::_run_command() {
  local mode=$1 command=$2

  case "$mode" in
    server-phase0 | server-phase1a)
      [[ "${GITHUB_ACTIONS:-}" == "true" ]] ||
        ci_quality::_fail "Kubernetes server contracts are restricted to GitHub Actions"
      ;;
  esac
  printf 'RUN %s\n' "$command"
  case "$mode" in
    direct)
      "./$command"
      ;;
    git-head)
      if [[ -z "${ci_quality_git_head:-}" ]]; then
        ci_quality_git_head=$(ci_quality::_git_head) || return 1
      fi
      "./$command" "$ci_quality_git_head"
      ;;
    server-phase0)
      ATLAS_CI_KIND_VAP=1 "./$command"
      ;;
    server-phase1a)
      ATLAS_CI_PHASE1A_VAP=1 "./$command"
      ;;
  esac
}

ci_quality::main() {
  local selector=${1:-}
  local repository_root inventory workflow
  local suite legacy_group fast mode command extra
  local selected_count=0

  [[ "$#" == "1" ]] || ci_quality::_fail "usage: run-quality-suite.sh <selector>"
  ci_quality::_selector_is_valid "$selector" || return 1
  repository_root=$(ci_quality::_repository_root) || return 1
  cd -- "$repository_root" || return 1
  inventory="$repository_root/tests/ci/quality-suite-inventory.txt"
  workflow="$repository_root/.github/workflows/quality.yml"

  ci_quality::_validate_inventory "$inventory" || return 1
  ci_quality::_validate_workflow_topology "$workflow" || return 1

  if [[ "$selector" == "check" ]]; then
    printf 'RUN git diff --check\n'
    git diff --check || return 1
    printf 'RUN git diff --cached --check\n'
    git diff --cached --check || return 1
  fi

  while IFS='|' read -r suite legacy_group fast mode command extra; do
    [[ -z "$suite" || "$suite" == \#* ]] && continue
    if ci_quality::_selected "$selector" "$suite" "$legacy_group" "$fast"; then
      ci_quality::_run_command "$mode" "$command" || return 1
      selected_count=$((selected_count + 1))
    fi
  done < "$inventory"

  [[ "$selected_count" -gt 0 ]] || ci_quality::_fail "selector matched no commands: $selector"
  if [[ "$selector" == "repository-all" && "$selected_count" != "31" ]]; then
    ci_quality::_fail "repository-all did not execute exactly 31 commands"
  fi
  printf 'OK quality selector %s (%s mandatory commands)\n' "$selector" "$selected_count"
}

ci_quality::main "$@"
