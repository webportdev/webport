package route

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("route lease not found")
	ErrConflict = errors.New("route conflict")
)

// Claim is one client's renewable ownership of a manual route.
type Claim struct {
	LeaseID  string
	ClientID string
	Project  string
	Branch   string
	Port     int
	TTL      time.Duration
	Expires  time.Time
	Legacy   bool
}

// Controller serializes manual-route state changes with Traefik publication.
// Process-discovered routes remain in Store and are preserved by every manual
// reconciliation.
type Controller struct {
	mu sync.Mutex

	store     *Store
	publish   func() error
	claims    map[string]Claim
	clients   map[string]string
	legacyIDs map[RouteID]string

	revision        uint64
	appliedRevision uint64
	lastPublished   time.Time
	lastError       error

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

type ControllerStatus struct {
	Revision        uint64
	AppliedRevision uint64
	LastPublished   time.Time
	LastError       string
	LeaseCount      int
}

func NewController(store *Store, publish func() error) *Controller {
	return &Controller{
		store:     store,
		publish:   publish,
		claims:    make(map[string]Claim),
		clients:   make(map[string]string),
		legacyIDs: make(map[RouteID]string),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

func (c *Controller) SetPublisher(publish func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publish = publish
}

// Initialize publishes the initial snapshot and establishes readiness.
func (c *Controller) Initialize() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revision++
	if c.publish != nil {
		if err := c.publish(); err != nil {
			c.lastError = err
			return err
		}
	}
	c.appliedRevision = c.revision
	c.lastPublished = time.Now()
	c.lastError = nil
	return nil
}

func (c *Controller) RegisterLegacy(req RegisterRequest, baseDomain string, now time.Time, ttl time.Duration) (Route, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := RouteID{Project: req.Project, Branch: req.Branch}
	leaseID, ok := c.legacyIDs[id]
	if !ok {
		leaseID = "legacy:" + id.String()
	}
	claim := Claim{
		LeaseID: leaseID, Project: req.Project, Branch: req.Branch, Port: req.Port,
		TTL: ttl, Expires: now.Add(ttl), Legacy: true,
	}
	previous, hadPrevious := c.claims[leaseID]
	c.claims[leaseID] = claim
	c.legacyIDs[id] = leaseID

	route, err := c.applyLocked(baseDomain, now)
	if err != nil {
		if hadPrevious {
			c.claims[leaseID] = previous
		} else {
			delete(c.claims, leaseID)
			delete(c.legacyIDs, id)
		}
		return Route{}, err
	}
	return route[id], nil
}

func (c *Controller) HeartbeatLegacy(id RouteID, requestedTTL, defaultTTL time.Duration, now time.Time, baseDomain string) (Route, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	leaseID, ok := c.legacyIDs[id]
	if !ok {
		existing, exists := c.store.Get(id)
		if !exists || existing.IsDiscovered() {
			return Route{}, ErrNotFound
		}
		leaseID = "legacy:" + id.String()
		ttl := requestedTTL
		if ttl <= 0 {
			ttl = defaultTTL
		}
		c.claims[leaseID] = Claim{
			LeaseID: leaseID, Project: existing.Project, Branch: existing.Branch,
			Port: existing.Port, TTL: ttl, Expires: now.Add(ttl), Legacy: true,
		}
		c.legacyIDs[id] = leaseID
	}
	claim := c.claims[leaseID]
	if requestedTTL > 0 {
		claim.TTL = requestedTTL
	}
	claim.Expires = now.Add(claim.TTL)
	c.claims[leaseID] = claim
	return c.refreshRouteLocked(id, baseDomain, now)
}

func (c *Controller) DeleteLegacy(id RouteID, now time.Time, baseDomain string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	leaseID, ok := c.legacyIDs[id]
	if !ok {
		existing, exists := c.store.Get(id)
		if !exists || existing.IsDiscovered() {
			return ErrNotFound
		}
		c.store.Delete(id)
		c.revision++
		if c.publish != nil {
			if err := c.publish(); err != nil {
				_ = c.store.Add(existing)
				c.lastError = err
				return err
			}
		}
		c.appliedRevision = c.revision
		c.lastPublished = time.Now()
		c.lastError = nil
		return nil
	}
	previous := c.claims[leaseID]
	delete(c.claims, leaseID)
	delete(c.legacyIDs, id)
	if _, err := c.applyLocked(baseDomain, now); err != nil {
		c.claims[leaseID] = previous
		c.legacyIDs[id] = leaseID
		return err
	}
	return nil
}

// CreateLease registers an idempotent client claim.
func (c *Controller) CreateLease(clientID string, req RegisterRequest, baseDomain string, now time.Time, ttl time.Duration) (Claim, Route, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if clientID == "" {
		return Claim{}, Route{}, false, fmt.Errorf("client_id is required")
	}
	if leaseID, ok := c.clients[clientID]; ok {
		claim := c.claims[leaseID]
		if claim.Project != req.Project || claim.Branch != req.Branch || claim.Port != req.Port {
			return Claim{}, Route{}, false, fmt.Errorf("%w: client_id is already registered with different route data", ErrConflict)
		}
		claim.TTL = ttl
		claim.Expires = now.Add(ttl)
		c.claims[leaseID] = claim
		registered, err := c.refreshRouteLocked(RouteID{Project: req.Project, Branch: req.Branch}, baseDomain, now)
		return claim, registered, false, err
	}

	leaseID, err := newLeaseID()
	if err != nil {
		return Claim{}, Route{}, false, err
	}
	claim := Claim{
		LeaseID: leaseID, ClientID: clientID, Project: req.Project, Branch: req.Branch,
		Port: req.Port, TTL: ttl, Expires: now.Add(ttl),
	}
	c.claims[leaseID] = claim
	c.clients[clientID] = leaseID
	routes, err := c.applyLocked(baseDomain, now)
	if err != nil {
		delete(c.claims, leaseID)
		delete(c.clients, clientID)
		return Claim{}, Route{}, false, err
	}
	return claim, routes[RouteID{Project: req.Project, Branch: req.Branch}], true, nil
}

func (c *Controller) HeartbeatLease(leaseID string, requestedTTL time.Duration, now time.Time, baseDomain string) (Claim, Route, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	claim, ok := c.claims[leaseID]
	if !ok || claim.Legacy {
		return Claim{}, Route{}, ErrNotFound
	}
	if requestedTTL > 0 {
		claim.TTL = requestedTTL
	}
	claim.Expires = now.Add(claim.TTL)
	c.claims[leaseID] = claim
	registered, err := c.refreshRouteLocked(RouteID{Project: claim.Project, Branch: claim.Branch}, baseDomain, now)
	return claim, registered, err
}

func (c *Controller) DeleteLease(leaseID string, now time.Time, baseDomain string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	claim, ok := c.claims[leaseID]
	if !ok || claim.Legacy {
		return nil
	}
	delete(c.claims, leaseID)
	delete(c.clients, claim.ClientID)
	if _, err := c.applyLocked(baseDomain, now); err != nil {
		c.claims[leaseID] = claim
		c.clients[claim.ClientID] = leaseID
		return err
	}
	return nil
}

func (c *Controller) Expire(now time.Time, baseDomain string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var expired []Claim
	for leaseID, claim := range c.claims {
		if !now.Before(claim.Expires) {
			expired = append(expired, claim)
			delete(c.claims, leaseID)
			if claim.Legacy {
				delete(c.legacyIDs, RouteID{Project: claim.Project, Branch: claim.Branch})
			} else {
				delete(c.clients, claim.ClientID)
			}
		}
	}
	if len(expired) == 0 {
		return 0, nil
	}
	if _, err := c.applyLocked(baseDomain, now); err != nil {
		for _, claim := range expired {
			c.claims[claim.LeaseID] = claim
			if claim.Legacy {
				c.legacyIDs[RouteID{Project: claim.Project, Branch: claim.Branch}] = claim.LeaseID
			} else {
				c.clients[claim.ClientID] = claim.LeaseID
			}
		}
		return 0, err
	}
	return len(expired), nil
}

func (c *Controller) Start(interval time.Duration, baseDomain string) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		defer close(c.doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				_, _ = c.Expire(now, baseDomain)
			case <-c.stopCh:
				return
			}
		}
	}()
}

func (c *Controller) Stop() {
	c.stopOnce.Do(func() { close(c.stopCh) })
	<-c.doneCh
}

func (c *Controller) Status() ControllerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := ControllerStatus{
		Revision: c.revision, AppliedRevision: c.appliedRevision,
		LastPublished: c.lastPublished, LeaseCount: len(c.claims),
	}
	if c.lastError != nil {
		status.LastError = c.lastError.Error()
	}
	return status
}

func (c *Controller) Ready() bool {
	status := c.Status()
	return status.AppliedRevision > 0 &&
		status.Revision == status.AppliedRevision &&
		status.LastError == ""
}

func (c *Controller) applyLocked(baseDomain string, now time.Time) (map[RouteID]Route, error) {
	manual := make(map[RouteID]Route)
	domainOwners := make(map[string]RouteID)
	for _, claim := range c.claims {
		if !now.Before(claim.Expires) {
			continue
		}
		id := RouteID{Project: claim.Project, Branch: claim.Branch}
		domain, err := BuildDomainChecked(baseDomain, claim.Project, claim.Branch)
		if err != nil {
			return nil, err
		}
		if owner, exists := domainOwners[strings.ToLower(domain)]; exists && owner != id {
			return nil, fmt.Errorf("%w: routes %s and %s produce hostname %s", ErrConflict, owner.String(), id.String(), domain)
		}
		domainOwners[strings.ToLower(domain)] = id
		if existing, exists := manual[id]; exists {
			if existing.Port != claim.Port {
				return nil, fmt.Errorf("%w: route %s is already claimed on port %d", ErrConflict, id.String(), existing.Port)
			}
			if claim.Expires.After(existing.ExpiresAt) {
				existing.ExpiresAt = claim.Expires
				manual[id] = existing
			}
			continue
		}
		manual[id] = Route{
			Project: claim.Project, Branch: claim.Branch, Port: claim.Port,
			Domain: domain, ExpiresAt: claim.Expires, Source: SourceManual,
		}
	}

	desired := make([]Route, 0, len(manual))
	for _, existing := range c.store.List() {
		if !existing.IsDiscovered() {
			continue
		}
		id := RouteID{Project: existing.Project, Branch: existing.Branch}
		if owner, exists := domainOwners[strings.ToLower(existing.Domain)]; exists && owner != id {
			return nil, fmt.Errorf("%w: routes %s and %s produce hostname %s", ErrConflict, owner.String(), id.String(), existing.Domain)
		}
	}
	for _, item := range manual {
		desired = append(desired, item)
	}
	previous := c.store.SetManual(desired)
	c.revision++
	revision := c.revision
	if c.publish != nil {
		if err := c.publish(); err != nil {
			c.store.SetManual(previous)
			c.lastError = err
			return nil, err
		}
	}
	c.appliedRevision = revision
	c.lastPublished = time.Now()
	c.lastError = nil
	return manual, nil
}

func (c *Controller) refreshRouteLocked(id RouteID, baseDomain string, now time.Time) (Route, error) {
	var selected *Route
	for _, claim := range c.claims {
		if claim.Project != id.Project || claim.Branch != id.Branch || !now.Before(claim.Expires) {
			continue
		}
		if selected == nil {
			domain, err := BuildDomainChecked(baseDomain, claim.Project, claim.Branch)
			if err != nil {
				return Route{}, err
			}
			selected = &Route{
				Project: claim.Project, Branch: claim.Branch, Port: claim.Port,
				Domain: domain, ExpiresAt: claim.Expires, Source: SourceManual,
			}
			continue
		}
		if selected.Port != claim.Port {
			return Route{}, fmt.Errorf("%w: route %s is claimed on different ports", ErrConflict, id.String())
		}
		if claim.Expires.After(selected.ExpiresAt) {
			selected.ExpiresAt = claim.Expires
		}
	}
	if selected == nil {
		return Route{}, ErrNotFound
	}
	if existing, ok := c.store.Get(id); ok {
		selected.CreatedAt = existing.CreatedAt
	}
	if err := c.store.Add(*selected); err != nil {
		return Route{}, err
	}
	return *selected, nil
}

func newLeaseID() (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate lease ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}
