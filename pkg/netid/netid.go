package netid

/*
Accessor methods to annotate namespace for multitenant support
*/

import (
	"fmt"
	"math"
	"strconv"

	kapi "k8s.io/kubernetes/pkg/api"
)

const (
	// Current assigned VNID for the namespace
	VNIDAnnotation string = "pod.network.openshift.io/multitenant.vnid"
	// Desired VNID for the namespace
	WantsVNIDAnnotation string = "pod.network.openshift.io/multitenant.wants-vnid"
)

var (
	ErrorVNIDNotFound = fmt.Errorf("VNID/WantsVNID annotation not found")
)

func getVNIDAnnotation(ns *kapi.Namespace, annotationKey string) (uint, error) {
	value, ok := ns.Annotations[annotationKey]
	if !ok {
		return math.MaxUint32, ErrorVNIDNotFound
	}

	id, err := strconv.ParseUint(value, 10, 32)
	return uint(id), err
}

func setVNIDAnnotation(ns *kapi.Namespace, annotationKey string, id uint) {
	if ns.Annotations == nil {
		ns.Annotations = make(map[string]string)
	}
	ns.Annotations[annotationKey] = string(id)
}

func GetVNID(ns *kapi.Namespace) (uint, error) {
	return getVNIDAnnotation(ns, VNIDAnnotation)
}

func SetVNID(ns *kapi.Namespace, id uint) {
	setVNIDAnnotation(ns, VNIDAnnotation, id)
}

func GetWantsVNID(ns *kapi.Namespace) (uint, error) {
	return getVNIDAnnotation(ns, WantsVNIDAnnotation)
}

func SetWantsVNID(ns *kapi.Namespace, id uint) {
	setVNIDAnnotation(ns, WantsVNIDAnnotation, id)
}

func DeleteWantsVNID(ns *kapi.Namespace) {
	delete(ns.Annotations, WantsVNIDAnnotation)
}
