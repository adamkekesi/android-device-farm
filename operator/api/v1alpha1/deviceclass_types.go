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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ProviderType identifies how a device is backed.
// +kubebuilder:validation:Enum=emulator;physical
type ProviderType string

const (
	ProviderEmulator ProviderType = "emulator"
	ProviderPhysical ProviderType = "physical"
)

// AVDSpec describes the emulated device profile and emulator parameters.
type AVDSpec struct {
	// Emulator skin (e.g. "pixel_5").
	// +optional
	Skin string `json:"skin,omitempty"`

	// Screen density in dpi.
	// +optional
	Density int32 `json:"density,omitempty"`

	// Extra arguments passed verbatim to the emulator binary.
	// +optional
	ExtraArgs []string `json:"extraArgs,omitempty"`
}

// DeviceClassSpec defines a catalogued device type.
type DeviceClassSpec struct {
	// ProviderType selects the backing implementation.
	// +kubebuilder:default=emulator
	ProviderType ProviderType `json:"providerType"`

	// Image is the emulator container image (emulator providers only).
	// +optional
	Image string `json:"image,omitempty"`

	// APILevel is the Android API level, used for selection and labelling.
	// +optional
	APILevel int32 `json:"apiLevel,omitempty"`

	// Arch is the device ABI.
	// +kubebuilder:validation:Enum=x86;x86_64;arm64-v8a
	// +optional
	Arch string `json:"arch,omitempty"`

	// AVD holds the device profile and emulator parameters.
	// +optional
	AVD *AVDSpec `json:"avd,omitempty"`

	// Resources are the pod requests/limits for a device of this class.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// ReadinessProbe overrides the default boot-completed readiness check.
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`

	// NodeSelector targets KVM- or USB-host nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations let device pods schedule onto tainted (e.g. dedicated) nodes.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// DeviceClassStatus defines the observed state of DeviceClass.
type DeviceClassStatus struct {
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// shortName "dfc" disambiguates from the built-in resource.k8s.io/DeviceClass (DRA).
// +kubebuilder:resource:scope=Cluster,shortName=dfc
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.providerType`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
// +kubebuilder:printcolumn:name="API",type=integer,JSONPath=`.spec.apiLevel`
// +kubebuilder:printcolumn:name="Arch",type=string,JSONPath=`.spec.arch`

// DeviceClass is the catalog entry for a device type.
type DeviceClass struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of DeviceClass
	// +required
	Spec DeviceClassSpec `json:"spec"`

	// status defines the observed state of DeviceClass
	// +optional
	Status DeviceClassStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DeviceClassList contains a list of DeviceClass
type DeviceClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DeviceClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DeviceClass{}, &DeviceClassList{})
		return nil
	})
}
