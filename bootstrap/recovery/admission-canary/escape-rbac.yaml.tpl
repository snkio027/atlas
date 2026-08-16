apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: atlas-bootstrap-break-glass-escape
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
rules:
  - apiGroups:
      - admissionregistration.k8s.io
    resources:
      - validatingadmissionpolicies
    resourceNames:
      - atlas-bootstrap-admission-escape-canary
    verbs:
      - get
  - apiGroups:
      - admissionregistration.k8s.io
    resources:
      - validatingadmissionpolicybindings
    resourceNames:
      - atlas-bootstrap-admission-escape-canary
    verbs:
      - get
      - patch
      - update
  - apiGroups:
      - ""
    resources:
      - configmaps
    resourceNames:
      - atlas-bootstrap-admission-escape-canary
    verbs:
      - get
  - apiGroups:
      - ""
    resources:
      - namespaces
    resourceNames:
      - kube-system
    verbs:
      - get
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: atlas-bootstrap-break-glass-escape
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: atlas-bootstrap-break-glass-escape
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: User
    name: __ATLAS_RECOVERY_OPERATOR_USERNAME__
