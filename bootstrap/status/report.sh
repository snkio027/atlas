# shellcheck shell=bash

[[ -n ${_ATLAS_STATUS_REPORT_LOADED:-} ]] && return 0
readonly _ATLAS_STATUS_REPORT_LOADED=1

status::_component_state() {
  local report=$1 expected_component=$2 first_line component state detail tabless
  first_line=${report%%$'\n'*}
  tabless=${first_line//$'\t'/}
  ((${#first_line} - ${#tabless} == 2)) || return 1
  IFS=$'\t' read -r component state detail <<< "$first_line"
  [[ $component == "$expected_component" && -n $state && -n $detail ]] || return 1
  printf '%s\n' "$state"
}

status::_synthetic_argocd_report() {
  local state=$1 detail=$2
  printf 'argocd\t%s\t%s\n' "$state" "$detail"
  printf 'root\t%s\t%s\n' "$state" "$detail"
  printf 'argocd-self\t%s\t%s\n' "$state" "$detail"
}

status::inspect() {
  local cluster_report cluster_state registry_report argocd_report

  if cluster_report=$(cluster::inspect_status); then :; else :; fi
  [[ -n $cluster_report ]] || cluster_report=$'cluster\tUNAVAILABLE\tstatus inspection produced no record'
  printf '%s\n' "$cluster_report"

  if registry_report=$(registry::inspect_status); then :; else :; fi
  [[ -n $registry_report ]] || registry_report=$'registry\tUNAVAILABLE\tstatus inspection produced no record'
  printf '%s\n' "$registry_report"

  cluster_state=$(status::_component_state "$cluster_report" cluster) || cluster_state=UNAVAILABLE
  case "$cluster_state" in
    READY | DRIFTED)
      if argocd_report=$(argocd::inspect_status); then :; else :; fi
      [[ -n $argocd_report ]] ||
        argocd_report=$(status::_synthetic_argocd_report UNAVAILABLE "status inspection produced no record")
      ;;
    ABSENT)
      argocd_report=$(status::_synthetic_argocd_report ABSENT "cluster absent")
      ;;
    *)
      argocd_report=$(status::_synthetic_argocd_report UNAVAILABLE "cluster status unavailable")
      ;;
  esac
  printf '%s\n' "$argocd_report"
}

status::_record_severity() {
  local component=$1 state=$2
  case "$component" in
    cluster | registry)
      case "$state" in
        READY) return 0 ;;
        ABSENT | DRIFTED) return 1 ;;
        UNAVAILABLE) return 2 ;;
        *) return 2 ;;
      esac
      ;;
    argocd)
      case "$state" in
        READY) return 0 ;;
        ABSENT | DEGRADED) return 1 ;;
        UNAVAILABLE) return 2 ;;
        *) return 2 ;;
      esac
      ;;
    root | argocd-self)
      case "$state" in
        Synced/Healthy) return 0 ;;
        ABSENT | */*) return 1 ;;
        UNAVAILABLE) return 2 ;;
        *) return 2 ;;
      esac
      ;;
    *) return 2 ;;
  esac
}

status::check() {
  local report=$1 line tabless component state detail record_severity expected_component
  local severity=0
  local -A seen=()

  while IFS= read -r line || [[ -n $line ]]; do
    tabless=${line//$'\t'/}
    if ((${#line} - ${#tabless} != 2)); then
      severity=2
      continue
    fi
    IFS=$'\t' read -r component state detail <<< "$line"
    if [[ -z $component || -z $state || -z $detail || -n ${seen[$component]:-} ]]; then
      severity=2
      continue
    fi
    seen[$component]=1
    if status::_record_severity "$component" "$state"; then
      record_severity=0
    else
      record_severity=$?
    fi
    ((record_severity > severity)) && severity=$record_severity
  done <<< "$report"

  for expected_component in cluster registry argocd root argocd-self; do
    [[ -n ${seen[$expected_component]:-} ]] || severity=2
  done
  return "$severity"
}
