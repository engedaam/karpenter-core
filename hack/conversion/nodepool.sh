# Adding conversion block to the NodePool Resource 

yq eval '.spec.conversion.strategy="Webhook"' -i pkg/apis/crds/karpenter.sh_nodepools.yaml 
yq eval '.spec.conversion.webhook.conversionReviewVersions=["v1beta1", "v1"]' -i pkg/apis/crds/karpenter.sh_nodepools.yaml 
yq eval '.spec.conversion.webhook.clientConfig.service.namespace="kube-system"' -i pkg/apis/crds/karpenter.sh_nodepools.yaml 
yq eval '.spec.conversion.webhook.clientConfig.service.name="karpenter"' -i pkg/apis/crds/karpenter.sh_nodepools.yaml 
yq eval '.spec.conversion.webhook.clientConfig.service.port=8443' -i pkg/apis/crds/karpenter.sh_nodepools.yaml 




