apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: atlas-bootstrap-recovery-fence-authorization-canary
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups:
          - ""
        apiVersions:
          - v1
        operations:
          - CREATE
          - UPDATE
          - DELETE
        resources:
          - configmaps
        scope: Namespaced
  matchConditions:
    - name: session-authorizer-or-canary-fence
      expression: >-
        request.userInfo.username == '__ATLAS_SESSION_AUTHORIZER_USERNAME__' ||
        (request.namespace == 'kube-system' &&
          (request.operation == 'DELETE'
            ? oldObject.metadata.name == 'atlas-bootstrap-operation-fence-canary'
            : object.metadata.name == 'atlas-bootstrap-operation-fence-canary'))
  variables:
    - name: fence
      expression: "request.operation == 'DELETE' ? oldObject : object"
  validations:
    - expression: >-
        request.namespace == 'kube-system' &&
        variables.fence.metadata.name == 'atlas-bootstrap-operation-fence-canary'
      message: Canary Fence requests must target the canonical kube-system object
      reason: Forbidden
    - expression: >-
        request.userInfo.username == '__ATLAS_SESSION_AUTHORIZER_USERNAME__' &&
        request.operation != 'UPDATE'
      message: Canary Fence create or delete requires the exact Session Authorizer
      reason: Forbidden
    - expression: >-
        has(variables.fence.immutable) && variables.fence.immutable &&
        has(variables.fence.data) && variables.fence.data.size() == 11 &&
        has(variables.fence.metadata.labels) &&
        variables.fence.metadata.labels.size() == 3 &&
        variables.fence.metadata.labels['app.kubernetes.io/part-of'] == 'atlas-recovery' &&
        variables.fence.metadata.labels['atlas.io/recovery-scope'] == 'canary' &&
        variables.fence.metadata.labels['atlas.io/recovery-session'] == variables.fence.data.sessionID &&
        variables.fence.data.schema == 'atlas.io/bootstrap-operation-fence/v1' &&
        variables.fence.data.mode == 'recovery' &&
        variables.fence.data.operationID.matches('^[0-9a-f]{32}$') &&
        variables.fence.data.sessionID.matches('^[0-9a-f]{32}$') &&
        variables.fence.data.clusterFingerprintSHA256.matches('^[0-9a-f]{64}$') &&
        variables.fence.data.planSHA256.matches('^[0-9a-f]{64}$') &&
        variables.fence.data.knownGoodRevision.matches('^[0-9a-f]{40}$') &&
        variables.fence.data.createdAt.matches('^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$') &&
        variables.fence.data.holderUsername == '__ATLAS_SESSION_AUTHORIZER_USERNAME__' &&
        variables.fence.data.authorizerPrincipal == '__ATLAS_SESSION_AUTHORIZER_USERNAME__' &&
        variables.fence.data.recoveryPrincipal == '__ATLAS_RECOVERY_OPERATOR_USERNAME__'
      message: Canary Fence schema or authority lineage is invalid
      reason: Forbidden
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: atlas-bootstrap-recovery-fence-authorization-canary
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
spec:
  policyName: atlas-bootstrap-recovery-fence-authorization-canary
  validationActions:
    - Audit
    - Deny
