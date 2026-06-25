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

// EvictionPolicy decides how room is made when at capacity.
// +kubebuilder:validation:Enum=LRUIdle;None
type EvictionPolicy string

const (
	// EvictLRUIdle evicts the least-recently-used idle device of another class.
	EvictLRUIdle EvictionPolicy = "LRUIdle"
	// EvictNone never evicts; requests wait for capacity.
	EvictNone EvictionPolicy = "None"
)

// PoolClass references a DeviceClass with per-class warm bounds.
type PoolClass struct {
	// Name of the (cluster-scoped) DeviceClass.
	Name string `json:"name"`

	// MinWarm is the number of ready devices to keep idle.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	MinWarm int32 `json:"minWarm"`

	// MaxWarm caps idle devices of this class (0 = no extra cap beyond the pool).
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxWarm int32 `json:"maxWarm,omitempty"`
}

// STFRef describes how to reach the STF control plane for registration.
type STFRef struct {
	// URL is the STF base URL (e.g. http://devicefarmer-app:3000).
	// +optional
	URL string `json:"url,omitempty"`

	// ADBHost is the adb host STF providers connect to.
	// +optional
	ADBHost string `json:"adbHost,omitempty"`
}

// DevicePoolSpec defines capacity and policy for a set of classes.
type DevicePoolSpec struct {
	// Classes the pool can provision, each with warm bounds.
	// +kubebuilder:validation:MinItems=1
	Classes []PoolClass `json:"classes"`

	// MaxConcurrent is the global cap on running devices in this pool.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	MaxConcurrent int32 `json:"maxConcurrent"`

	// EvictionPolicy decides how room is made when at capacity.
	// +kubebuilder:default=LRUIdle
	// +optional
	EvictionPolicy EvictionPolicy `json:"evictionPolicy,omitempty"`

	// STFRef is how ready devices are registered into STF.
	// +optional
	STFRef *STFRef `json:"stfRef,omitempty"`
}

// ClassCounts is the per-class device tally.
type ClassCounts struct {
	Class        string `json:"class"`
	Total        int32  `json:"total"`
	Ready        int32  `json:"ready"`
	Leased       int32  `json:"leased"`
	Provisioning int32  `json:"provisioning"`
}

// DevicePoolStatus defines the observed state of DevicePool.
type DevicePoolStatus struct {
	// PerClass holds the device tally per class.
	// +optional
	PerClass []ClassCounts `json:"perClass,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dfp
// +kubebuilder:printcolumn:name="MaxConcurrent",type=integer,JSONPath=`.spec.maxConcurrent`
// +kubebuilder:printcolumn:name="Eviction",type=string,JSONPath=`.spec.evictionPolicy`

// DevicePool is the Schema for the devicepools API
type DevicePool struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of DevicePool
	// +required
	Spec DevicePoolSpec `json:"spec"`

	// status defines the observed state of DevicePool
	// +optional
	Status DevicePoolStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DevicePoolList contains a list of DevicePool
type DevicePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DevicePool `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DevicePool{}, &DevicePoolList{})
		return nil
	})
}
