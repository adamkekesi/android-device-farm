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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	farmv1alpha1 "github.com/adamkekesi/android-device-farm/operator/api/v1alpha1"
)

var _ = Describe("DevicePool warm-pool controller", func() {
	const (
		className = "test-class"
		poolName  = "test-pool"
		namespace = "default"
		timeout   = 20 * time.Second
		interval  = 200 * time.Millisecond
	)

	activeDevices := func() []farmv1alpha1.Device {
		var list farmv1alpha1.DeviceList
		Expect(k8sClient.List(ctx, &list,
			client.InNamespace(namespace),
			client.MatchingLabels{LabelPool: poolName},
		)).To(Succeed())
		out := make([]farmv1alpha1.Device, 0, len(list.Items))
		for i := range list.Items {
			if list.Items[i].DeletionTimestamp == nil {
				out = append(out, list.Items[i])
			}
		}
		return out
	}

	BeforeEach(func() {
		dc := &farmv1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
			Spec: farmv1alpha1.DeviceClassSpec{
				ProviderType: farmv1alpha1.ProviderEmulator,
				Image:        "example/emulator:latest",
			},
		}
		Expect(k8sClient.Create(ctx, dc)).To(Succeed())

		pool := &farmv1alpha1.DevicePool{
			ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: namespace},
			Spec: farmv1alpha1.DevicePoolSpec{
				Classes:       []farmv1alpha1.PoolClass{{Name: className, MinWarm: 2}},
				MaxConcurrent: 5,
			},
		}
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())
	})

	AfterEach(func() {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &farmv1alpha1.DevicePool{
			ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: namespace},
		}))).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &farmv1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
		}))).To(Succeed())
		// Clear any leftover devices so cases don't bleed into each other.
		Expect(k8sClient.DeleteAllOf(ctx, &farmv1alpha1.Device{},
			client.InNamespace(namespace),
			client.MatchingLabels{LabelPool: poolName},
		)).To(Succeed())
	})

	It("provisions minWarm devices for the class", func() {
		Eventually(func() int { return len(activeDevices()) }, timeout, interval).Should(Equal(2))

		By("stamping the right spec + owner reference on created devices")
		devs := activeDevices()
		for _, d := range devs {
			Expect(d.Spec.ClassRef).To(Equal(className))
			Expect(d.Spec.PoolRef).To(Equal(poolName))
			Expect(d.Spec.ProviderType).To(Equal(farmv1alpha1.ProviderEmulator))
			Expect(d.OwnerReferences).To(HaveLen(1))
			Expect(d.OwnerReferences[0].Kind).To(Equal("DevicePool"))
		}
	})

	It("recreates a device when one is deleted, restoring the warm count", func() {
		Eventually(func() int { return len(activeDevices()) }, timeout, interval).Should(Equal(2))

		victim := activeDevices()[0]
		Expect(k8sClient.Delete(ctx, &victim)).To(Succeed())

		// Count of non-victim devices should return to 2.
		Eventually(func() int {
			n := 0
			for _, d := range activeDevices() {
				if d.Name != victim.Name {
					n++
				}
			}
			return n
		}, timeout, interval).Should(Equal(2))
	})

	It("reports per-class counts and a Ready condition in status", func() {
		Eventually(func() int32 {
			var pool farmv1alpha1.DevicePool
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: poolName, Namespace: namespace}, &pool); err != nil {
				return -1
			}
			for _, c := range pool.Status.PerClass {
				if c.Class == className {
					return c.Total
				}
			}
			return -1
		}, timeout, interval).Should(Equal(int32(2)))

		var pool farmv1alpha1.DevicePool
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: poolName, Namespace: namespace}, &pool)).To(Succeed())
		cond := findCondition(pool.Status.Conditions, "Ready")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	})

	It("does not provision and reports NotFound when the class is missing", func() {
		// Remove the class the pool references.
		Expect(k8sClient.Delete(ctx, &farmv1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
		})).To(Succeed())

		missingPool := &farmv1alpha1.DevicePool{
			ObjectMeta: metav1.ObjectMeta{Name: "no-class-pool", Namespace: namespace},
			Spec: farmv1alpha1.DevicePoolSpec{
				Classes:       []farmv1alpha1.PoolClass{{Name: "ghost-class", MinWarm: 2}},
				MaxConcurrent: 5,
			},
		}
		Expect(k8sClient.Create(ctx, missingPool)).To(Succeed())
		defer func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, missingPool))).To(Succeed())
		}()

		Eventually(func() *metav1.Condition {
			var pool farmv1alpha1.DevicePool
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: "no-class-pool", Namespace: namespace}, &pool); err != nil {
				return nil
			}
			return findCondition(pool.Status.Conditions, "Ready")
		}, timeout, interval).ShouldNot(BeNil())

		var pool farmv1alpha1.DevicePool
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "no-class-pool", Namespace: namespace}, &pool)).To(Succeed())
		Expect(findCondition(pool.Status.Conditions, "Ready").Reason).To(Equal("DeviceClassNotFound"))

		var list farmv1alpha1.DeviceList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(namespace),
			client.MatchingLabels{LabelPool: "no-class-pool"})).To(Succeed())
		Expect(list.Items).To(BeEmpty())
	})
})

// findCondition returns the condition of the given type, or nil.
func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}
