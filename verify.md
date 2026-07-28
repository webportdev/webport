# Verification Checklist

## macOS Native Installation

- [ ] `./scripts/install_macos_test.sh` passes
- [ ] Both LaunchDaemon plists pass `plutil -lint`
- [ ] Darwin amd64 and arm64 binaries build successfully
- [ ] `launchctl print system/com.webport.caddy` reports a running Caddy service
- [ ] `launchctl print system/com.webport.webport` reports a running webport service
- [ ] A registered route proxies to a development server bound to macOS localhost
- [ ] `curl http://127.0.0.1:8080/health` succeeds and the API is not reachable through the Mac's LAN address

This document contains a comprehensive testing checklist for verifying the webport service.

## Unit Tests

Run with: `go test ./...` or `mise run test`

### Route Storage (`internal/route/store_test.go`)

- [ ] Store.AddAndGet - Adding and retrieving routes
- [ ] Store.AddPreservesCreatedAt - Updates preserve original creation time
- [ ] Store.AddSetsCreatedAtForNew - New routes get creation timestamp
- [ ] Store.Delete - Deleting existing routes
- [ ] Store.List - Listing all routes
- [ ] Store.DeleteExpired - Expired routes are removed
- [ ] Store.Clear - All routes cleared
- [ ] Store.ConcurrentAccess - Thread-safe concurrent operations
- [ ] RouteID.String - Route ID formatting
- [ ] RouteIDFromString - Parsing route IDs

### Caddy Generator (`internal/caddy/generator_test.go`)

- [ ] GenerateCaddyfile with empty routes
- [ ] GenerateCaddyfile with single route
- [ ] GenerateCaddyfile with multiple routes
- [ ] BuildDomain with simple branch
- [ ] BuildDomain with branch containing slashes
- [ ] BuildDomain with branch containing multiple slashes
- [ ] BuildDomain with branch containing hyphens
- [ ] GenerateCaddyfileFormat - Output structure validation
- [ ] GenerateCaddyfileWithDNSChallenge - Wildcard cert and tls directive
- [ ] GenerateCaddyfileWithoutDNSChallenge - HTTP challenge (default)

### Caddy Reload (`internal/caddy/reload_test.go`)

- [ ] NewWriter - Writer initialization
- [ ] WriteCreatesDirectory - Directory creation for Caddyfile
- [ ] WriteOverwritesExisting - Overwriting existing Caddyfile
- [ ] WriteIsAtomic - Atomic file operations
- [ ] WriteExecutesReloadCommand - Reload command execution
- [ ] WriteWithBadReloadCommand - Error handling for failed reload

### API Handlers (`internal/api/handlers_test.go`)

- [ ] TestRegisterRoute - Route registration
- [ ] TestRegisterRouteWithBranchSlash - Branch name sanitization
- [ ] TestRegisterRouteValidation - Input validation
- [ ] TestRegisterRouteIdempotent - Idempotent updates
- [ ] TestListRoutes - Listing routes
- [ ] TestDeleteRoute - Route deletion
- [ ] TestDeleteRouteNotFound - 404 for missing routes
- [ ] TestHeartbeat - TTL refresh
- [ ] TestHeartbeatNotFound - 404 for missing routes
- [ ] TestHealth - Health check endpoint
- [ ] TestParseRouteID - Route ID parsing
- [ ] TestIsHeartbeat - Heartbeat path detection

### TTL Checker (`internal/route/ttl_test.go`)

- [ ] TestTTLCheckerExpiration - Expired routes are cleaned up
- [ ] TestTTLCheckerNoExpiration - Active routes preserved
- [ ] TestTTLCheckerStop - Graceful shutdown
- [ ] TestTTLCheckerConcurrentAccess - Thread-safe operations
- [ ] TestTTLCheckerWithNilCallback - Handles nil callback

### Graceful Shutdown (`internal/shutdown/graceful_test.go`)

- [ ] TestShutdownClearsRoutes - Routes cleared on shutdown
- [ ] TestShutdownCallsOnExit - Exit callback invoked
- [ ] TestShutdownStopsTTLChecker - TTL checker stopped
- [ ] TestShutdownWithServer - HTTP server shutdown
- [ ] TestNewManager - Manager initialization
- [ ] TestSetupSignals - Signal handler setup

### Config (`internal/config/config_test.go`)

- [ ] TestLoadDefaults - Default configuration values
- [ ] TestLoadWithEnvVars - Environment variable parsing
- [ ] TestLoadWithTLSConfig - TLS/DNS provider configuration
- [ ] TestLoadRequiredBaseDomain - Panic on missing base domain
- [ ] TestGetListenAddr - Listen address formatting

## Integration Tests

### API Endpoint Integration

- [ ] POST /routes creates new route
- [ ] POST /routes is idempotent (same project+branch updates)
- [ ] GET /routes lists all routes
- [ ] DELETE /routes/{id} removes route
- [ ] POST /routes/{id}/heartbeat refreshes TTL
- [ ] Invalid requests return proper error codes
- [ ] Missing required fields return 400
- [ ] Malformed JSON returns 400

### Caddy Integration

- [ ] Caddyfile is generated in correct location
- [ ] Caddyfile format is valid
- [ ] Caddy reload command is executed after changes
- [ ] Atomic write prevents partial reads
- [ ] Empty Caddyfile is generated when no routes

## Manual Verification

### Build and Installation

```bash
# 1. Build succeeds
mise run build

# 2. Binary is created
test -f build/webport

# 3. Binary runs with help (if implemented) or shows version
./build/webport --help || echo "No help yet"

# 4. Install succeeds
mise run install

# 5. Binary is installed
test -f /usr/local/bin/webport
```

### Development Mode

```bash
# 1. Run in development mode
WEBPORT_BASE_DOMAIN=example.com \
WEBPORT_PORT=8080 \
WEBPORT_CADDYFILE_PATH=/tmp/webport-Caddyfile \
WEBPORT_CADDY_RELOAD_CMD="echo 'Would reload caddy'" \
mise run dev &

# 2. API responds on configured port
curl http://localhost:8080/health
# Expected: OK

# 3. Register a route
curl -X POST http://localhost:8080/routes \
  -H "Content-Type: application/json" \
  -d '{"project": "test", "branch": "main", "port": 3000}'

# 4. List routes
curl http://localhost:8080/routes
# Expected: JSON with one route

# 5. Caddyfile is generated
cat /tmp/webport-Caddyfile
# Expected: test-main.example.com block

# 6. Delete route
curl -X DELETE http://localhost:8080/routes/test:main

# 7. Verify deletion
curl http://localhost:8080/routes
# Expected: Empty routes array
```

### DNS Challenge Configuration

```bash
# 1. Run with DNS challenge enabled
WEBPORT_BASE_DOMAIN=example.com \
WEBPORT_TLS_DNS_PROVIDER=cloudflare \
WEBPORT_TLS_DNS_PROVIDER_MODULE=cloudflare \
WEBPORT_PORT=8080 \
WEBPORT_CADDYFILE_PATH=/tmp/webport-Caddyfile \
WEBPORT_CADDY_RELOAD_CMD="echo 'Would reload caddy'" \
mise run dev &

# 2. Register a route
curl -X POST http://localhost:8080/routes \
  -H "Content-Type: application/json" \
  -d '{"project": "test", "branch": "main", "port": 3000}'

# 3. Check generated Caddyfile contains wildcard and tls directive
cat /tmp/webport-Caddyfile
# Expected: *.example.com block with respond
# Expected: test-main.example.com block with "tls { dns cloudflare }"

# 4. Without DNS challenge (default HTTP challenge)
pkill -f webport
unset WEBPORT_TLS_DNS_PROVIDER WEBPORT_TLS_DNS_PROVIDER_MODULE
WEBPORT_BASE_DOMAIN=example.com \
WEBPORT_PORT=8081 \
WEBPORT_CADDYFILE_PATH=/tmp/webport-Caddyfile-http \
WEBPORT_CADDY_RELOAD_CMD="echo 'Would reload caddy'" \
mise run dev &

curl -X POST http://localhost:8081/routes \
  -H "Content-Type: application/json" \
  -d '{"project": "test", "branch": "main", "port": 3000}'

# 5. Verify no wildcard or tls directive
cat /tmp/webport-Caddyfile-http
# Expected: NO *.example.com block
# Expected: NO tls directive
# Expected: Just reverse_proxy
```

### Systemd Service

```bash
# 1. Install service
mise run install-service

# 2. Service starts successfully
sudo systemctl start webport

# 3. Service status is active
sudo systemctl status webport
# Expected: Active: active (running)

# 4. Service is enabled on boot
sudo systemctl is-enabled webport
# Expected: enabled

# 5. API responds
curl http://localhost:8080/health

# 6. Routes work with real Caddy
curl -X POST http://localhost:8080/routes \
  -H "Content-Type: application/json" \
  -d '{"project": "test", "branch": "main", "port": 3000}'

# 7. Check generated Caddyfile
sudo cat /etc/caddy/conf.d/Caddyfile
# Expected: valid Caddyfile with route

# 8. Caddy reload was successful
sudo journalctl -u caddy | tail -20
# Expected: no errors

# 9. Stop service
sudo systemctl stop webport
# Expected: graceful shutdown, routes cleared

# 10. Check Caddyfile is empty or doesn't exist
sudo cat /etc/caddy/conf.d/Caddyfile
```

### TTL and Cleanup

```bash
# 1. Register a route with short TTL
curl -X POST http://localhost:8080/routes \
  -H "Content-Type: application/json" \
  -d '{"project": "ttl-test", "branch": "main", "port": 3000, "ttl": 10}'

# 2. Verify route exists
curl http://localhost:8080/routes

# 3. Wait for expiration (10 + check interval seconds)
sleep 40

# 4. Verify route is removed
curl http://localhost:8080/routes
# Expected: empty routes array

# 5. Check logs for expiration message
sudo journalctl -u webport | grep TTL
# Expected: "TTL: expired 1 route(s)"
```

### Heartbeat

```bash
# 1. Register a route with short TTL
curl -X POST http://localhost:8080/routes \
  -H "Content-Type: application/json" \
  -d '{"project": "hb-test", "branch": "main", "port": 3000, "ttl": 10}'

# 2. Send heartbeat before expiration
sleep 5
curl -X POST http://localhost:8080/routes/hb-test:main/heartbeat \
  -H "Content-Type: application/json" \
  -d '{"ttl": 600}'

# 3. Wait original TTL (should not expire)
sleep 10

# 4. Verify route still exists
curl http://localhost:8080/routes
# Expected: route still present
```

### Systemd Watchdog

```bash
# 1. Check watchdog is enabled in service
sudo systemctl show webport | grep Watchdog
# Expected: WatchdogTimestamp=...

# 2. Check logs for watchdog pings
sudo journalctl -u webport | grep -i watchdog
# Expected: periodic ping activity

# 3. Verify service doesn't restart unexpectedly
sudo systemctl status webport
# Expected: normal running state
```

### Logs and Monitoring

```bash
# 1. Check logs are visible in journalctl
sudo journalctl -u webport -n 50

# 2. Follow logs
sudo journalctl -u webport -f

# 3. Verify log format contains timestamps
# Expected: proper timestamp formatting

# 4. Check for error messages
sudo journalctl -u webport -p err
# Expected: no errors during normal operation
```

### Graceful Shutdown

```bash
# 1. Register multiple routes
for i in 1 2 3; do
  curl -X POST http://localhost:8080/routes \
    -H "Content-Type: application/json" \
    -d "{\"project\": \"app$i\", \"branch\": \"main\", \"port\": $((3000+i))}"
done

# 2. Verify routes exist
curl http://localhost:8080/routes
# Expected: 3 routes

# 3. Stop service
sudo systemctl stop webport

# 4. Check shutdown logs
sudo journalctl -u webport -n 20 | grep Shutdown
# Expected: "Starting graceful shutdown..." and "Shutdown complete"

# 5. Verify routes were cleared
sudo cat /etc/caddy/conf.d/Caddyfile
# Expected: empty or no routes

# 6. Restart service
sudo systemctl start webport

# 7. Verify fresh start (no old routes)
curl http://localhost:8080/routes
# Expected: empty routes array
```

### Restart on Failure

```bash
# 1. Simulate a crash (kill -9)
sudo systemctl start webport
PID=$(systemctl show webport --property MainPID --value)
sudo kill -9 $PID

# 2. Check service restarted
sleep 2
sudo systemctl status webport
# Expected: service is running again

# 3. Check restart count
systemctl show webport --property NRestarts
# Expected: NRestarts=1 or more
```

## Security Tests

### Input Validation

- [ ] Port numbers outside 1-65535 are rejected
- [ ] Empty project name is rejected
- [ ] Empty branch name is rejected
- [ ] Malformed JSON returns 400 error
- [ ] Invalid route IDs return 400 error

### File Permissions

```bash
# 1. Check Caddyfile permissions
ls -l /etc/caddy/conf.d/Caddyfile
# Expected: -rw-r--r-- (0644)

# 2. Check directory permissions
ls -ld /etc/caddy/conf.d/
# Expected: drwxr-xr-x (0755) or similar
```

### Service Security

```bash
# 1. Check service runs as root (required for Caddy reload)
systemctl show webport --property User
# Expected: root

# 2. Check security options
systemctl show webport --property NoNewPrivileges
# Expected: 1 (true)

# 3. Check protect system
systemctl show webport --property ProtectSystem
# Expected: strict
```

## Performance Tests

### Concurrent Requests

```bash
# 1. Install ab (Apache Bench) or similar
# 2. Test concurrent route creation
ab -n 100 -c 10 -p route.json -T application/json \
  http://localhost:8080/routes

# Expected: All requests succeed, no panics
```

### Many Routes

```bash
# 1. Register 100 routes
for i in {1..100}; do
  curl -X POST http://localhost:8080/routes \
    -H "Content-Type: application/json" \
    -d "{\"project\": \"app$i\", \"branch\": \"main\", \"port\": $((3000+i))}"
done

# 2. List routes (response time)
time curl http://localhost:8080/routes

# 3. Verify all routes are present
curl -s http://localhost:8080/routes | jq '.total'
# Expected: 100
```

## Edge Cases

### Branch Name Handling

- [ ] Branch with single slash: `feature/auth` → `app-feature-auth.domain`
- [ ] Branch with multiple slashes: `feature/auth/oauth` → `app-feature-auth-oauth.domain`
- [ ] Branch with hyphens: `feature-auth` → `app-feature-auth.domain`
- [ ] Branch with numbers: `feature123` → `app-feature123.domain`

### Special Characters

- [ ] Project with numbers: `app123` → `app123-main.domain`
- [ ] Project with hyphens: `my-app` → `my-app-main.domain`

### Boundary Conditions

- [ ] TTL = 1 second (minimum)
- [ ] TTL = very large value
- [ ] Port = 1 (minimum)
- [ ] Port = 65535 (maximum)
- [ ] Empty route list
- [ ] Very long project/branch names

## Production Readiness

- [ ] All unit tests pass
- [ ] All integration tests pass
- [ ] Service runs successfully under systemd
- [ ] Logs are properly formatted
- [ ] Graceful shutdown works
- [ ] Watchdog is functioning
- [ ] Restart on failure works
- [ ] Documentation is complete
- [ ] Example client code works
- [ ] No memory leaks (run with race detector)
- [ ] No data races (go test -race)
