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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	farmv1alpha1 "github.com/adamkekesi/android-device-farm/operator/api/v1alpha1"
)

const (
	// adbPort is the TCP port the emulator exposes for adb.
	adbPort int32 = 5555
	// kvmHostPath is the host device required for hardware acceleration.
	kvmHostPath = "/dev/kvm"
	// labelDevice ties a Pod/Service back to its Device.
	labelDevice = "farm.example.com/device"
	// adbServerPort is the port STF's adb server listens on.
	adbServerPort = "5037"
	// adbClientImage carries an adb client for the registration Job.
	adbClientImage = "devicefarmer/adb:latest"
	// adbProxyPort is the pod-network port a socat sidecar re-exposes the
	// emulator's localhost-only adb port on.
	adbProxyPort int32 = 5556
	// socatImage is the TCP-forwarder sidecar image.
	socatImage = "alpine/socat:latest"
)

// DeviceReconciler reconciles a Device object: it backs each Device with an
// emulator Pod and an adb Service, and tracks readiness via the Pod's
// boot-completed probe. STF registration is the next increment.
type DeviceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=farm.example.com,resources=devices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=farm.example.com,resources=devices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=farm.example.com,resources=devices/finalizers,verbs=update
// +kubebuilder:rbac:groups=farm.example.com,resources=deviceclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=farm.example.com,resources=devicepools,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile backs a Device with an emulator Pod + adb Service and reflects pod
// readiness in the Device phase.
func (r *DeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var device farmv1alpha1.Device
	if err := r.Get(ctx, req.NamespacedName, &device); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if device.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	var dc farmv1alpha1.DeviceClass
	if err := r.Get(ctx, client.ObjectKey{Name: device.Spec.ClassRef}, &dc); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("DeviceClass not found", "class", device.Spec.ClassRef)
			return r.setPhase(ctx, &device, farmv1alpha1.DeviceFailed, "", "DeviceClassNotFound",
				fmt.Sprintf("DeviceClass %q does not exist", device.Spec.ClassRef))
		}
		return ctrl.Result{}, err
	}

	if err := r.ensureService(ctx, &device); err != nil {
		return ctrl.Result{}, err
	}
	pod, err := r.ensurePod(ctx, &device, &dc)
	if err != nil {
		return ctrl.Result{}, err
	}

	phase, reason, msg := phaseForPod(pod)
	adb := fmt.Sprintf("%s.%s.svc.cluster.local:%d", adbServiceName(&device), device.Namespace, adbPort)

	// Once the emulator is up, register it into STF by connecting its adb endpoint
	// to STF's adb server (the provider then picks it up).
	if phase == farmv1alpha1.DeviceReady {
		if err := r.ensureRegistration(ctx, &device, adb); err != nil {
			return ctrl.Result{}, err
		}
	}

	return r.setPhase(ctx, &device, phase, adb, reason, msg)
}

// ensureRegistration creates an idempotent Job that `adb connect`s the device to
// STF's adb server, if the owning pool declares an stfRef.adbHost.
func (r *DeviceReconciler) ensureRegistration(ctx context.Context, device *farmv1alpha1.Device, adbEndpoint string) error {
	if device.Spec.PoolRef == "" {
		return nil
	}
	var pool farmv1alpha1.DevicePool
	if err := r.Get(ctx, client.ObjectKey{Name: device.Spec.PoolRef, Namespace: device.Namespace}, &pool); err != nil {
		return client.IgnoreNotFound(err)
	}
	if pool.Spec.STFRef == nil || pool.Spec.STFRef.ADBHost == "" {
		return nil
	}

	jobName := device.Name + "-register"
	if err := r.Get(ctx, client.ObjectKey{Name: jobName, Namespace: device.Namespace}, &batchv1.Job{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	backoff := int32(5)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: device.Namespace,
			Labels:    deviceSelector(device),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{{
						Name:    "register",
						Image:   adbClientImage,
						Command: []string{"adb", "-H", pool.Spec.STFRef.ADBHost, "-P", adbServerPort, "connect", adbEndpoint},
					}},
				},
			},
		},
	}
	if err := ctrl.SetControllerReference(device, job, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, job)
}

func adbServiceName(d *farmv1alpha1.Device) string  { return d.Name + "-adb" }
func emulatorPodName(d *farmv1alpha1.Device) string { return d.Name + "-emulator" }

func deviceSelector(d *farmv1alpha1.Device) map[string]string {
	return map[string]string{labelDevice: d.Name}
}

func (r *DeviceReconciler) ensureService(ctx context.Context, device *farmv1alpha1.Device) error {
	key := client.ObjectKey{Name: adbServiceName(device), Namespace: device.Namespace}
	var existing corev1.Service
	if err := r.Get(ctx, key, &existing); err == nil {
		// Reconcile the target port (e.g. after an operator upgrade changed it).
		if len(existing.Spec.Ports) == 1 && existing.Spec.Ports[0].TargetPort != intstr.FromInt32(adbProxyPort) {
			existing.Spec.Ports[0].TargetPort = intstr.FromInt32(adbProxyPort)
			return r.Update(ctx, &existing)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      adbServiceName(device),
			Namespace: device.Namespace,
			Labels:    deviceSelector(device),
		},
		Spec: corev1.ServiceSpec{
			Selector: deviceSelector(device),
			Ports: []corev1.ServicePort{{
				Name:       "adb",
				Port:       adbPort,
				TargetPort: intstr.FromInt32(adbProxyPort),
			}},
		},
	}
	if err := ctrl.SetControllerReference(device, svc, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, svc)
}

func (r *DeviceReconciler) ensurePod(ctx context.Context, device *farmv1alpha1.Device, dc *farmv1alpha1.DeviceClass) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	key := client.ObjectKey{Name: emulatorPodName(device), Namespace: device.Namespace}
	if err := r.Get(ctx, key, pod); err == nil {
		return pod, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	pod = r.buildPod(device, dc)
	if err := ctrl.SetControllerReference(device, pod, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, pod); err != nil {
		return nil, err
	}
	return pod, nil
}

func (r *DeviceReconciler) buildPod(device *farmv1alpha1.Device, dc *farmv1alpha1.DeviceClass) *corev1.Pod {
	privileged := true
	hostPathType := corev1.HostPathCharDev

	probe := dc.Spec.ReadinessProbe
	if probe == nil {
		probe = defaultBootCompletedProbe()
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      emulatorPodName(device),
			Namespace: device.Namespace,
			Labels:    deviceSelector(device),
		},
		Spec: corev1.PodSpec{
			NodeSelector: dc.Spec.NodeSelector,
			Tolerations:  dc.Spec.Tolerations,
			Containers: []corev1.Container{{
				Name:            "emulator",
				Image:           dc.Spec.Image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Ports:           []corev1.ContainerPort{{Name: "adb", ContainerPort: adbPort}},
				ReadinessProbe:  probe,
				Resources:       dc.Spec.Resources,
				SecurityContext: &corev1.SecurityContext{
					// /dev/kvm passthrough requires a privileged container.
					Privileged: &privileged,
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "kvm", MountPath: kvmHostPath}},
			}, {
				// Re-exposes the emulator's localhost-only adb port on the pod
				// network so STF's adb server can `adb connect` to it.
				Name:  "adb-proxy",
				Image: socatImage,
				Args: []string{
					fmt.Sprintf("tcp-listen:%d,fork,reuseaddr", adbProxyPort),
					fmt.Sprintf("tcp:127.0.0.1:%d", adbPort),
				},
				Ports: []corev1.ContainerPort{{Name: "adb", ContainerPort: adbProxyPort}},
			}},
			Volumes: []corev1.Volume{{
				Name: "kvm",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: kvmHostPath, Type: &hostPathType},
				},
			}},
		},
	}
}

func defaultBootCompletedProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"sh", "-c", "adb shell getprop sys.boot_completed | grep -q 1"},
			},
		},
		InitialDelaySeconds: 30,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
		FailureThreshold:    30,
	}
}

// phaseForPod maps a Pod's state to a Device phase.
func phaseForPod(pod *corev1.Pod) (farmv1alpha1.DevicePhase, string, string) {
	switch pod.Status.Phase {
	case corev1.PodFailed:
		return farmv1alpha1.DeviceFailed, "PodFailed", "emulator pod failed"
	case corev1.PodSucceeded:
		return farmv1alpha1.DeviceFailed, "PodExited", "emulator pod exited unexpectedly"
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return farmv1alpha1.DeviceReady, "BootCompleted", "emulator booted and ready"
		}
	}
	return farmv1alpha1.DeviceProvisioning, "Provisioning", "waiting for emulator to boot"
}

func (r *DeviceReconciler) setPhase(ctx context.Context, device *farmv1alpha1.Device, phase farmv1alpha1.DevicePhase, adb, reason, msg string) (ctrl.Result, error) {
	changed := device.Status.Phase != phase
	device.Status.Phase = phase
	if adb != "" && device.Status.ADBEndpoint != adb {
		device.Status.ADBEndpoint = adb
		changed = true
	}
	condStatus := metav1.ConditionFalse
	if phase == farmv1alpha1.DeviceReady {
		condStatus = metav1.ConditionTrue
	}
	if meta.SetStatusCondition(&device.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             condStatus,
		ObservedGeneration: device.Generation,
		Reason:             reason,
		Message:            msg,
	}) {
		changed = true
	}
	if !changed {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Update(ctx, device)
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeviceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&farmv1alpha1.Device{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.Service{}).
		Owns(&batchv1.Job{}).
		Named("device").
		Complete(r)
}
