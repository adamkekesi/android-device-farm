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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	farmv1alpha1 "github.com/adamkekesi/android-device-farm/operator/api/v1alpha1"
)

var _ = Describe("Device controller", func() {
	const (
		className = "dev-class"
		namespace = "default"
		timeout   = 20 * time.Second
		interval  = 200 * time.Millisecond
	)

	var deviceName string

	getPod := func(d *farmv1alpha1.Device) (*corev1.Pod, error) {
		var pod corev1.Pod
		err := k8sClient.Get(ctx, client.ObjectKey{Name: d.Name + "-emulator", Namespace: namespace}, &pod)
		return &pod, err
	}
	getDevice := func() *farmv1alpha1.Device {
		var d farmv1alpha1.Device
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: deviceName, Namespace: namespace}, &d)).To(Succeed())
		return &d
	}

	BeforeEach(func() {
		Expect(k8sClient.Create(ctx, &farmv1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
			Spec: farmv1alpha1.DeviceClassSpec{
				ProviderType: farmv1alpha1.ProviderEmulator,
				Image:        "example/emulator:34",
				NodeSelector: map[string]string{"farm.example.com/kvm": "true"},
			},
		})).To(Succeed())

		deviceName = "dev-" + string(uuidish())
		Expect(k8sClient.Create(ctx, &farmv1alpha1.Device{
			ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespace},
			Spec: farmv1alpha1.DeviceSpec{
				ClassRef:     className,
				ProviderType: farmv1alpha1.ProviderEmulator,
			},
		})).To(Succeed())
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &farmv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespace}})
		_ = k8sClient.Delete(ctx, &farmv1alpha1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: className}})
		_ = k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(namespace))
	})

	It("creates an emulator Pod with KVM passthrough and an adb Service", func() {
		d := &farmv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespace}}

		Eventually(func() error {
			_, err := getPod(d)
			return err
		}, timeout, interval).Should(Succeed())

		pod, _ := getPod(d)
		c := pod.Spec.Containers[0]
		Expect(c.Image).To(Equal("example/emulator:34"))
		Expect(c.SecurityContext.Privileged).NotTo(BeNil())
		Expect(*c.SecurityContext.Privileged).To(BeTrue())
		Expect(c.VolumeMounts).To(ContainElement(HaveField("MountPath", "/dev/kvm")))
		Expect(pod.Spec.Volumes).To(ContainElement(HaveField("Name", "kvm")))
		Expect(pod.Spec.NodeSelector).To(HaveKeyWithValue("farm.example.com/kvm", "true"))
		Expect(pod.OwnerReferences).To(HaveLen(1))

		By("creating the adb Service and publishing the endpoint")
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Name: deviceName + "-adb", Namespace: namespace}, &corev1.Service{})
		}, timeout, interval).Should(Succeed())
		Eventually(func() string { return getDevice().Status.ADBEndpoint }, timeout, interval).
			Should(Equal(deviceName + "-adb." + namespace + ".svc.cluster.local:5555"))
	})

	It("transitions to Ready when the emulator Pod becomes Ready", func() {
		d := &farmv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespace}}
		Eventually(func() bool { return getDevice().Status.Phase == farmv1alpha1.DeviceProvisioning },
			timeout, interval).Should(BeTrue())

		// envtest has no kubelet, so simulate the pod becoming Ready.
		var pod *corev1.Pod
		Eventually(func() error { var e error; pod, e = getPod(d); return e }, timeout, interval).Should(Succeed())
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		Eventually(func() farmv1alpha1.DevicePhase { return getDevice().Status.Phase },
			timeout, interval).Should(Equal(farmv1alpha1.DeviceReady))
	})

	It("recreates the emulator Pod if it is deleted", func() {
		d := &farmv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespace}}
		var first *corev1.Pod
		Eventually(func() error { var e error; first, e = getPod(d); return e }, timeout, interval).Should(Succeed())

		Expect(k8sClient.Delete(ctx, first)).To(Succeed())
		Eventually(func() bool {
			p, err := getPod(d)
			return err == nil && p.UID != first.UID
		}, timeout, interval).Should(BeTrue())
	})

	It("marks the Device Failed when its class is missing", func() {
		orphan := "orphan-" + string(uuidish())
		Expect(k8sClient.Create(ctx, &farmv1alpha1.Device{
			ObjectMeta: metav1.ObjectMeta{Name: orphan, Namespace: namespace},
			Spec:       farmv1alpha1.DeviceSpec{ClassRef: "ghost", ProviderType: farmv1alpha1.ProviderEmulator},
		})).To(Succeed())
		defer func() {
			_ = k8sClient.Delete(ctx, &farmv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: orphan, Namespace: namespace}})
		}()

		Eventually(func() farmv1alpha1.DevicePhase {
			var d farmv1alpha1.Device
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: orphan, Namespace: namespace}, &d); err != nil {
				return ""
			}
			return d.Status.Phase
		}, timeout, interval).Should(Equal(farmv1alpha1.DeviceFailed))

		_, err := getPod(&farmv1alpha1.Device{ObjectMeta: metav1.ObjectMeta{Name: orphan}})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

// uuidish returns a short unique-ish suffix so parallel specs don't collide.
var uuidCounter int

func uuidish() []byte {
	uuidCounter++
	return []byte{byte('a' + uuidCounter%26), byte('a' + (uuidCounter/26)%26)}
}
