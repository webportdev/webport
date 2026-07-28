package route

import (
	"fmt"
	"strings"
)

// BuildDomain constructs the public hostname for a route.
func BuildDomain(baseDomain, project, branch string) string {
	branchSlug := strings.ReplaceAll(branch, "/", "-")
	return fmt.Sprintf("%s-%s.%s", project, branchSlug, baseDomain)
}
