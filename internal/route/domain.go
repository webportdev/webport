package route

import (
	"fmt"
	"strings"
)

// BuildDomain constructs the public hostname for a route.
func BuildDomain(baseDomain, project, branch string) string {
	domain, _ := BuildDomainChecked(baseDomain, project, branch)
	return domain
}

// BuildDomainChecked constructs and validates a DNS-safe route hostname.
func BuildDomainChecked(baseDomain, project, branch string) (string, error) {
	slugPart := func(value string) string {
		value = strings.ToLower(value)
		value = strings.ReplaceAll(value, "/", "-")
		value = strings.ReplaceAll(value, "_", "-")
		return value
	}
	label := slugPart(project) + "-" + slugPart(branch)
	base := strings.ToLower(strings.Trim(baseDomain, "."))
	if len(label) > 63 {
		return "", fmt.Errorf("generated hostname label is %d characters; maximum is 63", len(label))
	}
	domain := label + "." + base
	if len(domain) > 253 {
		return "", fmt.Errorf("generated hostname is %d characters; maximum is 253", len(domain))
	}
	return domain, nil
}

// BuildRouteDomain returns the hostname for a route, using subdomain as the
// complete hostname label when it is set.
func BuildRouteDomain(baseDomain, project, branch, subdomain string) (string, error) {
	if subdomain == "" {
		return BuildDomainChecked(baseDomain, project, branch)
	}
	domain := strings.ToLower(subdomain) + "." + strings.ToLower(strings.Trim(baseDomain, "."))
	if len(domain) > 253 {
		return "", fmt.Errorf("generated hostname is %d characters; maximum is 253", len(domain))
	}
	return domain, nil
}
