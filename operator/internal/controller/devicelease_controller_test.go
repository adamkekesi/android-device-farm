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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	farmv1alpha1 "github.com/adamkekesi/android-device-farm/operator/api/v1alpha1"
)

var _ = Describe("DeviceLease controller", func() {
	const (
		className = "lease-class"
		poolName  = "lease-pool"
		namespace = "default"
		timeout   = 25 * time.Second
		interval  = 200 * time.Millisecond
	)

	// makeReady patches a device's pod to Ready so the Device controller marks the
	// device Ready (envtest has no kubelet).
	makeReady := func(deviceName string) {
		pod := &corev1.Pod{}
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Name: deviceName + "-emulator", Namespace: namespace}, pod)
		}, timeout, interval).Should(Succeed())
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		Eventually(func() farmv1alpha1.DevicePhase {
			var d farmv1alpha1.Device
			_ = k8sClient.Get(ctx, client.ObjectKey{Name: deviceName, Namespace: namespace}, &d)
			return d.Status.Phase
		}, timeout, interval).Should(Equal(farmv1alpha1.DeviceReady))
	}

	deviceNames := func() []string {
		var list farmv1alpha1.DeviceList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(namespace),
			client.MatchingLabels{LabelPool: poolName})).To(Succeed())
		out := []string{}
		for i := range list.Items {
			if list.Items[i].DeletionTimestamp == nil {
				out = append(out, list.Items[i].Name)
			}
		}
		return out
	}

	getLease := func(name string) *farmv1alpha1.DeviceLease {
		var l farmv1alpha1.DeviceLease
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &l)).To(Succeed())
		return &l
	}

	newLease := func(name string, ttl int32) *farmv1alpha1.DeviceLease {
		return &farmv1alpha1.DeviceLease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: farmv1alpha1.DeviceLeaseSpec{
				ClassRef: className, Requester: "test", TTLSeconds: ttl,
			},
		}
	}

	BeforeEach(func() {
		Expect(k8sClient.Create(ctx, &farmv1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
			Spec:       farmv1alpha1.DeviceClassSpec{ProviderType: farmv1alpha1.ProviderEmulator, Image: "ex/emu:1"},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &farmv1alpha1.DevicePool{
			ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: namespace},
			Spec: farmv1alpha1.DevicePoolSpec{
				Classes:       []farmv1alpha1.PoolClass{{Name: className, MinWarm: 2}},
				MaxConcurrent: 2,
			},
		})).To(Succeed())
		Eventually(func() int { return len(deviceNames()) }, timeout, interval).Should(Equal(2))
		for _, n := range deviceNames() {
			makeReady(n)
		}
	})

	AfterEach(func() {
		var leases farmv1alpha1.DeviceLeaseList
		_ = k8sClient.List(ctx, &leases, client.InNamespace(namespace))
		for i := range leases.Items {
			_ = k8sClient.Delete(ctx, &leases.Items[i])
		}
		_ = k8sClient.Delete(ctx, &farmv1alpha1.DevicePool{ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: namespace}})
		_ = k8sClient.Delete(ctx, &farmv1alpha1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: className}})
		_ = k8sClient.DeleteAllOf(ctx, &farmv1alpha1.Device{}, client.InNamespace(namespace), client.MatchingLabels{LabelPool: poolName})
		_ = k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(namespace))
	})

	It("binds a Ready device and marks it Leased", func() {
		Expect(k8sClient.Create(ctx, newLease("l1", 300))).To(Succeed())

		Eventually(func() farmv1alpha1.LeasePhase { return getLease("l1").Status.Phase },
			timeout, interval).Should(Equal(farmv1alpha1.LeaseBound))

		l := getLease("l1")
		Expect(l.Status.DeviceRef).NotTo(BeEmpty())
		Expect(l.Status.ExpiresAt).NotTo(BeNil())

		var d farmv1alpha1.Device
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: l.Status.DeviceRef, Namespace: namespace}, &d)).To(Succeed())
		Expect(d.Status.LeaseRef).To(Equal("l1"))
		Eventually(func() farmv1alpha1.DevicePhase {
			var dd farmv1alpha1.Device
			_ = k8sClient.Get(ctx, client.ObjectKey{Name: l.Status.DeviceRef, Namespace: namespace}, &dd)
			return dd.Status.Phase
		}, timeout, interval).Should(Equal(farmv1alpha1.DeviceLeased))
	})

	It("never double-binds: 3 leases over 2 devices bind exactly 2 distinct devices", func() {
		for i := 1; i <= 3; i++ {
			Expect(k8sClient.Create(ctx, newLease(fmt.Sprintf("c%d", i), 300))).To(Succeed())
		}

		Eventually(func() int {
			bound := 0
			for i := 1; i <= 3; i++ {
				if getLease(fmt.Sprintf("c%d", i)).Status.Phase == farmv1alpha1.LeaseBound {
					bound++
				}
			}
			return bound
		}, timeout, interval).Should(Equal(2))

		// Each device is bound to at most one lease.
		seen := map[string]string{}
		for i := 1; i <= 3; i++ {
			l := getLease(fmt.Sprintf("c%d", i))
			if l.Status.Phase == farmv1alpha1.LeaseBound {
				Expect(seen).NotTo(HaveKey(l.Status.DeviceRef), "device double-bound")
				seen[l.Status.DeviceRef] = l.Name
			}
		}
		Expect(seen).To(HaveLen(2))

		// The third lease stays Pending (no capacity yet — provisioning is increment 2).
		Consistently(func() int {
			pending := 0
			for i := 1; i <= 3; i++ {
				if getLease(fmt.Sprintf("c%d", i)).Status.Phase == farmv1alpha1.LeasePending {
					pending++
				}
			}
			return pending
		}, 2*time.Second, interval).Should(Equal(1))
	})

	It("auto-releases an un-heartbeated lease after its TTL and recycles the device", func() {
		Expect(k8sClient.Create(ctx, newLease("ttl", 2))).To(Succeed())
		Eventually(func() farmv1alpha1.LeasePhase { return getLease("ttl").Status.Phase },
			timeout, interval).Should(Equal(farmv1alpha1.LeaseBound))
		deviceRef := getLease("ttl").Status.DeviceRef

		// No heartbeats: within ~TTL the lease expires and the device is freed.
		Eventually(func() farmv1alpha1.LeasePhase { return getLease("ttl").Status.Phase },
			timeout, interval).Should(Equal(farmv1alpha1.LeaseExpired))
		Eventually(func() string {
			var d farmv1alpha1.Device
			_ = k8sClient.Get(ctx, client.ObjectKey{Name: deviceRef, Namespace: namespace}, &d)
			return d.Status.LeaseRef
		}, timeout, interval).Should(BeEmpty())
	})

	It("extends the lease when heartbeated past the original TTL", func() {
		Expect(k8sClient.Create(ctx, newLease("hb", 3))).To(Succeed())
		Eventually(func() farmv1alpha1.LeasePhase { return getLease("hb").Status.Phase },
			timeout, interval).Should(Equal(farmv1alpha1.LeaseBound))

		// Heartbeat a few times across the original TTL window (merge-patch so it
		// doesn't race the controller's own status updates).
		for i := 0; i < 4; i++ {
			time.Sleep(time.Second)
			base := getLease("hb")
			patched := base.DeepCopy()
			now := metav1.Now()
			patched.Status.LastHeartbeat = &now
			Expect(k8sClient.Status().Patch(ctx, patched, client.MergeFrom(base))).To(Succeed())
		}
		Expect(getLease("hb").Status.Phase).To(Equal(farmv1alpha1.LeaseBound))
	})

	It("frees the device when a bound lease is deleted", func() {
		Expect(k8sClient.Create(ctx, newLease("del", 300))).To(Succeed())
		Eventually(func() farmv1alpha1.LeasePhase { return getLease("del").Status.Phase },
			timeout, interval).Should(Equal(farmv1alpha1.LeaseBound))
		deviceRef := getLease("del").Status.DeviceRef

		Expect(k8sClient.Delete(ctx, &farmv1alpha1.DeviceLease{ObjectMeta: metav1.ObjectMeta{Name: "del", Namespace: namespace}})).To(Succeed())
		Eventually(func() string {
			var d farmv1alpha1.Device
			_ = k8sClient.Get(ctx, client.ObjectKey{Name: deviceRef, Namespace: namespace}, &d)
			return d.Status.LeaseRef
		}, timeout, interval).Should(BeEmpty())
	})
})

var _ = Describe("DeviceLease capacity & eviction", func() {
	const (
		namespace = "default"
		timeout   = 25 * time.Second
		interval  = 200 * time.Millisecond
	)

	// makeReady patches the device's pod to Ready. It tolerates the device being
	// immediately Leased (a waiting lease may bind it the moment it's Ready).
	makeReady := func(deviceName string) {
		pod := &corev1.Pod{}
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Name: deviceName + "-emulator", Namespace: namespace}, pod)
		}, timeout, interval).Should(Succeed())
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		Eventually(func() bool {
			var d farmv1alpha1.Device
			_ = k8sClient.Get(ctx, client.ObjectKey{Name: deviceName, Namespace: namespace}, &d)
			return d.Status.Phase == farmv1alpha1.DeviceReady || d.Status.Phase == farmv1alpha1.DeviceLeased
		}, timeout, interval).Should(BeTrue())
	}

	devicesOfClass := func(class string) []farmv1alpha1.Device {
		var list farmv1alpha1.DeviceList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(namespace),
			client.MatchingLabels{LabelClass: class})).To(Succeed())
		out := []farmv1alpha1.Device{}
		for i := range list.Items {
			if list.Items[i].DeletionTimestamp == nil {
				out = append(out, list.Items[i])
			}
		}
		return out
	}

	createClass := func(name string) {
		Expect(k8sClient.Create(ctx, &farmv1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       farmv1alpha1.DeviceClassSpec{ProviderType: farmv1alpha1.ProviderEmulator, Image: "ex/" + name},
		})).To(Succeed())
	}

	AfterEach(func() {
		var leases farmv1alpha1.DeviceLeaseList
		_ = k8sClient.List(ctx, &leases, client.InNamespace(namespace))
		for i := range leases.Items {
			_ = k8sClient.Delete(ctx, &leases.Items[i])
		}
		var pools farmv1alpha1.DevicePoolList
		_ = k8sClient.List(ctx, &pools, client.InNamespace(namespace))
		for i := range pools.Items {
			_ = k8sClient.Delete(ctx, &pools.Items[i])
		}
		_ = k8sClient.DeleteAllOf(ctx, &farmv1alpha1.DeviceClass{})
		_ = k8sClient.DeleteAllOf(ctx, &farmv1alpha1.Device{}, client.InNamespace(namespace))
		_ = k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(namespace))
	})

	It("provisions a device on demand when none is warm", func() {
		createClass("cap-a")
		Expect(k8sClient.Create(ctx, &farmv1alpha1.DevicePool{
			ObjectMeta: metav1.ObjectMeta{Name: "cap-pool", Namespace: namespace},
			Spec: farmv1alpha1.DevicePoolSpec{
				Classes:       []farmv1alpha1.PoolClass{{Name: "cap-a", MinWarm: 0}},
				MaxConcurrent: 2,
			},
		})).To(Succeed())

		Expect(k8sClient.Create(ctx, &farmv1alpha1.DeviceLease{
			ObjectMeta: metav1.ObjectMeta{Name: "ondemand", Namespace: namespace},
			Spec:       farmv1alpha1.DeviceLeaseSpec{ClassRef: "cap-a", Requester: "t", TTLSeconds: 300},
		})).To(Succeed())

		// The operator provisions one device for the class on demand.
		Eventually(func() int { return len(devicesOfClass("cap-a")) }, timeout, interval).Should(Equal(1))
		makeReady(devicesOfClass("cap-a")[0].Name)

		Eventually(func() farmv1alpha1.LeasePhase {
			var l farmv1alpha1.DeviceLease
			_ = k8sClient.Get(ctx, client.ObjectKey{Name: "ondemand", Namespace: namespace}, &l)
			return l.Status.Phase
		}, timeout, interval).Should(Equal(farmv1alpha1.LeaseBound))

		// It does not over-provision.
		Consistently(func() int { return len(devicesOfClass("cap-a")) }, 2*time.Second, interval).Should(Equal(1))
	})

	It("evicts an idle device of another class at capacity to serve a new type", func() {
		createClass("evict-a")
		createClass("evict-b")
		Expect(k8sClient.Create(ctx, &farmv1alpha1.DevicePool{
			ObjectMeta: metav1.ObjectMeta{Name: "evict-pool", Namespace: namespace},
			Spec: farmv1alpha1.DevicePoolSpec{
				Classes: []farmv1alpha1.PoolClass{
					{Name: "evict-a", MinWarm: 2},
					{Name: "evict-b", MinWarm: 0},
				},
				MaxConcurrent:  2,
				EvictionPolicy: farmv1alpha1.EvictLRUIdle,
			},
		})).To(Succeed())

		// Two idle class-A devices fill the pool to capacity.
		Eventually(func() int { return len(devicesOfClass("evict-a")) }, timeout, interval).Should(Equal(2))
		for _, d := range devicesOfClass("evict-a") {
			makeReady(d.Name)
		}

		// A lease for class B must evict an idle A and provision a B.
		Expect(k8sClient.Create(ctx, &farmv1alpha1.DeviceLease{
			ObjectMeta: metav1.ObjectMeta{Name: "needs-b", Namespace: namespace},
			Spec:       farmv1alpha1.DeviceLeaseSpec{ClassRef: "evict-b", Requester: "t", TTLSeconds: 300},
		})).To(Succeed())

		Eventually(func() int { return len(devicesOfClass("evict-b")) }, timeout, interval).Should(Equal(1))
		makeReady(devicesOfClass("evict-b")[0].Name)

		Eventually(func() farmv1alpha1.LeasePhase {
			var l farmv1alpha1.DeviceLease
			_ = k8sClient.Get(ctx, client.ObjectKey{Name: "needs-b", Namespace: namespace}, &l)
			return l.Status.Phase
		}, timeout, interval).Should(Equal(farmv1alpha1.LeaseBound))

		// One A was evicted, so the pool stayed within maxConcurrent.
		Eventually(func() int { return len(devicesOfClass("evict-a")) }, timeout, interval).Should(Equal(1))
	})
})
