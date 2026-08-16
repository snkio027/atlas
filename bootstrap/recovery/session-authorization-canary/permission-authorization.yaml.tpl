apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: atlas-bootstrap-recovery-permission-authorization-canary
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
spec:
  failurePolicy: Fail
  paramKind:
    apiVersion: v1
    kind: ConfigMap
  matchConstraints:
    resourceRules:
      - apiGroups:
          - rbac.authorization.k8s.io
        apiVersions:
          - v1
        operations:
          - CREATE
          - UPDATE
          - DELETE
        resources:
          - rolebindings
        scope: Namespaced
  matchConditions:
    - name: canary-session-binding
      expression: "request.namespace == 'kube-system'"
  variables:
    - name: binding
      expression: "request.operation == 'DELETE' ? oldObject : object"
  validations:
    - expression: "params != null"
      message: Canary Fence parameter is required
      reason: Forbidden
    - expression: >-
        params.metadata.namespace == 'kube-system' &&
        params.metadata.name == 'atlas-bootstrap-operation-fence-canary' &&
        has(params.immutable) && params.immutable &&
        has(params.data) && params.data.size() == 11 &&
        params.data.schema == 'atlas.io/bootstrap-operation-fence/v1' &&
        params.data.mode == 'recovery'
      message: Canary permission parameter is not the canonical immutable Fence
      reason: Forbidden
    - expression: >-
        request.userInfo.username == params.data.authorizerPrincipal &&
        params.data.authorizerPrincipal == '__ATLAS_SESSION_AUTHORIZER_USERNAME__' &&
        params.data.recoveryPrincipal == '__ATLAS_RECOVERY_OPERATOR_USERNAME__' &&
        variables.binding.metadata.labels['atlas.io/recovery-session'] == params.data.sessionID &&
        variables.binding.metadata.annotations['atlas.io/recovery-plan-sha256'] == params.data.planSHA256 &&
        variables.binding.metadata.annotations['atlas.io/recovery-target-sha256'] == params.data.clusterFingerprintSHA256 &&
        variables.binding.metadata.annotations['atlas.io/recovery-revision'] == params.data.knownGoodRevision &&
        variables.binding.metadata.annotations['atlas.io/recovery-fence-uid'] == params.metadata.uid
      message: Canary permission Binding does not match the Fence lineage
      reason: Forbidden
    - expression: >-
        request.namespace == 'kube-system' &&
        request.operation != 'UPDATE' &&
        variables.binding.metadata.name == 'atlas-bg-canary-' + params.data.sessionID &&
        variables.binding.roleRef.apiGroup == 'rbac.authorization.k8s.io' &&
        variables.binding.roleRef.kind == 'Role' &&
        variables.binding.roleRef.name == 'atlas-bootstrap-recovery-canary' &&
        variables.binding.subjects.size() == 1 &&
        variables.binding.subjects[0].apiGroup == 'rbac.authorization.k8s.io' &&
        variables.binding.subjects[0].kind == 'User' &&
        variables.binding.subjects[0].name == params.data.recoveryPrincipal
      message: Canary permission Binding target is invalid
      reason: Forbidden
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: atlas-bootstrap-recovery-permission-authorization-canary
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
spec:
  policyName: atlas-bootstrap-recovery-permission-authorization-canary
  matchResources:
    objectSelector:
      matchLabels:
        atlas.io/recovery-scope: canary
      matchExpressions:
        - key: atlas.io/recovery-session
          operator: Exists
  paramRef:
    name: atlas-bootstrap-operation-fence-canary
    namespace: kube-system
    parameterNotFoundAction: Deny
  validationActions:
    - Audit
    - Deny
