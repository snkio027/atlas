apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: atlas-bootstrap-admission-escape-canary
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
    - name: canonical-canary-target
      expression: >-
        request.namespace == 'kube-system' &&
        (request.operation == 'DELETE'
          ? oldObject.metadata.name == 'atlas-bootstrap-admission-escape-canary'
          : object.metadata.name == 'atlas-bootstrap-admission-escape-canary')
    - name: ordinary-principal
      expression: >-
        request.userInfo.username != '__ATLAS_RECOVERY_OPERATOR_USERNAME__'
  validations:
    - expression: "false"
      message: Atlas admission escape canary mutation requires the exact Recovery Operator
      reason: Forbidden
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: atlas-bootstrap-admission-escape-canary
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
spec:
  policyName: atlas-bootstrap-admission-escape-canary
  validationActions:
    - Audit
    - Deny
