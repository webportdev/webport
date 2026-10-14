package tui

import "github.com/webportdev/webport/internal/devsession/state"

// InstanceName is the display name used for an instance in every dashboard.
func InstanceName(live state.LiveState) string { return instanceName(live) }

// InstanceURL returns the first published URL for service, or for any
// service when service is empty.
func InstanceURL(live state.LiveState, service string) string { return instanceURL(live, service) }

// ValidHTTPURL reports whether value is an HTTP(S) URL safe to link to.
func ValidHTTPURL(value string) bool { return validHTTPURL(value) }

// Clean returns value as plain text without terminal sequences or controls.
func Clean(value string) string { return clean(value) }
