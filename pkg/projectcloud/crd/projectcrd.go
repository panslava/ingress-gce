package crd

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Project mocks MultiProject CRD.
type Project struct {
	ProjectID         string
	Network           string
	Subnetwork        string
	DeletionTimestamp *metav1.Time
}
