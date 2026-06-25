/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	farmv1alpha1 "github.com/adamkekesi/android-device-farm/operator/api/v1alpha1"
)

const (
	// LabelPool marks a Device as owned by a DevicePool.
	LabelPool = "farm.example.com/pool"
	// LabelClass marks a Device's DeviceClass.
	LabelClass = "farm.example.com/class"
)

// DevicePoolReconciler reconciles a DevicePool object: it keeps minWarm ready
// Devices per class. Capacity caps, eviction, and leasing arrive in Phase 4.
type DevicePoolReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=farm.example.com,resources=devicepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=farm.example.com,resources=devicepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=farm.example.com,resources=devicepools/finalizers,verbs=update
// +kubebuilder:rbac:groups=farm.example.com,resources=devices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=farm.example.com,resources=devices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=farm.example.com,resources=deviceclasses,verbs=get;list;watch

// Reconcile ensures the pool has minWarm Devices per referenced class.
func (r *DevicePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pool farmv1alpha1.DevicePool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	active, err := r.activeDevicesByClass(ctx, &pool)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Total active devices (warm + on-demand) must stay within maxConcurrent, so
	// warm provisioning yields to leased/evicted capacity instead of fighting it.
	var total int32
	var all []farmv1alpha1.Device
	for _, ds := range active {
		total += int32(len(ds))
		all = append(all, ds...)
	}

	// Reserve capacity for pending lease demand current supply can't meet, so warm
	// devices don't crowd out (or get refilled to fight eviction for) leased slots.
	var reserved int32
	for _, pc := range pool.Spec.Classes {
		demand, err := pendingLeaseDemand(ctx, r.Client, pool.Namespace, pc.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		if supply := unclaimedSupply(all, pc.Name); demand > supply {
			reserved += demand - supply
		}
	}
	warmBudget := pool.Spec.MaxConcurrent - reserved

	classMissing := false
	for _, pc := range pool.Spec.Classes {
		var dc farmv1alpha1.DeviceClass
		if err := r.Get(ctx, client.ObjectKey{Name: pc.Name}, &dc); err != nil {
			if apierrors.IsNotFound(err) {
				log.Info("referenced DeviceClass not found; skipping", "class", pc.Name)
				classMissing = true
				continue
			}
			return ctrl.Result{}, err
		}

		for have := int32(len(active[pc.Name])); have < pc.MinWarm && total < warmBudget; have++ {
			if err := r.createDevice(ctx, &pool, &dc); err != nil {
				return ctrl.Result{}, err
			}
			total++
			log.Info("created warm device", "pool", pool.Name, "class", pc.Name)
		}
	}

	if err := r.updateStatus(ctx, &pool, classMissing); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// activeDevicesByClass returns non-terminating Devices owned by the pool, keyed
// by class.
func (r *DevicePoolReconciler) activeDevicesByClass(ctx context.Context, pool *farmv1alpha1.DevicePool) (map[string][]farmv1alpha1.Device, error) {
	var devices farmv1alpha1.DeviceList
	if err := r.List(ctx, &devices,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{LabelPool: pool.Name},
	); err != nil {
		return nil, err
	}
	byClass := map[string][]farmv1alpha1.Device{}
	for i := range devices.Items {
		d := devices.Items[i]
		if d.DeletionTimestamp != nil {
			continue // going away; don't count toward warm
		}
		byClass[d.Spec.ClassRef] = append(byClass[d.Spec.ClassRef], d)
	}
	return byClass, nil
}

// newPoolDevice builds (but does not create) a Device CR owned by the pool for
// the given class. Shared by the DevicePool (warm) and DeviceLease (on-demand)
// controllers so they label/own devices identically.
func newPoolDevice(pool *farmv1alpha1.DevicePool, dc *farmv1alpha1.DeviceClass) *farmv1alpha1.Device {
	return &farmv1alpha1.Device{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-%s-", pool.Name, dc.Name),
			Namespace:    pool.Namespace,
			Labels: map[string]string{
				LabelPool:  pool.Name,
				LabelClass: dc.Name,
			},
		},
		Spec: farmv1alpha1.DeviceSpec{
			ClassRef:     dc.Name,
			PoolRef:      pool.Name,
			ProviderType: dc.Spec.ProviderType,
		},
	}
}

func (r *DevicePoolReconciler) createDevice(ctx context.Context, pool *farmv1alpha1.DevicePool, dc *farmv1alpha1.DeviceClass) error {
	dev := newPoolDevice(pool, dc)
	if err := ctrl.SetControllerReference(pool, dev, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, dev)
}

func (r *DevicePoolReconciler) updateStatus(ctx context.Context, pool *farmv1alpha1.DevicePool, classMissing bool) error {
	var devices farmv1alpha1.DeviceList
	if err := r.List(ctx, &devices,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{LabelPool: pool.Name},
	); err != nil {
		return err
	}
	pool.Status.PerClass = computeCounts(pool.Spec.Classes, devices.Items)

	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		ObservedGeneration: pool.Generation,
		Reason:             "WarmPoolSatisfied",
		Message:            "minWarm satisfied for all referenced classes",
	}
	if classMissing {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "DeviceClassNotFound"
		cond.Message = "one or more referenced DeviceClasses do not exist"
	}
	meta.SetStatusCondition(&pool.Status.Conditions, cond)
	return r.Status().Update(ctx, pool)
}

// computeCounts tallies devices per class by phase.
func computeCounts(classes []farmv1alpha1.PoolClass, devices []farmv1alpha1.Device) []farmv1alpha1.ClassCounts {
	out := make([]farmv1alpha1.ClassCounts, 0, len(classes))
	idx := map[string]int{}
	for _, c := range classes {
		idx[c.Name] = len(out)
		out = append(out, farmv1alpha1.ClassCounts{Class: c.Name})
	}
	for i := range devices {
		d := devices[i]
		pos, ok := idx[d.Spec.ClassRef]
		if !ok {
			continue
		}
		cc := &out[pos]
		cc.Total++
		switch d.Status.Phase {
		case farmv1alpha1.DeviceReady:
			cc.Ready++
		case farmv1alpha1.DeviceLeased:
			cc.Leased++
		default:
			cc.Provisioning++
		}
	}
	return out
}

// poolsForLease enqueues pools in a lease's namespace so capacity reservation
// reacts promptly to new/changed leases.
func (r *DevicePoolReconciler) poolsForLease(ctx context.Context, obj client.Object) []ctrl.Request {
	var list farmv1alpha1.DevicePoolList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	reqs := make([]ctrl.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *DevicePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&farmv1alpha1.DevicePool{}).
		Owns(&farmv1alpha1.Device{}).
		Watches(&farmv1alpha1.DeviceLease{}, handler.EnqueueRequestsFromMapFunc(r.poolsForLease)).
		Named("devicepool").
		Complete(r)
}
