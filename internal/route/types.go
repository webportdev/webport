package route

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// validNamePattern matches valid project/branch name components
// Must start with alphanumeric, followed by alphanumeric, dash, or underscore
var validNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidProjectName checks if a project name is valid for use in domains
func ValidProjectName(name string) bool {
	return name != "" && validNamePattern.MatchString(name)
}

// ValidBranchName checks if a branch name is valid
// Branch names can contain slashes (e.g., "feature/auth") which are converted to dashes
func ValidBranchName(name string) bool {
	if name == "" {
		return false
	}
	// Check each path component (split by slashes)
	for _, part := range strings.Split(name, "/") {
		if part == "" || !validNamePattern.MatchString(part) {
			return false
		}
	}
	return true
}

// Route represents a single reverse proxy route
type Route struct {
	Project   string    `json:"project"`   // e.g., "myapp"
	Branch    string    `json:"branch"`    // e.g., "feature/auth"
	Port      int       `json:"port"`      // e.g., 3000
	Domain    string    `json:"domain"`    // e.g., "myapp-feature-auth.mond.boo"
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"` // TTL-based expiration
}

// RouteID is the composite key for routes
type RouteID struct {
	Project string
	Branch  string
}

// String returns the string representation used in URLs
// Format: {project}:{branch} where branch slashes are replaced with dashes
// Using colon as delimiter avoids ambiguity with dashes in project/branch names
func (r RouteID) String() string {
	branchSlug := strings.ReplaceAll(r.Branch, "/", "-")
	return fmt.Sprintf("%s:%s", r.Project, branchSlug)
}

// RouteIDFromString parses a RouteID from its string representation
func RouteIDFromString(s string) (RouteID, error) {
	// Split on colon delimiter (unambiguous since colons aren't valid in project/branch names)
	colonIdx := strings.Index(s, ":")
	if colonIdx <= 0 {
		return RouteID{}, fmt.Errorf("invalid route ID format: %s", s)
	}

	project := s[:colonIdx]
	branchSlug := s[colonIdx+1:]
	// Convert branch slug back to branch name (replace - with /)
	branch := strings.ReplaceAll(branchSlug, "-", "/")

	return RouteID{Project: project, Branch: branch}, nil
}

// RegisterRequest is the payload for POST /routes
type RegisterRequest struct {
	Project string `json:"project" validate:"required"`
	Branch  string `json:"branch"  validate:"required"`
	Port    int    `json:"port"    validate:"required,min=1,max=65535"`
	TTL     int    `json:"ttl"`    // Seconds, defaults to WEBPORT_DEFAULT_TTL
}

// Validate checks if the request is valid
func (r *RegisterRequest) Validate() error {
	if r.Project == "" {
		return fmt.Errorf("project is required")
	}
	if !ValidProjectName(r.Project) {
		return fmt.Errorf("project name '%s' contains invalid characters (only alphanumeric, dash, underscore allowed)", r.Project)
	}
	if r.Branch == "" {
		return fmt.Errorf("branch is required")
	}
	if !ValidBranchName(r.Branch) {
		return fmt.Errorf("branch name '%s' contains invalid characters (only alphanumeric, dash, underscore, and forward slash allowed)", r.Branch)
	}
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

// HeartbeatRequest is the payload for POST /routes/{id}/heartbeat
type HeartbeatRequest struct {
	TTL int `json:"ttl"` // Optional new TTL
}

// RoutesList is the response for GET /routes
type RoutesList struct {
	Routes []Route `json:"routes"`
	Total  int     `json:"total"`
}
