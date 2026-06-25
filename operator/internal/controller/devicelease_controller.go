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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	farmv1alpha1 "github.com/adamkekesi/android-device-farm/operator/api/v1alpha1"
)

// leaseFinalizer ensures a deleted lease frees its device.
const leaseFinalizer = "farm.example.com/lease-cleanup"

// DeviceLeaseReconciler reconciles a DeviceLease: it binds a Ready, unleased
// Device of the requested class with optimistic concurrency, tracks TTL via
// heartbeats, and releases (recycling the device) on expiry or deletion.
// Capacity-aware provisioning and eviction arrive in the next increment.
type DeviceLeaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=farm.example.com,resources=deviceleases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=farm.example.com,resources=deviceleases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=farm.example.com,resources=deviceleases/finalizers,verbs=update
// +kubebuilder:rbac:groups=farm.example.com,resources=devices,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=farm.example.com,resources=devices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=farm.example.com,resources=devicepools,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;delete

func (r *DeviceLeaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var lease farmv1alpha1.DeviceLease
	if err := r.Get(ctx, req.NamespacedName, &lease); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion: free the device, then drop the finalizer.
	if !lease.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&lease, leaseFinalizer) {
			if err := r.releaseDevice(ctx, &lease); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&lease, leaseFinalizer)
			if err := r.Update(ctx, &lease); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&lease, leaseFinalizer) {
		controllerutil.AddFinalizer(&lease, leaseFinalizer)
		return ctrl.Result{}, r.Update(ctx, &lease)
	}

	switch lease.Status.Phase {
	case farmv1alpha1.LeaseReleased, farmv1alpha1.LeaseFailed, farmv1alpha1.LeaseExpired:
		return ctrl.Result{}, nil
	case farmv1alpha1.LeaseBound:
		return r.reconcileBound(ctx, &lease)
	default: // "" or Pending
		return r.reconcilePending(ctx, &lease)
	}
}

func (r *DeviceLeaseReconciler) reconcilePending(ctx context.Context, lease *farmv1alpha1.DeviceLease) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Idempotency: a device may already be bound to us from a partial reconcile.
	dev, err := r.deviceBoundTo(ctx, lease)
	if err != nil {
		return ctrl.Result{}, err
	}
	if dev == nil {
		dev, err = r.freeReadyDevice(ctx, lease)
		if err != nil {
			return ctrl.Result{}, err
		}
		if dev == nil {
			if err := r.ensureCapacity(ctx, lease); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		// Bind with optimistic concurrency; a conflict means another lease won.
		dev.Status.LeaseRef = lease.Name
		if dev.Status.LastUsedTime == nil {
			now := metav1.Now()
			dev.Status.LastUsedTime = &now
		}
		if err := r.Status().Update(ctx, dev); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		log.Info("bound device to lease", "device", dev.Name, "lease", lease.Name)
	}

	now := metav1.Now()
	if lease.Status.LastHeartbeat == nil {
		lease.Status.LastHeartbeat = &now
	}
	lease.Status.DeviceRef = dev.Name
	lease.Status.ADBEndpoint = dev.Status.ADBEndpoint
	lease.Status.UIEndpoint = dev.Status.UIEndpoint
	expires := lease.Status.LastHeartbeat.Add(ttlDuration(lease))
	et := metav1.NewTime(expires)
	lease.Status.ExpiresAt = &et
	if err := r.writeStatus(ctx, lease, farmv1alpha1.LeaseBound, "Bound", "bound to device "+dev.Name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Until(expires)}, nil
}

func (r *DeviceLeaseReconciler) reconcileBound(ctx context.Context, lease *farmv1alpha1.DeviceLease) (ctrl.Result, error) {
	if lease.Status.LastHeartbeat == nil {
		now := metav1.Now()
		lease.Status.LastHeartbeat = &now
	}
	expires := lease.Status.LastHeartbeat.Add(ttlDuration(lease))

	if time.Now().After(expires) {
		if err := r.releaseDevice(ctx, lease); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.writeStatus(ctx, lease, farmv1alpha1.LeaseExpired, "TTLExpired",
			"lease expired without a heartbeat; device recycled")
	}

	et := metav1.NewTime(expires)
	if lease.Status.ExpiresAt == nil || !lease.Status.ExpiresAt.Equal(&et) {
		lease.Status.ExpiresAt = &et
		if err := r.writeStatus(ctx, lease, farmv1alpha1.LeaseBound, "Heartbeat", "lease refreshed"); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: time.Until(expires)}, nil
}

// releaseDevice clears the bound device's leaseRef and recycles it (deletes the
// emulator pod so the Device controller recreates a clean one).
func (r *DeviceLeaseReconciler) releaseDevice(ctx context.Context, lease *farmv1alpha1.DeviceLease) error {
	dev, err := r.deviceBoundTo(ctx, lease)
	if err != nil || dev == nil {
		return err
	}
	dev.Status.LeaseRef = ""
	now := metav1.Now()
	dev.Status.LastUsedTime = &now
	if err := r.Status().Update(ctx, dev); err != nil {
		return err
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: emulatorPodName(dev), Namespace: dev.Namespace}}
	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *DeviceLeaseReconciler) deviceBoundTo(ctx context.Context, lease *farmv1alpha1.DeviceLease) (*farmv1alpha1.Device, error) {
	var list farmv1alpha1.DeviceList
	if err := r.List(ctx, &list, client.InNamespace(lease.Namespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Status.LeaseRef == lease.Name {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}

func (r *DeviceLeaseReconciler) freeReadyDevice(ctx context.Context, lease *farmv1alpha1.DeviceLease) (*farmv1alpha1.Device, error) {
	var list farmv1alpha1.DeviceList
	if err := r.List(ctx, &list, client.InNamespace(lease.Namespace),
		client.MatchingLabels{LabelClass: lease.Spec.ClassRef}); err != nil {
		return nil, err
	}
	for i := range list.Items {
		d := &list.Items[i]
		if d.DeletionTimestamp == nil && d.Status.Phase == farmv1alpha1.DeviceReady && d.Status.LeaseRef == "" {
			return d, nil
		}
	}
	return nil, nil
}

// ensureCapacity makes a device available for a pending lease: it provisions one
// when the pool is below maxConcurrent (and unclaimed supply trails demand), or
// evicts an idle device of another class (LRU) when at capacity.
func (r *DeviceLeaseReconciler) ensureCapacity(ctx context.Context, lease *farmv1alpha1.DeviceLease) error {
	class := lease.Spec.ClassRef

	pool, err := r.resolvePool(ctx, lease)
	if err != nil {
		return err
	}
	if pool == nil {
		return r.writeStatus(ctx, lease, farmv1alpha1.LeasePending, "NoPool",
			"no DevicePool offers class "+class)
	}

	var dc farmv1alpha1.DeviceClass
	if err := r.Get(ctx, client.ObjectKey{Name: class}, &dc); err != nil {
		if apierrors.IsNotFound(err) {
			return r.writeStatus(ctx, lease, farmv1alpha1.LeasePending, "DeviceClassNotFound",
				"DeviceClass "+class+" does not exist")
		}
		return err
	}

	devices, err := r.poolDevices(ctx, pool)
	if err != nil {
		return err
	}

	// If a device of the class is already coming (or free) for the pending demand,
	// just wait — this prevents over-provisioning / over-eviction across reconciles.
	demand, err := pendingLeaseDemand(ctx, r.Client, pool.Namespace, class)
	if err != nil {
		return err
	}
	if unclaimedSupply(devices, class) >= demand {
		return r.writeStatus(ctx, lease, farmv1alpha1.LeasePending, "WaitingForDevice",
			"waiting for a provisioning device of class "+class+" to become Ready")
	}

	if int32(len(activeDevices(devices))) < pool.Spec.MaxConcurrent {
		if err := r.provision(ctx, pool, &dc); err != nil {
			return err
		}
		return r.writeStatus(ctx, lease, farmv1alpha1.LeasePending, "Provisioning",
			"provisioning a device of class "+class)
	}

	// At capacity: evict an idle device of another class to make room.
	if pool.Spec.EvictionPolicy == farmv1alpha1.EvictLRUIdle {
		if victim := lruIdleOtherClass(devices, class); victim != nil {
			if err := r.Delete(ctx, victim); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err := r.provision(ctx, pool, &dc); err != nil {
				return err
			}
			return r.writeStatus(ctx, lease, farmv1alpha1.LeasePending, "Evicted",
				"evicted idle "+victim.Spec.ClassRef+" device; provisioning "+class)
		}
	}
	return r.writeStatus(ctx, lease, farmv1alpha1.LeasePending, "AtCapacity",
		"pool at maxConcurrent with no idle device of another class to evict")
}

func (r *DeviceLeaseReconciler) resolvePool(ctx context.Context, lease *farmv1alpha1.DeviceLease) (*farmv1alpha1.DevicePool, error) {
	if lease.Spec.PoolRef != "" {
		var p farmv1alpha1.DevicePool
		if err := r.Get(ctx, client.ObjectKey{Name: lease.Spec.PoolRef, Namespace: lease.Namespace}, &p); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		return &p, nil
	}
	var list farmv1alpha1.DevicePoolList
	if err := r.List(ctx, &list, client.InNamespace(lease.Namespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		for _, c := range list.Items[i].Spec.Classes {
			if c.Name == lease.Spec.ClassRef {
				return &list.Items[i], nil
			}
		}
	}
	return nil, nil
}

func (r *DeviceLeaseReconciler) poolDevices(ctx context.Context, pool *farmv1alpha1.DevicePool) ([]farmv1alpha1.Device, error) {
	var list farmv1alpha1.DeviceList
	if err := r.List(ctx, &list, client.InNamespace(pool.Namespace),
		client.MatchingLabels{LabelPool: pool.Name}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// pendingLeaseDemand counts unsatisfied (Pending/new) leases for a class. Shared
// by the lease controller (on-demand provisioning) and the pool controller
// (capacity reservation).
func pendingLeaseDemand(ctx context.Context, c client.Client, namespace, class string) (int32, error) {
	var list farmv1alpha1.DeviceLeaseList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return 0, err
	}
	var n int32
	for i := range list.Items {
		l := &list.Items[i]
		if l.DeletionTimestamp != nil || l.Spec.ClassRef != class {
			continue
		}
		if l.Status.Phase == "" || l.Status.Phase == farmv1alpha1.LeasePending {
			n++
		}
	}
	return n, nil
}

func (r *DeviceLeaseReconciler) provision(ctx context.Context, pool *farmv1alpha1.DevicePool, dc *farmv1alpha1.DeviceClass) error {
	dev := newPoolDevice(pool, dc)
	if err := ctrl.SetControllerReference(pool, dev, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, dev)
}

func activeDevices(devs []farmv1alpha1.Device) []farmv1alpha1.Device {
	out := make([]farmv1alpha1.Device, 0, len(devs))
	for i := range devs {
		if devs[i].DeletionTimestamp == nil {
			out = append(out, devs[i])
		}
	}
	return out
}

// unclaimedSupply counts unleased devices of the class that are Ready or on their
// way (Provisioning / just-created) — i.e. devices that can satisfy demand soon.
func unclaimedSupply(devs []farmv1alpha1.Device, class string) int32 {
	var n int32
	for i := range devs {
		d := &devs[i]
		if d.DeletionTimestamp != nil || d.Spec.ClassRef != class || d.Status.LeaseRef != "" {
			continue
		}
		switch d.Status.Phase {
		case farmv1alpha1.DeviceReady, farmv1alpha1.DeviceProvisioning, "":
			n++
		}
	}
	return n
}

// lruIdleOtherClass returns the least-recently-used idle (Ready, unleased) device
// of a class other than the requested one, or nil.
func lruIdleOtherClass(devs []farmv1alpha1.Device, class string) *farmv1alpha1.Device {
	var best *farmv1alpha1.Device
	for i := range devs {
		d := &devs[i]
		if d.DeletionTimestamp != nil || d.Spec.ClassRef == class {
			continue
		}
		if d.Status.Phase != farmv1alpha1.DeviceReady || d.Status.LeaseRef != "" {
			continue
		}
		if best == nil || olderLastUsed(d, best) {
			best = d
		}
	}
	return best
}

func olderLastUsed(a, b *farmv1alpha1.Device) bool {
	if a.Status.LastUsedTime == nil {
		return true
	}
	if b.Status.LastUsedTime == nil {
		return false
	}
	return a.Status.LastUsedTime.Before(b.Status.LastUsedTime)
}

func (r *DeviceLeaseReconciler) writeStatus(ctx context.Context, lease *farmv1alpha1.DeviceLease, phase farmv1alpha1.LeasePhase, reason, msg string) error {
	lease.Status.Phase = phase
	condStatus := metav1.ConditionFalse
	if phase == farmv1alpha1.LeaseBound {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&lease.Status.Conditions, metav1.Condition{
		Type:               "Bound",
		Status:             condStatus,
		ObservedGeneration: lease.Generation,
		Reason:             reason,
		Message:            msg,
	})
	return r.Status().Update(ctx, lease)
}

func ttlDuration(lease *farmv1alpha1.DeviceLease) time.Duration {
	return time.Duration(lease.Spec.TTLSeconds) * time.Second
}

// leasesForDevice enqueues pending leases in a device's namespace when the device
// changes (e.g. becomes Ready or is freed), so binding is prompt.
func (r *DeviceLeaseReconciler) leasesForDevice(ctx context.Context, obj client.Object) []ctrl.Request {
	var list farmv1alpha1.DeviceLeaseList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var reqs []ctrl.Request
	for i := range list.Items {
		l := &list.Items[i]
		if l.Status.Phase == "" || l.Status.Phase == farmv1alpha1.LeasePending {
			reqs = append(reqs, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(l)})
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeviceLeaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&farmv1alpha1.DeviceLease{}).
		Watches(&farmv1alpha1.Device{}, handler.EnqueueRequestsFromMapFunc(r.leasesForDevice)).
		Named("devicelease").
		Complete(r)
}
