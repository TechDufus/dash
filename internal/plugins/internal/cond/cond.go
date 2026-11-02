// Package cond reads the status.conditions of unstructured Kubernetes objects: the shared reader
// of the plugins that watch custom resources (certificates, external secrets, Flux objects).
//
// A plugin imports no other plugin, so this is where the plugins that watch custom resources
// share it.
package cond

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Of returns the status and message of a named status.condition of an unstructured object.
func Of(u *unstructured.Unstructured, typ string) (status, message string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != typ {
			continue
		}
		status, _ = cm["status"].(string)
		message, _ = cm["message"].(string)
		return status, message
	}
	return "", ""
}

// True reports whether the named condition has status "True".
func True(u *unstructured.Unstructured, typ string) bool {
	s, _ := Of(u, typ)
	return s == "True"
}

// Time returns the lastTransitionTime of a status condition, in UTC, or nil when the condition
// or its time is missing or malformed.
func Time(u *unstructured.Unstructured, typ string) *time.Time {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != typ {
			continue
		}
		if s, _ := cm["lastTransitionTime"].(string); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return new(t.UTC())
			}
		}
	}
	return nil
}
