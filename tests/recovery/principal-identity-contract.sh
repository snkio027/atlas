#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-principal-identity.XXXXXX")
trap 'rm -rf -- "$test_workspace"' EXIT

recovery::die() {
  printf '%s\n' "$*" >&2
  return 1
}

# shellcheck source=bootstrap/recovery/principal-identity.sh
source bootstrap/recovery/principal-identity.sh

namespace_uid=12345678-1234-1234-1234-123456789abc
foreign_uid=aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa
recovery_g1="atlas:break-glass:${namespace_uid}:g1"
recovery_g2="atlas:break-glass:${namespace_uid}:g2"
recovery_g3="atlas:break-glass:${namespace_uid}:g3"
recovery_g999999="atlas:break-glass:${namespace_uid}:g999999"
authorizer_g1="atlas:session-authz:${namespace_uid}:g1"
authorizer_g2="atlas:session-authz:${namespace_uid}:g2"
authorizer_g999999="atlas:session-authz:${namespace_uid}:g999999"
authorizer_g1000000="atlas:session-authz:${namespace_uid}:g1000000"

[[ $(principal_identity::recovery_operator "$namespace_uid" 1) == "$recovery_g1" &&
$(principal_identity::recovery_operator "$namespace_uid" 999999) == "$recovery_g999999" &&
$(principal_identity::session_authorizer "$namespace_uid" 1) == "$authorizer_g1" &&
$(principal_identity::session_authorizer "$namespace_uid" 999999) == "$authorizer_g999999" ]] ||
  test::fail "canonical recovery principal construction drifted"

LC_ALL=C
[[ ${#recovery_g1} -eq 57 && ${#recovery_g999999} -eq 62 &&
  ${#authorizer_g1} -eq 59 && ${#authorizer_g999999} -eq 64 &&
  ${#authorizer_g1000000} -eq 65 ]] ||
  test::fail "canonical recovery principal byte projections drifted"
unset LC_ALL

principal_identity::validate_pair "$recovery_g1" "$authorizer_g1" "$namespace_uid"
principal_identity::validate_recovery_operator "$recovery_g999999" "$namespace_uid"
principal_identity::validate_session_authorizer "$authorizer_g999999" "$namespace_uid"
expected_plan="${recovery_g3}"$'\t'"${authorizer_g2}"$'\t'"${recovery_g2}"$'\t'"${authorizer_g1}"
[[ $(principal_identity::plan "$namespace_uid" 3 2 2 1) == "$expected_plan" ]] ||
  test::fail "multi-generation principal plan is not atomic and deterministic"
principal_identity::validate_plan "$namespace_uid" 3 2 2 1 \
  "$recovery_g3" "$authorizer_g2" "$recovery_g2" "$authorizer_g1"
if principal_identity::validate_plan "$namespace_uid" 3 2 2 1 \
  "$recovery_g3" "$authorizer_g2" "$recovery_g2" \
  "atlas:session-authz:${namespace_uid}:g2" > /dev/null 2>&1; then
  test::fail "drifted multi-generation principal plan was accepted"
fi
for invalid_rotation in '2 2 2 1' '4 2 2 1' '3 2 1 2'; do
  IFS=' ' read -r recovery_current recovery_previous authorizer_current authorizer_previous <<< "$invalid_rotation"
  if principal_identity::plan "$namespace_uid" "$recovery_current" "$recovery_previous" \
    "$authorizer_current" "$authorizer_previous" > /dev/null 2>&1; then
    test::fail "invalid independent principal rotation was accepted: ${invalid_rotation}"
  fi
done

assert_recovery_rejected() {
  if principal_identity::validate_recovery_operator "$1" "${2:-}" > /dev/null 2>&1; then
    test::fail "invalid Recovery Operator principal was accepted: $1"
  fi
}

assert_authorizer_rejected() {
  if principal_identity::validate_session_authorizer "$1" "${2:-}" > /dev/null 2>&1; then
    test::fail "invalid Session Authorizer principal was accepted: $1"
  fi
}

for generation in 0 01 +1 -1 '' 1000000; do
  assert_recovery_rejected "atlas:break-glass:${namespace_uid}:g${generation}"
  assert_authorizer_rejected "atlas:session-authz:${namespace_uid}:g${generation}"
done

for invalid_uid in \
  12345678-1234-1234-1234-123456789ABC \
  12345678123412341234123456789abc \
  12345678-1234-1234-1234-123456789ab \
  '12345678-1234-1234-1234-123456789abc ' \
  ''; do
  assert_recovery_rejected "atlas:break-glass:${invalid_uid}:g1"
  assert_authorizer_rejected "atlas:session-authz:${invalid_uid}:g1"
done

assert_recovery_rejected "$recovery_g1" "$foreign_uid"
assert_authorizer_rejected "$authorizer_g1" "$foreign_uid"
assert_authorizer_rejected "atlas:recovery-authorizer:${namespace_uid}:g1"
if principal_identity::validate_pair "$recovery_g1" \
  "atlas:session-authz:${foreign_uid}:g1" > /dev/null 2>&1; then
  test::fail "principal pair accepted different Namespace UIDs"
fi

mapfile -t locales < <(
  printf '%s\n' C
  locale -a | awk 'tolower($0) ~ /utf-?8/ {print; exit}'
)
for locale_name in "${locales[@]}"; do
  LC_ALL=$locale_name principal_identity::validate_pair "$recovery_g1" "$authorizer_g1" "$namespace_uid"
  if LC_ALL=$locale_name principal_identity::validate_session_authorizer \
    "atlas:session-authz:${namespace_uid}:g1é" > /dev/null 2>&1; then
    test::fail "non-ASCII principal was accepted under ${locale_name}"
  fi
done

key="${test_workspace}/principal.key"
csr="${test_workspace}/principal.csr"
certificate="${test_workspace}/principal.crt"
invalid_csr="${test_workspace}/principal-with-organization.csr"
invalid_certificate="${test_workspace}/principal-with-organization.crt"
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$key" > /dev/null 2>&1
openssl req -new -sha256 -key "$key" -subj "/CN=${authorizer_g1}" -out "$csr"
openssl req -x509 -sha256 -key "$key" -subj "/CN=${authorizer_g1}" -out "$certificate"
principal_identity::validate_csr authorizer "$authorizer_g1" "$csr"
principal_identity::validate_certificate authorizer "$authorizer_g1" "$certificate"

openssl req -new -sha256 -key "$key" -subj "/CN=${authorizer_g1}/O=forbidden" -out "$invalid_csr"
openssl req -x509 -sha256 -key "$key" -subj "/CN=${authorizer_g1}/O=forbidden" -out "$invalid_certificate"
if principal_identity::validate_csr authorizer "$authorizer_g1" "$invalid_csr" > /dev/null 2>&1; then
  test::fail "CSR with an Organization was accepted"
fi
if principal_identity::validate_certificate authorizer "$authorizer_g1" "$invalid_certificate" > /dev/null 2>&1; then
  test::fail "certificate with an Organization was accepted"
fi

test::assert_not_found 'atlas:recovery-authorizer:' bootstrap/recovery docs/runbooks bootstrap/README.md
test::pass "Recovery principal identity contract"
