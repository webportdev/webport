// Package webui is the daemon's optional browser dashboard. It shows the
// sessions connected to the daemon's authenticated Unix socket, the daemon's
// routes, and its status, and publishes itself on a configurable subdomain
// of the base domain through a daemon-owned system route.
package webui

import (
	"errors"
	"fmt"
	"os"

	"github.com/webportdev/webport/internal/devsession/state"
	"github.com/webportdev/webport/internal/route"
)

const (
	// DefaultSubdomain is the hostname label used when none is configured.
	DefaultSubdomain = "webport"
	// RouteProject and RouteBranch form the dashboard's route identity. The
	// hostname comes from the configured subdomain, never from this identity.
	RouteProject = "webport"
	RouteBranch  = "webui"
)

// Settings are the daemon's persistent web UI preferences.
type Settings struct {
	Enabled      bool   `json:"enabled"`
	Subdomain    string `json:"subdomain,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
}

// EffectiveSubdomain returns the configured subdomain or the default.
func (s Settings) EffectiveSubdomain() string {
	if s.Subdomain == "" {
		return DefaultSubdomain
	}
	return s.Subdomain
}

// HasPassword reports whether a password has been set.
func (s Settings) HasPassword() bool { return s.PasswordHash != "" }

// ValidateSubdomain checks that value is one lowercase DNS label.
func ValidateSubdomain(value string) error {
	if !route.ValidSubdomain(value) {
		return fmt.Errorf("subdomain %q must be one lowercase DNS label of letters, digits, and inner dashes", value)
	}
	return nil
}

// Validate checks every stored field.
func (s Settings) Validate() error {
	var errs []error
	if s.Subdomain != "" {
		errs = append(errs, ValidateSubdomain(s.Subdomain))
	}
	if s.PasswordHash != "" {
		if _, err := parseHash(s.PasswordHash); err != nil {
			errs = append(errs, err)
		}
	}
	if s.Enabled && !s.HasPassword() {
		errs = append(errs, errors.New("set a web UI password before enabling it (webport webui password)"))
	}
	return errors.Join(errs...)
}

// LoadSettings reads path. A missing file yields default settings.
func LoadSettings(path string) (Settings, error) {
	settings, err := state.ReadJSON[Settings](path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read web UI settings %s: %w", path, err)
	}
	if err := settings.Validate(); err != nil {
		return Settings{}, fmt.Errorf("web UI settings %s: %w", path, err)
	}
	return settings, nil
}

// SaveSettings atomically replaces path with a mode-0600 file.
func SaveSettings(path string, settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	return state.WriteJSONAtomic(path, settings)
}
