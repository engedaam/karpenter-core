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

package validation

import (
	"context"
	"fmt"

	"github.com/awslabs/operatorpkg/reasonable"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/apis/v1alpha1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

type InstanceTypeUpdate struct {
	overlayName string
	Offering    map[string]*string
	Capacity    corev1.ResourceList
	weight      *int32
}

// Controller for validating NodeOverlay configuration and surfacing conflicts to the user
type Controller struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
}

func (c *Controller) Name() string {
	return "nodeoverlay.validation"
}

// NewController constructs a controller for node overlay validation
func NewController(kubeClient client.Client, cp cloudprovider.CloudProvider) *Controller {
	return &Controller{
		kubeClient:    kubeClient,
		cloudProvider: cp,
	}
}

// Reconcile validates that all node overlays don't have conflicting requirements
func (c *Controller) Reconcile(ctx context.Context, _ *v1alpha1.NodeOverlay) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, c.Name())

	overlayList := &v1alpha1.NodeOverlayList{}
	nodePoolList := &v1.NodePoolList{}
	if err := c.kubeClient.List(ctx, overlayList); err != nil {
		return reconcile.Result{}, fmt.Errorf("listing nodeoverlays, %w", err)
	}
	err := c.kubeClient.List(ctx, nodePoolList)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("listing nodepool, %w", err)
	}

	overlayWithRuntimeValidationFailure := map[string]error{}
	overlaysWithConflict := []string{}
	updatedInstanceTypes := map[string]InstanceTypeUpdate{}

	for i := range overlayList.Items {
		if err := overlayList.Items[i].RuntimeValidate(ctx); err != nil {
			overlayWithRuntimeValidationFailure[overlayList.Items[i].Name] = err
			continue
		}

		// Due to reserved capacity type offering being dynamically injected as part of the GetInstanceTypes call
		// We will need to make sure we are validating against each nodepool to make sure. This will ensure that
		// overlays that are targeting reserved instance offerings will be able to apply the offering.
		for _, np := range nodePoolList.Items {
			its, err := c.cloudProvider.GetInstanceTypes(ctx, &np)
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("listing instance types for nodepool, %w", err)
			}
			overlaysWithConflict = append(overlaysWithConflict, checkOverlayPerNodePool(its, updatedInstanceTypes, overlayList.Items[i])...)
		}
	}

	filterOverlaysWithConflict := lo.Filter(lo.Uniq(overlaysWithConflict), func(val string, _ int) bool { return val != "" })
	return reconcile.Result{}, c.updateOverlayStatuses(ctx, overlayList.Items, filterOverlaysWithConflict, overlayWithRuntimeValidationFailure)
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		// The reconciled overlay does not matter in this case as one reconcile loop
		// will compare every overlay against every other overlay in the cluster.
		For(&v1alpha1.NodeOverlay{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
			RateLimiter:             reasonable.RateLimiter(),
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}

func checkOverlayPerNodePool(its []*cloudprovider.InstanceType, store map[string]InstanceTypeUpdate, overlay v1alpha1.NodeOverlay) []string {
	overlaysWithConflict := []string{}
	overlayRequirements := scheduling.NewNodeSelectorRequirements(overlay.Spec.Requirements...)

	for _, it := range its {
		offerings := getOverlaidOfferings(it, overlayRequirements)
		if len(offerings) != 0 {
			_, foundInstanceType := store[it.Name]
			if !foundInstanceType {
				// if both are not defined, price will be set to nil
				price := lo.Ternary(overlay.Spec.Price == nil, overlay.Spec.PriceAdjustment, overlay.Spec.Price)
				offeringMap := map[string]*string{}
				if price != nil {
					for _, of := range offerings {
						offeringMap[of.Requirements.String()] = price
					}
				}
				store[it.Name] = InstanceTypeUpdate{
					overlayName: overlay.Name,
					Offering:    offeringMap,
					Capacity:    overlay.Spec.Capacity,
					weight:      overlay.Spec.Weight,
				}
				continue
			}

			conflictingPriceOverlay := isPriceAlreadyUpdated(store, it, offerings, overlay)
			conflictingCapacityOverlay := isCapacityAlreadyUpdated(store, it, overlay)
			// When we find an instance type that is matches a set offering, we will track that based on the
			// overlay that is applied
			if conflictingPriceOverlay != "" || conflictingCapacityOverlay != "" {
				overlaysWithConflict = append(overlaysWithConflict, overlay.Name, conflictingPriceOverlay, conflictingCapacityOverlay)
			}
		}
	}

	return overlaysWithConflict
}

// isOfferingsForCompatibleInstanceType will validate that an instance type matches a set of node overlay requirements
// if true, the set of Compatible offering
func getOverlaidOfferings(it *cloudprovider.InstanceType, overlayReq scheduling.Requirements) cloudprovider.Offerings {
	if !it.Requirements.IsCompatible(overlayReq) {
		return nil
	}
	return it.Offerings.Compatible(overlayReq)
}

func isPriceAlreadyUpdated(store map[string]InstanceTypeUpdate, it *cloudprovider.InstanceType, offerings cloudprovider.Offerings, overlay v1alpha1.NodeOverlay) string {
	overlayPriceChange := lo.Ternary(overlay.Spec.Price == nil, overlay.Spec.PriceAdjustment, overlay.Spec.Price)
	_, foundUpdatedInstanceType := store[it.Name]
	if !foundUpdatedInstanceType {
		return ""
	}

	for _, of := range offerings {
		offeringPrice, foundOffering := store[it.Name].Offering[of.Requirements.String()]
		if lo.FromPtr(overlay.Spec.Weight) == lo.FromPtr(store[it.Name].weight) && (offeringPrice != nil || overlayPriceChange != nil) {
			if (lo.FromPtr(offeringPrice) == lo.FromPtr(overlayPriceChange)) || !foundOffering {
				store[it.Name].Offering[of.Requirements.String()] = overlayPriceChange
			} else {
				return store[it.Name].overlayName
			}
		}
	}

	return ""
}

func isCapacityAlreadyUpdated(store map[string]InstanceTypeUpdate, it *cloudprovider.InstanceType, overlay v1alpha1.NodeOverlay) string {
	_, foundUpdatedInstanceType := store[it.Name]

	if foundUpdatedInstanceType && lo.FromPtr(overlay.Spec.Weight) == lo.FromPtr(store[it.Name].weight) && overlay.Spec.Capacity != nil && store[it.Name].Capacity != nil {
		resource := findConflictingResources(overlay.Spec.Capacity, store[it.Name].Capacity)
		if len(resource) == 0 {
			for k, v := range overlay.Spec.Capacity {
				store[it.Name].Capacity[k] = v
			}
		} else {
			return store[it.Name].overlayName
		}
	}

	return ""
}

func (c *Controller) updateOverlayStatuses(ctx context.Context, overlayList []v1alpha1.NodeOverlay, overlaysWithConflict []string, overlayWithRuntimeValidationFailure map[string]error) error {
	var errs []error
	for _, overlay := range overlayList {
		stored := overlay.DeepCopy()
		overlay.StatusConditions().SetTrue(v1alpha1.ConditionTypeValidationSucceeded)
		if err, ok := overlayWithRuntimeValidationFailure[overlay.Name]; ok {
			overlay.StatusConditions().SetFalse(v1alpha1.ConditionTypeValidationSucceeded, "RuntimeValidation", err.Error())
		} else if lo.Contains(overlaysWithConflict, overlay.Name) {
			overlay.StatusConditions().SetFalse(v1alpha1.ConditionTypeValidationSucceeded, "Conflict", "conflict with another overlay")
		}

		if !equality.Semantic.DeepEqual(stored, overlay) {
			// We use client.MergeFromWithOptimisticLock because patching a list with a JSON merge patch
			// can cause races due to the fact that it fully replaces the list on a change
			// Here, we are updating the status condition list
			if err := c.kubeClient.Status().Patch(ctx, &overlay, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); !errors.IsConflict(client.IgnoreNotFound(err)) {
				errs = append(errs, err)
			}
		}
	}
	return multierr.Combine(errs...)
}

// findConflictingResources compares two resource field with each other, and validated that
// the same resource is not set to different values within the resource list
func findConflictingResources(capacityOne corev1.ResourceList, capacityTwo corev1.ResourceList) []string {
	result := []string{}
	for key, quantity := range capacityOne {
		if _, ok := capacityTwo[key]; ok && !quantity.Equal(capacityTwo[key]) {
			result = append(result, string(key))
		}
	}
	return result
}
