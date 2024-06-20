/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"context"
	"fmt"
	"strings"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"knative.dev/pkg/apis"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/apis/v1beta1"
)

func (in *NodePool) ConvertTo(ctx context.Context, to apis.Convertible) error {
	fmt.Println("ConvertToV1")
	sink := to.(*v1beta1.NodePool)
	sink.Name = in.Name
	sink.UID = in.UID

	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(config, manager.Options{})
	if err != nil {
		return err
	}

	nodeclass := &unstructured.Unstructured{}
	ncGVK := schema.GroupVersionKind{
		Group:   "karpenter.k8s.aws",
		Version: "v1",
		Kind:    "EC2NodeClass",
	}
	nodeclass.SetGroupVersionKind(ncGVK)
	err = mgr.GetClient().Get(ctx, types.NamespacedName{Name: in.Spec.Template.Spec.NodeClassRef.Name}, nodeclass)
	if err != nil {
		return err
	}
	fmt.Println(nodeclass)
	// sink.Spec.Template.Spec.Kubelet = nodeclass.Object["spec"]

	// adding nodeclassRef
	sink.Spec.Template.Spec.NodeClassRef = &v1beta1.NodeClassReference{
		APIVersion: fmt.Sprintf("%s/%s", in.Spec.Template.Spec.NodeClassRef.Group, "v1beta1"),
		Kind:       in.Spec.Template.Spec.NodeClassRef.Kind,
		Name:       in.Spec.Template.Spec.NodeClassRef.Name,
	}

	// adding disruption
	sink.Spec.Disruption = v1beta1.Disruption{
		ConsolidateAfter:    (*v1beta1.NillableDuration)(in.Spec.Disruption.ConsolidateAfter),
		ConsolidationPolicy: (v1beta1.ConsolidationPolicy)(in.Spec.Disruption.ConsolidationPolicy),
		ExpireAfter:         v1beta1.NillableDuration(in.Spec.Disruption.ExpireAfter),
		Budgets: lo.Map(in.Spec.Disruption.Budgets, func(budget Budget, _ int) v1beta1.Budget {
			return v1beta1.Budget{
				Nodes:    budget.Nodes,
				Schedule: budget.Schedule,
				Duration: budget.Duration,
			}
		}),
	}
	// adding requirements
	sink.Spec.Template.Spec.Requirements = lo.Map(in.Spec.Template.Spec.Requirements, func(x NodeSelectorRequirementWithMinValues, _ int) v1beta1.NodeSelectorRequirementWithMinValues {
		return v1beta1.NodeSelectorRequirementWithMinValues{
			v1.NodeSelectorRequirement{
				Key:      x.Key,
				Operator: v1.NodeSelectorOperator(x.Operator),
				Values:   x.Values,
			},
			x.MinValues,
		}
	})
	// adding taints
	sink.Spec.Template.Spec.Taints = in.Spec.Template.Spec.Taints
	// adding startup taints
	sink.Spec.Template.Spec.StartupTaints = in.Spec.Template.Spec.StartupTaints
	// adding limits
	sink.Spec.Limits = v1beta1.Limits(in.Spec.Limits)
	// adding weight
	sink.Spec.Weight = in.Spec.Weight

	sink.Annotations = lo.Assign(sink.Annotations, map[string]string{
		v1beta1.NodePoolHashAnnotationKey:        sink.Hash(),
		v1beta1.NodePoolHashVersionAnnotationKey: v1beta1.NodePoolHashVersion,
	})

	return nil
}

func (in *NodePool) ConvertFrom(ctx context.Context, from apis.Convertible) error {
	fmt.Println("ConvertFromV1")
	sink := from.(*v1beta1.NodePool)
	in.Name = sink.Name
	in.UID = sink.UID

	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(config, manager.Options{})
	if err != nil {
		return err
	}

	nodeclass := &unstructured.Unstructured{}
	ncGVK := schema.GroupVersionKind{
		Group:   "karpenter.k8s.aws",
		Version: "v1beta1",
		Kind:    "EC2NodeClass",
	}
	nodeclass.SetGroupVersionKind(ncGVK)
	err = mgr.GetClient().Get(ctx, types.NamespacedName{Name: sink.Spec.Template.Spec.NodeClassRef.Name}, nodeclass)
	if err != nil {
		return err
	}

	// adding nodeclassRef - updated for v1
	in.Spec.Template.Spec.NodeClassRef = &NodeClassReference{
		Name:  sink.Spec.Template.Spec.NodeClassRef.Name,
		Kind:  sink.Spec.Template.Spec.NodeClassRef.Kind,
		Group: strings.Split(sink.Spec.Template.Spec.NodeClassRef.APIVersion, "/")[0],
	}

	// adding disruption
	in.Spec.Disruption = Disruption{
		ConsolidateAfter:    (*NillableDuration)(sink.Spec.Disruption.ConsolidateAfter),
		ConsolidationPolicy: (ConsolidationPolicy)(sink.Spec.Disruption.ConsolidationPolicy),
		ExpireAfter:         NillableDuration(sink.Spec.Disruption.ExpireAfter),
		Budgets: lo.Map(sink.Spec.Disruption.Budgets, func(budget v1beta1.Budget, _ int) Budget {
			return Budget{
				Nodes:    budget.Nodes,
				Schedule: budget.Schedule,
				Duration: budget.Duration,
			}
		}),
	}
	// adding requirements
	in.Spec.Template.Spec.Requirements = lo.Map(sink.Spec.Template.Spec.Requirements, func(x v1beta1.NodeSelectorRequirementWithMinValues, _ int) NodeSelectorRequirementWithMinValues {
		return NodeSelectorRequirementWithMinValues{
			v1.NodeSelectorRequirement{
				Key:      x.Key,
				Operator: v1.NodeSelectorOperator(x.Operator),
				Values:   x.Values,
			},
			x.MinValues,
		}
	})
	// adding taints
	in.Spec.Template.Spec.Taints = sink.Spec.Template.Spec.Taints
	// adding startup taints
	in.Spec.Template.Spec.StartupTaints = sink.Spec.Template.Spec.StartupTaints
	// adding limits
	in.Spec.Limits = Limits(sink.Spec.Limits)
	// adding weight
	in.Spec.Weight = sink.Spec.Weight
	in.Annotations = lo.Assign(sink.Annotations, map[string]string{
		v1beta1.NodePoolHashAnnotationKey:        in.Hash(),
		v1beta1.NodePoolHashVersionAnnotationKey: NodePoolHashVersion,
	})

	return nil
}
