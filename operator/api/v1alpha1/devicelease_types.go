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

// LeasePhase is the lifecycle phase of a DeviceLease.
// +kubebuilder:validation:Enum=Pending;Bound;Expired;Released;Failed
type LeasePhase string

const (
	LeasePending  LeasePhase = "Pending"
	LeaseBound    LeasePhase = "Bound"
	LeaseExpired  LeasePhase = "Expired"
	LeaseReleased LeasePhase = "Released"
	LeaseFailed   LeasePhase = "Failed"
)

// DeviceLeaseSpec is a workflow's claim on a device of a given class.
type DeviceLeaseSpec struct {
	// ClassRef is the requested DeviceClass.
	ClassRef string `json:"classRef"`

	// PoolRef optionally pins the lease to a specific DevicePool.
	// +optional
	PoolRef string `json:"poolRef,omitempty"`

	// Requester is an opaque identifier (CI job id, user).
	Requester string `json:"requester"`

	// TTLSeconds is the lease lifetime; refreshed by heartbeats.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=300
	TTLSeconds int32 `json:"ttlSeconds"`
}

// DeviceLeaseStatus is the observed state of a DeviceLease.
type DeviceLeaseStatus struct {
	// Phase is the lease lifecycle phase.
	// +optional
	Phase LeasePhase `json:"phase,omitempty"`

	// DeviceRef is the bound Device (namespaced name).
	// +optional
	DeviceRef string `json:"deviceRef,omitempty"`

	// ADBEndpoint is the bound device's adb endpoint.
	// +optional
	ADBEndpoint string `json:"adbEndpoint,omitempty"`

	// UIEndpoint is the bound device's optional viewer URL.
	// +optional
	UIEndpoint string `json:"uiEndpoint,omitempty"`

	// ExpiresAt is when the lease expires absent a heartbeat.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// LastHeartbeat is the last time the lease was refreshed.
	// +optional
	LastHeartbeat *metav1.Time `json:"lastHeartbeat,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dfl
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Class",type=string,JSONPath=`.spec.classRef`
// +kubebuilder:printcolumn:name="Device",type=string,JSONPath=`.status.deviceRef`
// +kubebuilder:printcolumn:name="Requester",type=string,JSONPath=`.spec.requester`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.status.expiresAt`

// DeviceLease is the Schema for the deviceleases API
type DeviceLease struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of DeviceLease
	// +required
	Spec DeviceLeaseSpec `json:"spec"`

	// status defines the observed state of DeviceLease
	// +optional
	Status DeviceLeaseStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DeviceLeaseList contains a list of DeviceLease
type DeviceLeaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DeviceLease `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DeviceLease{}, &DeviceLeaseList{})
		return nil
	})
}
