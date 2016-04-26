// Accessor methods to annotate namespace for multitenant support
package netid

import (
	"fmt"
	"strconv"

	kapi "k8s.io/kubernetes/pkg/api"
)

const (
	// Maximum VXLAN Virtual Network Identifier(VNID) as per RFC#7348
	MaxVNID = uint((1 << 24) - 1)
	// VNID: 1 to 9 are internally reserved for any special cases in the future
	MinVNID = 10
	// VNID: 0 reserved for default namespace and can reach any network in the cluster
	GlobalVNID = uint(0)

	// Current assigned VNID for the namespace
	VNIDAnnotation string = "pod.network.openshift.io/multitenant.vnid"
	// Desired VNID for the namespace
	RequestedVNIDAnnotation string = "pod.network.openshift.io/multitenant.requested-vnid"
)

var (
	ErrorVNIDNotFound = fmt.Errorf("VNID or RequestedVNID annotation not found")
)

func ValidVNID(vnid uint) error {
	if vnid == GlobalVNID {
		return nil
	}
	if vnid < MinVNID {
		return fmt.Errorf("VNID must be greater than or equal to %d", MinVNID)
	}
	if vnid > MaxVNID {
		return fmt.Errorf("VNID must be less than or equal to %d", MaxVNID)
	}
	return nil
}

func GetVNID(ns *kapi.Namespace) (uint, error) {
	return getVNIDAnnotation(ns, VNIDAnnotation)
}

func SetVNID(ns *kapi.Namespace, id uint) error {
	return setVNIDAnnotation(ns, VNIDAnnotation, id)
}

func DeleteVNID(ns *kapi.Namespace) {
	delete(ns.Annotations, VNIDAnnotation)
}

func GetRequestedVNID(ns *kapi.Namespace) (uint, error) {
	return getVNIDAnnotation(ns, RequestedVNIDAnnotation)
}

func SetRequestedVNID(ns *kapi.Namespace, id uint) error {
	return setVNIDAnnotation(ns, RequestedVNIDAnnotation, id)
}

func DeleteRequestedVNID(ns *kapi.Namespace) {
	delete(ns.Annotations, RequestedVNIDAnnotation)
}

func getVNIDAnnotation(ns *kapi.Namespace, annotationKey string) (uint, error) {
	if ns.Annotations == nil {
		return MaxVNID + 1, ErrorVNIDNotFound
	}
	value, ok := ns.Annotations[annotationKey]
	if !ok {
		return MaxVNID + 1, ErrorVNIDNotFound
	}
	id, err := strconv.ParseUint(value, 10, 32)
	vnid := uint(id)

	if err := ValidVNID(vnid); err != nil {
		return MaxVNID + 1, err
	}
	return vnid, err
}

func setVNIDAnnotation(ns *kapi.Namespace, annotationKey string, id uint) error {
	if err := ValidVNID(id); err != nil {
		return err
	}

	if ns.Annotations == nil {
		ns.Annotations = make(map[string]string)
	}
	ns.Annotations[annotationKey] = strconv.Itoa(int(id))
	return nil
}
