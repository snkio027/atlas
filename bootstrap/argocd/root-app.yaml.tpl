apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: @@ROOT_NAME@@
  namespace: argocd
spec:
  project: atlas-bootstrap
  source:
    repoURL: '@@REPO_URL@@'
    targetRevision: '@@REVISION@@'
    path: '@@ROOT_PATH@@'
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - ApplyOutOfSyncOnly=true
