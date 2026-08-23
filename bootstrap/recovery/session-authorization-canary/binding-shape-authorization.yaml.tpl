apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: atlas-bootstrap-recovery-binding-shape-authorization-canary
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
spec:
  failurePolicy: Fail
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
    - name: session-authorizer-or-canary-binding
      expression: >-
        request.userInfo.username == '__ATLAS_SESSION_AUTHORIZER_USERNAME__' ||
        (request.namespace == 'kube-system' &&
          ((request.operation != 'CREATE' &&
            (oldObject.metadata.name.startsWith('atlas-bg-canary-') ||
              (has(oldObject.metadata.labels) &&
                'atlas.io/recovery-session' in oldObject.metadata.labels))) ||
          (request.operation != 'DELETE' &&
            (object.metadata.name.startsWith('atlas-bg-canary-') ||
              (has(object.metadata.labels) &&
                'atlas.io/recovery-session' in object.metadata.labels)))))
  variables:
    - name: binding
      expression: "request.operation == 'DELETE' ? oldObject : object"
  validations:
    - expression: >-
        request.namespace == 'kube-system' &&
        request.userInfo.username == '__ATLAS_SESSION_AUTHORIZER_USERNAME__' &&
        request.operation != 'UPDATE'
      message: Canary permission Binding lifecycle requires the exact Session Authorizer
      reason: Forbidden
    - expression: >-
        has(variables.binding.metadata.labels) &&
        variables.binding.metadata.labels.size() == 3 &&
        variables.binding.metadata.labels['app.kubernetes.io/part-of'] == 'atlas-recovery' &&
        variables.binding.metadata.labels['atlas.io/recovery-scope'] == 'canary' &&
        variables.binding.metadata.labels['atlas.io/recovery-session'].matches('^[0-9a-f]{32}$') &&
        variables.binding.metadata.name ==
          'atlas-bg-canary-' + variables.binding.metadata.labels['atlas.io/recovery-session']
      message: Canary permission Binding name or labels are invalid
      reason: Forbidden
    - expression: >-
        has(variables.binding.metadata.annotations) &&
        variables.binding.metadata.annotations.size() == 4 &&
        variables.binding.metadata.annotations['atlas.io/recovery-fence-uid'].matches('^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') &&
        variables.binding.metadata.annotations['atlas.io/recovery-plan-sha256'].matches('^[0-9a-f]{64}$') &&
        variables.binding.metadata.annotations['atlas.io/recovery-target-sha256'].matches('^[0-9a-f]{64}$') &&
        variables.binding.metadata.annotations['atlas.io/recovery-revision'].matches('^[0-9a-f]{40}$')
      message: Canary permission Binding lineage annotations are invalid
      reason: Forbidden
    - expression: >-
        variables.binding.roleRef.apiGroup == 'rbac.authorization.k8s.io' &&
        variables.binding.roleRef.kind == 'Role' &&
        variables.binding.roleRef.name == 'atlas-bootstrap-recovery-canary' &&
        variables.binding.subjects.size() == 1 &&
        variables.binding.subjects[0].apiGroup == 'rbac.authorization.k8s.io' &&
        variables.binding.subjects[0].kind == 'User' &&
        variables.binding.subjects[0].name == '__ATLAS_RECOVERY_OPERATOR_USERNAME__'
      message: Canary permission Binding roleRef or subject is invalid
      reason: Forbidden
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: atlas-bootstrap-recovery-binding-shape-authorization-canary
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
spec:
  policyName: atlas-bootstrap-recovery-binding-shape-authorization-canary
  validationActions:
    - Audit
    - Deny
