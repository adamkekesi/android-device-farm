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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DevicePhase is the lifecycle phase of a Device.
// +kubebuilder:validation:Enum=Provisioning;Ready;Leased;Draining;Failed
type DevicePhase string

const (
	DeviceProvisioning DevicePhase = "Provisioning"
	DeviceReady        DevicePhase = "Ready"
	DeviceLeased       DevicePhase = "Leased"
	DeviceDraining     DevicePhase = "Draining"
	DeviceFailed       DevicePhase = "Failed"
)

// DeviceSpec is the desired state of a Device. Devices are created and owned by
// the operator (from a DevicePool), not authored by users.
type DeviceSpec struct {
	// ClassRef is the DeviceClass this device instantiates.
	ClassRef string `json:"classRef"`

	// PoolRef is the owning DevicePool (namespaced name).
	// +optional
	PoolRef string `json:"poolRef,omitempty"`

	// ProviderType is denormalised from the class for scheduling decisions.
	// +kubebuilder:default=emulator
	ProviderType ProviderType `json:"providerType"`

	// ADBEndpoint is set by the physical-device provider (providerType physical):
	// the host:port the attached device is reachable at. Ignored for emulators,
	// which derive their endpoint from the operator-managed adb Service.
	// +optional
	ADBEndpoint string `json:"adbEndpoint,omitempty"`

	// NodeName is the USB-host node a physical device is attached to.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// Serial is the physical device serial (kept stable via udev rules).
	// +optional
	Serial string `json:"serial,omitempty"`
}

// DeviceStatus is the observed state of a Device.
type DeviceStatus struct {
	// Phase is the device lifecycle phase.
	// +optional
	Phase DevicePhase `json:"phase,omitempty"`

	// ADBEndpoint is the host:port to reach the device over adb.
	// +optional
	ADBEndpoint string `json:"adbEndpoint,omitempty"`

	// UIEndpoint is an optional per-device viewer URL.
	// +optional
	UIEndpoint string `json:"uiEndpoint,omitempty"`

	// LeaseRef is the bound DeviceLease, if any.
	// +optional
	LeaseRef string `json:"leaseRef,omitempty"`

	// LastUsedTime drives LRU eviction.
	// +optional
	LastUsedTime *metav1.Time `json:"lastUsedTime,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dfd
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.classRef`
// +kubebuilder:printcolumn:name="ADB",type=string,JSONPath=`.status.adbEndpoint`
// +kubebuilder:printcolumn:name="Lease",type=string,JSONPath=`.status.leaseRef`

// Device is one running device instance, owned by the operator.
type Device struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Device
	// +required
	Spec DeviceSpec `json:"spec"`

	// status defines the observed state of Device
	// +optional
	Status DeviceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DeviceList contains a list of Device
type DeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Device `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Device{}, &DeviceList{})
		return nil
	})
}
