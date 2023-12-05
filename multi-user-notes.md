
### Quick Deploy (assuming argo already deployed via `make argodeploy`:

```bash
version=5
img=quay.io/hukhan/data-science-pipelines-operator:argo-multi-user-$version && make build-deploy IMG=$img
```

Query the {{dsp-route}}/apis/v2beta1/.. endpoints


### Some changes to note (do full diff for details)
# Branch v2-argo-multi-user
1. Cluster rbac added (see rbac folder)
2. Oauth proxy updated 
3. UI env vars updated for multi-user
4. API Server env vars updated for multi-user 
5. UI Doesn't work fully (not sure why)


### Cluster wide argo deployment
1. Add S3 info name: mlpipeline-minio-artifact
2. Update workflow-controller-configmap

### Set UI deployment env vars
6. HIDE_SIDENAV=false
7. DEPLOYMENT=KUBEFLOW
8. DISABLE_GKE_METADATA=true
