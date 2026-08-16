apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: atlas-bootstrap-recovery-canary
  namespace: kube-system
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
rules:
  - apiGroups:
      - ""
    resources:
      - configmaps
    resourceNames:
      - atlas-bootstrap-admission-escape-canary
    verbs:
      - get
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: atlas-bootstrap-recovery-authorizer-canary
  namespace: kube-system
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
rules:
  # CREATE cannot be constrained by resourceNames. The Fence authorization
  # Policy rejects every non-canonical object.
  - apiGroups:
      - ""
    resources:
      - configmaps
    verbs:
      - create
  - apiGroups:
      - ""
    resources:
      - configmaps
    resourceNames:
      - atlas-bootstrap-operation-fence-canary
    verbs:
      - get
      - delete
  # Temporary names contain the approved session ID. Shape and Permission
  # Policies constrain this namespace-wide RBAC lifecycle grant.
  - apiGroups:
      - rbac.authorization.k8s.io
    resources:
      - rolebindings
    verbs:
      - create
      - delete
  - apiGroups:
      - rbac.authorization.k8s.io
    resources:
      - roles
    resourceNames:
      - atlas-bootstrap-recovery-canary
    verbs:
      - get
      - bind
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: atlas-bootstrap-recovery-authorizer-canary
  namespace: kube-system
  labels:
    app.kubernetes.io/part-of: atlas-recovery
    atlas.io/recovery-scope: canary
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: atlas-bootstrap-recovery-authorizer-canary
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: User
    name: __ATLAS_SESSION_AUTHORIZER_USERNAME__
