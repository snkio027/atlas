apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: atlas-bootstrap-recovery-guard-authorization-canary
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
    - name: recovery-operator-or-canary-guard-change
      expression: >-
        request.namespace == 'kube-system' &&
        ((request.userInfo.username == '__ATLAS_RECOVERY_OPERATOR_USERNAME__' &&
          ((request.operation != 'CREATE' &&
            oldObject.metadata.name == 'atlas-bootstrap-recovery-guard-canary') ||
          (request.operation != 'DELETE' &&
            object.metadata.name == 'atlas-bootstrap-recovery-guard-canary'))) ||
        (request.operation != 'CREATE' &&
          oldObject.metadata.name == 'atlas-bootstrap-recovery-guard-canary' &&
          has(oldObject.data) &&
          'policy.atlas-recovery-freeze.csv' in oldObject.data) ||
        (request.operation != 'DELETE' &&
          object.metadata.name == 'atlas-bootstrap-recovery-guard-canary' &&
          has(object.data) &&
          'policy.atlas-recovery-freeze.csv' in object.data))
  variables:
    - name: guard
      expression: "request.operation == 'DELETE' ? oldObject : object"
    - name: oldGuardValue
      expression: >-
        request.operation == 'CREATE' || !has(oldObject.data) ||
        !('policy.atlas-recovery-freeze.csv' in oldObject.data)
          ? ''
          : oldObject.data['policy.atlas-recovery-freeze.csv']
    - name: newGuardValue
      expression: >-
        request.operation == 'DELETE' || !has(object.data) ||
        !('policy.atlas-recovery-freeze.csv' in object.data)
          ? ''
          : object.data['policy.atlas-recovery-freeze.csv']
  validations:
    - expression: >-
        request.userInfo.username == '__ATLAS_RECOVERY_OPERATOR_USERNAME__' &&
        variables.guard.metadata.name == 'atlas-bootstrap-recovery-guard-canary'
      message: Canary guard mutation requires the exact Recovery Operator and target
      reason: Forbidden
    - expression: "request.operation != 'DELETE'"
      message: Canary guard ConfigMap cannot be deleted while the guard exists
      reason: Forbidden
    - expression: >-
        request.operation != 'CREATE' ||
        (has(object.metadata.labels) && object.metadata.labels.size() == 2 &&
          object.metadata.labels['app.kubernetes.io/part-of'] == 'atlas-recovery' &&
          object.metadata.labels['atlas.io/recovery-scope'] == 'canary' &&
          !has(object.metadata.annotations) &&
          !has(object.metadata.finalizers) &&
          !has(object.metadata.ownerReferences) &&
          !has(object.binaryData) && !has(object.immutable) &&
          has(object.data) && object.data.size() == 2 &&
          object.data.sentinel == 'recovery-guard-canary')
      message: Canary guard CREATE projection is invalid
      reason: Forbidden
    - expression: >-
        request.operation != 'UPDATE' ||
        (object.metadata.labels == oldObject.metadata.labels &&
          object.metadata.annotations == oldObject.metadata.annotations &&
          object.metadata.finalizers == oldObject.metadata.finalizers &&
          object.metadata.ownerReferences == oldObject.metadata.ownerReferences &&
          !has(object.binaryData) && !has(oldObject.binaryData) &&
          !has(object.immutable) && !has(oldObject.immutable) &&
          has(object.data) && has(oldObject.data) &&
          object.data.size() <= 2 && oldObject.data.size() <= 2 &&
          object.data.sentinel == 'recovery-guard-canary' &&
          oldObject.data.sentinel == 'recovery-guard-canary')
      message: Canary guard UPDATE changed a field outside the guarded projection
      reason: Forbidden
    - expression: >-
        variables.oldGuardValue != variables.newGuardValue &&
        (variables.newGuardValue == '' ||
          variables.newGuardValue == 'p, role:atlas-recovery-guard-canary, applications, *, */*, deny')
      message: Canary guard value is unchanged or invalid
      reason: Forbidden
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: atlas-bootstrap-recovery-guard-authorization-canary
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
spec:
  policyName: atlas-bootstrap-recovery-guard-authorization-canary
  validationActions:
    - Audit
    - Deny
