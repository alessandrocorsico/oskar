package checks

import (
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptr[T any](v T) *T { return &v }

// testSlice builds an EndpointSlice as the core EndpointSlice controller
// would write it: labelled with the service name and managed-by.
func testSlice(ns, name, service, address, targetPod string) discoveryv1.EndpointSlice {
	return discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: service,
				discoveryv1.LabelManagedBy:   endpointSliceController,
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses: []string{address},
				TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: targetPod, Namespace: ns},
			},
		},
	}
}

func testPodWithIP(ns, name, ip string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     corev1.PodStatus{PodIP: ip, PodIPs: []corev1.PodIP{{IP: ip}}},
	}
}

func testService(ns, name string, selector map[string]string) corev1.Service {
	return corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.ServiceSpec{Selector: selector},
	}
}

func testPod(ns, name string, labels map[string]string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
	}
}

func annotateIgnore(meta *metav1.ObjectMeta, value string) {
	meta.Annotations = map[string]string{IgnoreAnnotation: value}
}
