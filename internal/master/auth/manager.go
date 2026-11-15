package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Rashid2G/compute/internal/dns"
	"github.com/Rashid2G/compute/internal/master/auth/quota"
	"github.com/Rashid2G/compute/internal/master/pki"
	"github.com/Rashid2G/compute/internal/store"
	"github.com/Rashid2G/compute/internal/store/db"
)

type contextKey string

const (
	AuthContextKey    contextKey = "compute_auth_context"
	SessionCookieName            = "compute_session"
)

// AuthContext contains the caller's verified identity, active organization, role, and scopes.
type AuthContext struct {
	User     *db.User    `json:"user"`
	APIKey   *db.APIKey  `json:"api_key,omitempty"`
	Session  *db.Session `json:"session,omitempty"`
	OrgID    string      `json:"org_id"`
	Role     string      `json:"role"`
	Scopes   []string    `json:"scopes"`
	AuthType string      `json:"auth_type"` // "api_key", "session", "admin_secret"
	ClientIP string      `json:"client_ip"`
}

// ExternalAuthenticator verifies tokens issued by an external/scoped key store
// (for example access_api_keys managed by the compute control plane).
type ExternalAuthenticator func(ctx context.Context, token, clientIP string) (*AuthContext, error)

// UsageProvider computes live resource usage for a user. It lets the control
// plane (which owns VM and volume state) supply real numbers to GetLimits.
type UsageProvider func(ctx context.Context, userID string) (*db.QuotaUsage, error)

// Manager orchestrates user registration, logins, API keys, sessions, and request authentication.
type Manager struct {
	queries        *db.Queries
	masterKey      *pki.MasterKey
	keyMgr         *KeyManager
	sessionMgr     *SessionManager
	adminBootstrap string // optional static admin bearer secret
	adminUsername  string
	adminEmail     string
	externalAuth   ExternalAuthenticator
	usageProvider  UsageProvider
}

// NewManager creates an authentication and identity manager.
func NewManager(queries *db.Queries, masterKey *pki.MasterKey, sessionTTL time.Duration, adminSecret string) *Manager {
	return &Manager{
		queries:        queries,
		masterKey:      masterKey,
		keyMgr:         NewKeyManager(queries, masterKey),
		sessionMgr:     NewSessionManager(queries, sessionTTL),
		adminBootstrap: adminSecret,
		adminUsername:  "admin",
		adminEmail:     "admin@compute.local",
	}
}

// SetAdminIdentity sets the configured bootstrap administrator username and email.
func (m *Manager) SetAdminIdentity(username, email string) {
	u := strings.TrimSpace(strings.ToLower(username))
	if u != "" {
		m.adminUsername = u
	}
	e := strings.TrimSpace(email)
	if e != "" {
		m.adminEmail = e
	}
}

// AdminIdentity returns the configured administrator username and email.
func (m *Manager) AdminIdentity() (string, string) {
	u := m.adminUsername
	if u == "" {
		u = "admin"
	}
	e := m.adminEmail
	if e == "" {
		e = "admin@compute.local"
	}
	return u, e
}

func (m *Manager) resolveSeededAdminUser(ctx context.Context) *db.User {
	if m.queries == nil {
		return nil
	}
	uname, uemail := m.AdminIdentity()
	for _, candidate := range []string{uname, uemail, "admin"} {
		if candidate == "" {
			continue
		}
		if u, err := m.queries.GetUserByEmailOrUsername(ctx, candidate); err == nil && u != nil {
			if u.Role == "admin" && (u.Status == "" || u.Status == "active") {
				return u
			}
		}
	}
	if u, err := m.queries.GetUserByID(ctx, "usr_system_admin"); err == nil && u != nil {
		if u.Role == "admin" && (u.Status == "" || u.Status == "active") {
			return u
		}
	}
	return nil
}

// KeyManager returns the underlying API key manager.
func (m *Manager) KeyManager() *KeyManager {
	return m.keyMgr
}

// SessionManager returns the underlying session manager.
func (m *Manager) SessionManager() *SessionManager {
	return m.sessionMgr
}

// SetExternalAuthenticator installs a fallback verifier for external API keys.
func (m *Manager) SetExternalAuthenticator(fn ExternalAuthenticator) {
	m.externalAuth = fn
}

// SetUsageProvider installs a live-usage provider used by GetLimits.
func (m *Manager) SetUsageProvider(fn UsageProvider) {
	m.usageProvider = fn
}

// Signup registers a new user, hashes password with Argon2id, and provisions their default organization.
func (m *Manager) Signup(ctx context.Context, email, username, displayName, password, role string) (*db.User, error) {
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters")
	}
	return m.createUserWithOrg(ctx, email, username, displayName, password, role)
}

func (m *Manager) createUserWithOrg(ctx context.Context, email, username, displayName, password, role string) (*db.User, error) {
	email = strings.TrimSpace(email)
	username = strings.TrimSpace(strings.ToLower(username))
	if email == "" || username == "" || password == "" {
		return nil, errors.New("email, username, and password are required")
	}
	// Usernames become the tenant label in sandbox DNS names
	// (<sandbox>.<username>.<base-domain>), so they must be DNS-safe.
	if !dns.ValidLabel(username) {
		return nil, fmt.Errorf("invalid username: must be lowercase letters, digits, and dashes (a DNS label, max %d chars) so sandboxes are reachable at <sandbox>.<username>.<base-domain>", dns.MaxLabelLength)
	}
	if role == "" {
		role = "user"
	}
	if role != "admin" && role != "user" {
		return nil, fmt.Errorf("invalid role: %s", role)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	userID := store.NewUserID()
	now := db.NowMillis()

	user := &db.User{
		ID:           userID,
		Email:        email,
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: hash,
		Role:         role,
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
		Revision:     1,
	}

	if err := m.queries.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	m.ensurePersonalOrg(ctx, userID, username, now)

	return user, nil
}

func (m *Manager) ensurePersonalOrg(ctx context.Context, userID, username string, now int64) {
	orgID := personalOrgIDFor(userID)
	expectedName := fmt.Sprintf("%s's Workspace", username)
	if existingOrg, err := m.queries.GetOrganizationByID(ctx, orgID); errors.Is(err, db.ErrNotFound) {
		org := &db.Organization{
			ID:        orgID,
			Name:      expectedName,
			Slug:      username,
			Status:    "active",
			CreatedAt: now,
			UpdatedAt: now,
		}
		_ = m.queries.CreateOrganization(ctx, org)
	} else if err == nil && existingOrg != nil && (existingOrg.Slug != username || existingOrg.Name != expectedName) {
		_ = m.queries.UpdateOrganizationIdentity(ctx, orgID, expectedName, username)
	}
	_ = m.queries.AddOrganizationMember(ctx, &db.OrganizationMember{
		OrganizationID: orgID,
		UserID:         userID,
		Role:           "owner",
		CreatedAt:      now,
	})
}

// EnsureAdmin ensures the configured administrator account exists, is active with
// role "admin", has any lockout cleared, and (when password is non-empty) has
// its password synced to the configured value.
func (m *Manager) EnsureAdmin(ctx context.Context, email, username, displayName, password string) (*db.User, error) {
	if m.queries == nil {
		return nil, errors.New("auth store unavailable")
	}
	username = strings.TrimSpace(strings.ToLower(username))
	if username == "" {
		username = "admin"
	}
	email = strings.TrimSpace(email)
	if email == "" {
		email = "admin@compute.local"
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "Administrator"
	}
	if !dns.ValidLabel(username) {
		return nil, fmt.Errorf("invalid admin username: must be lowercase letters, digits, and dashes (a DNS label, max %d chars)", dns.MaxLabelLength)
	}

	m.SetAdminIdentity(username, email)

	var existing *db.User
	for _, candidate := range []string{username, email} {
		if u, err := m.queries.GetUserByEmailOrUsername(ctx, candidate); err == nil && u != nil {
			existing = u
			break
		}
	}
	if existing == nil {
		if u, err := m.queries.GetUserByID(ctx, "usr_system_admin"); err == nil && u != nil {
			existing = u
		}
	}
	if existing == nil {
		if users, err := m.queries.ListUsers(ctx); err == nil {
			var admins []*db.User
			for _, u := range users {
				if u != nil && u.Role == "admin" {
					admins = append(admins, u)
					if u.Username == "admin" || strings.EqualFold(u.Email, "admin@compute.local") {
						existing = u
						break
					}
				}
			}
			if existing == nil && len(admins) == 1 {
				existing = admins[0]
			}
		}
	}

	if existing == nil {
		if password == "" {
			return nil, errors.New("admin password is required to bootstrap admin account")
		}
		return m.createUserWithOrg(ctx, email, username, displayName, password, "admin")
	}

	passwordHash := ""
	if password != "" {
		if VerifyPassword(password, existing.PasswordHash) != nil {
			h, err := HashPassword(password)
			if err != nil {
				return nil, fmt.Errorf("failed to hash admin password: %w", err)
			}
			passwordHash = h
		}
	}

	if err := m.queries.UpdateUserAdminBootstrap(ctx, existing.ID, email, username, displayName, passwordHash, "admin", "active"); err != nil {
		return nil, fmt.Errorf("failed to update admin user: %w", err)
	}
	if passwordHash != "" || !strings.EqualFold(existing.Username, username) || !strings.EqualFold(existing.Email, email) {
		_ = m.queries.DeleteUserSessions(ctx, existing.ID)
	}

	m.ensurePersonalOrg(ctx, existing.ID, username, db.NowMillis())

	updated, err := m.queries.GetUserByID(ctx, existing.ID)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Login verifies credentials with Argon2id and account lockout protection, then issues a session token.
func (m *Manager) Login(ctx context.Context, identifier, password, ip, userAgent string) (*db.User, string, string, error) {
	user, err := m.queries.GetUserByEmailOrUsername(ctx, identifier)
	if err != nil {
		return nil, "", "", ErrPasswordMismatch
	}

	if user.Status != "active" {
		return nil, "", "", errors.New("account is disabled")
	}

	// Verify Argon2id
	verifyErr := VerifyPassword(password, user.PasswordHash)
	passwordMatch := verifyErr == nil

	if err := CheckAndRecordLoginAttempt(ctx, m.queries, user, passwordMatch); err != nil {
		return nil, "", "", err
	}

	// Issue browser session
	token, csrf, _, err := m.sessionMgr.CreateSession(ctx, user.ID, ip, userAgent)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to create session: %w", err)
	}

	return user, token, csrf, nil
}

// isQueryAuthPermitted returns true only for WebSocket, SSE, and preview proxy endpoints
// where browsers cannot set custom Authorization headers. Caller-controlled headers like
// Upgrade or Accept MUST NEVER grant query token auth on arbitrary REST endpoints.
func isQueryAuthPermitted(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	path := r.URL.Path

	// Explicitly reject query parameter authentication on all standard REST CRUD endpoints
	// (e.g. /api/v1/vms, /api/v1/sandboxes, /api/v1/volumes, /api/v1/images, /api/v1/templates,
	// /api/v1/jobs, /api/v1/config, etc.) whenever they are not one of the allowed endpoints below,
	// even if the resource name starts with or contains "ws".
	crudPrefixes := []string{
		"/api/v1/vms",
		"/api/v1/sandboxes",
		"/api/v1/volumes",
		"/api/v1/images",
		"/api/v1/templates",
		"/api/v1/jobs",
		"/api/v1/config",
		"/api/v1/users",
		"/api/v1/pools",
		"/api/v1/workers",
	}

	// Strictly allow only known WebSocket, SSE, exec terminal, preview proxy, and logs stream routes:
	// - WebSocket endpoint: path == "/api/v1/ws" || strings.HasPrefix(path, "/api/v1/ws/")
	// - Events endpoint: path == "/api/v1/events" || path == "/api/v1/events/stream"
	// - Sandboxes/VMs exec terminal: strings.HasSuffix(path, "/exec")
	// - Sandboxes/VMs preview proxy: strings.Contains(path, "/preview/") || strings.HasPrefix(path, "/preview/")
	// - Logs stream: strings.HasSuffix(path, "/logs")
	isAllowedStreamingRoute := (path == "/api/v1/ws" || strings.HasPrefix(path, "/api/v1/ws/")) ||
		(path == "/api/v1/events" || path == "/api/v1/events/stream") ||
		strings.HasSuffix(path, "/exec") ||
		(strings.Contains(path, "/preview/") || strings.HasPrefix(path, "/preview/")) ||
		strings.HasSuffix(path, "/logs")

	if !isAllowedStreamingRoute {
		return false
	}

	// For any standard REST CRUD endpoints, query auth is strictly forbidden unless
	// accessing an allowed sub-route (/exec, /preview/..., /logs).
	for _, prefix := range crudPrefixes {
		if path == prefix || path == prefix+"/" {
			return false
		}
		if strings.HasPrefix(path, prefix+"/") {
			if !strings.HasSuffix(path, "/exec") &&
				!strings.HasSuffix(path, "/logs") &&
				!strings.Contains(path, "/preview/") {
				return false
			}
		}
	}

	return true
}

// AuthenticateRequest inspects incoming HTTP headers, query parameters, or
// cookies to establish caller identity. Query parameters ("token" / "api_key")
// are strictly restricted to browser WebSocket, SSE, and preview proxy endpoints
// and are rejected on standard REST CRUD endpoints.
func (m *Manager) AuthenticateRequest(r *http.Request) (*AuthContext, error) {
	clientIP := extractClientIP(r)

	// 1. Resolve a token from Authorization, X-API-Key, or query parameters (for allowed streaming routes only).
	authHeader := r.Header.Get("Authorization")
	var headerToken string
	switch {
	case strings.HasPrefix(authHeader, "Bearer "):
		headerToken = strings.TrimPrefix(authHeader, "Bearer ")
	case r.Header.Get("X-API-Key") != "":
		headerToken = r.Header.Get("X-API-Key")
	}
	headerToken = strings.TrimSpace(headerToken)

	token := headerToken
	if token == "" && isQueryAuthPermitted(r) {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
		if token == "" {
			token = strings.TrimSpace(r.URL.Query().Get("api_key"))
		}
	}

	// Admin Bootstrap Secret Check (header credentials only)
	if m.adminBootstrap != "" && headerToken != "" {
		if subtle.ConstantTimeCompare([]byte(headerToken), []byte(m.adminBootstrap)) == 1 {
			if adminUser := m.resolveSeededAdminUser(r.Context()); adminUser != nil {
				return m.authContextForUser(r, adminUser, nil, nil, "admin_secret", clientIP), nil
			}
			uname, uemail := m.AdminIdentity()
			adminUser := &db.User{
				ID:          "usr_system_admin",
				Email:       uemail,
				Username:    uname,
				DisplayName: "System Administrator",
				Role:        "admin",
				Status:      "active",
			}
			return &AuthContext{
				User:     adminUser,
				OrgID:    "org_default",
				Role:     "admin",
				Scopes:   []string{"*"},
				AuthType: "admin_secret",
				ClientIP: clientIP,
			}, nil
		}
	}

	// API Key Authentication (format: cmp_..., cmp_live_..., or legacy mfk_...)
	if strings.HasPrefix(token, "mfk_") || strings.HasPrefix(token, "cmp_") {
		if user, keyRecord, err := m.keyMgr.AuthenticateToken(r.Context(), token, clientIP); err == nil {
			return m.authContextForUser(r, user, keyRecord, nil, "api_key", clientIP), nil
		}
		// Not a platform key: could be a scoped access key such as cmp_live_...
	}

	// External / scoped API keys (access_api_keys in the control-plane store).
	if token != "" && m.externalAuth != nil {
		if authCtx, err := m.externalAuth(r.Context(), token, clientIP); err == nil && authCtx != nil {
			authCtx.ClientIP = clientIP
			return authCtx, nil
		}
	}

	// Session token passed directly through a header or query parameter.
	if token != "" {
		if user, session, err := m.sessionMgr.AuthenticateSession(r.Context(), token); err == nil {
			return m.authContextForUser(r, user, nil, session, "session", clientIP), nil
		}
	}

	// 2. Cookie Authentication (Session)
	cookie, err := r.Cookie(SessionCookieName)
	if err == nil && cookie.Value != "" {
		user, session, err := m.sessionMgr.AuthenticateSession(r.Context(), cookie.Value)
		if err != nil {
			return nil, err
		}
		return m.authContextForUser(r, user, nil, session, "session", clientIP), nil
	}

	return nil, errors.New("no valid authentication token or session provided")
}

// personalOrgIDFor returns the deterministic personal organization ID for a user.
func personalOrgIDFor(userID string) string {
	return "org_" + strings.TrimPrefix(userID, "usr_")
}

// PersonalOrgID returns the deterministic personal organization ID for a user.
// Resources may be owned directly by the user or by their personal
// organization, so tenant lookups must consider both.
func PersonalOrgID(userID string) string {
	return personalOrgIDFor(userID)
}

// UserByUsername resolves a user by username or email (case-insensitive for
// email, exact for username). It returns (nil, nil) when no user matches.
func (m *Manager) UserByUsername(ctx context.Context, identifier string) (*db.User, error) {
	if m.queries == nil || strings.TrimSpace(identifier) == "" {
		return nil, nil
	}
	user, err := m.queries.GetUserByEmailOrUsername(ctx, identifier)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup user %q: %w", identifier, err)
	}
	return user, nil
}

// UserByID resolves a user by ID. It returns (nil, nil) when no user matches.
func (m *Manager) UserByID(ctx context.Context, id string) (*db.User, error) {
	if m.queries == nil || strings.TrimSpace(id) == "" {
		return nil, nil
	}
	user, err := m.queries.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup user %q: %w", id, err)
	}
	return user, nil
}

// Users lists every user account.
func (m *Manager) Users(ctx context.Context) ([]*db.User, error) {
	if m.queries == nil {
		return nil, fmt.Errorf("auth store unavailable")
	}
	return m.queries.ListUsers(ctx)
}

// ErrProtectedAdmin is returned when attempting to delete the primary bootstrap administrator.
var ErrProtectedAdmin = errors.New("cannot delete primary bootstrap administrator")

// DeleteUser removes a user account along with its sessions, API keys, and org memberships.
func (m *Manager) DeleteUser(ctx context.Context, id string) error {
	if m.queries == nil {
		return errors.New("auth store unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("user id is required")
	}
	if id == "usr_system_admin" {
		return ErrProtectedAdmin
	}
	user, err := m.UserByID(ctx, id)
	if err != nil {
		return err
	}
	if user == nil {
		return db.ErrNotFound
	}
	configuredAdminUsername, _ := m.AdminIdentity()
	if user.Username == configuredAdminUsername {
		return ErrProtectedAdmin
	}
	return m.queries.DeleteUser(ctx, user.ID)
}

// resolveOrgID returns the organization a request may act within. A caller may
// only select an organization they are a confirmed member of; any other value
// (including an empty header) falls back to their personal organization. This
// prevents X-Compute-Org from being used as a cross-tenant authorization bypass.
func (m *Manager) resolveOrgID(ctx context.Context, user *db.User, requested string) string {
	personal := personalOrgIDFor(user.ID)
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == personal {
		return personal
	}
	if m.queries != nil {
		if orgs, err := m.queries.ListOrganizationsForUser(ctx, user.ID); err == nil {
			for _, org := range orgs {
				if org != nil && org.ID == requested {
					return requested
				}
			}
		}
	}
	return personal
}

// authContextForUser builds a verified AuthContext for a user and optional key/session.
func (m *Manager) authContextForUser(r *http.Request, user *db.User, keyRecord *db.APIKey, session *db.Session, authType, clientIP string) *AuthContext {
	orgID := m.resolveOrgID(r.Context(), user, r.Header.Get("X-Compute-Org"))
	scopes := []string{"*"}
	if keyRecord != nil && keyRecord.Scopes != "" {
		var parsed []string
		if json.Unmarshal([]byte(keyRecord.Scopes), &parsed) == nil && len(parsed) > 0 {
			scopes = parsed
		}
	}
	return &AuthContext{
		User:     user,
		APIKey:   keyRecord,
		Session:  session,
		OrgID:    orgID,
		Role:     user.Role,
		Scopes:   scopes,
		AuthType: authType,
		ClientIP: clientIP,
	}
}

// Middleware returns an HTTP middleware enforcing authentication on routes.
func (m *Manager) Middleware(requireAuth bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authCtx, err := m.AuthenticateRequest(r)
		if err != nil {
			if requireAuth {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"error":"unauthorized","message":%q}`+"\n", err.Error())
				return
			}
		} else {
			r = r.WithContext(context.WithValue(r.Context(), AuthContextKey, authCtx))
		}
		next(w, r)
	}
}

// RequireAuth wraps a handler ensuring the caller is authenticated.
func (m *Manager) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return m.Middleware(true, next)
}

// RequireAdmin wraps a handler ensuring the caller has admin privileges.
func (m *Manager) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authCtx, err := m.AuthenticateRequest(r)
		if err != nil || authCtx.Role != "admin" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":"forbidden","message":"administrative privileges required"}`+"\n")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), AuthContextKey, authCtx))
		next(w, r)
	}
}

// GetAuth retrieves the AuthContext from a request context, if present.
func GetAuth(ctx context.Context) *AuthContext {
	val := ctx.Value(AuthContextKey)
	if val == nil {
		return nil
	}
	if authCtx, ok := val.(*AuthContext); ok {
		return authCtx
	}
	return nil
}

func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
		return strings.TrimSpace(xrip)
	}
	ip, _, err := netSplitHostPort(r.RemoteAddr)
	if err == nil {
		return ip
	}
	return r.RemoteAddr
}

func netSplitHostPort(addr string) (string, string, error) {
	idx := strings.LastIndex(addr, ":")
	if idx == -1 {
		return addr, "", nil
	}
	return addr[:idx], addr[idx+1:], nil
}

// GetLimits returns quota and limit metrics for a user or organization owner.
// Organizations share the same quota surface as users; an unknown organization
// falls back to default limits instead of failing on the user-only lookup.
func (m *Manager) GetLimits(ctx context.Context, userID string) (*quota.Quota, *db.QuotaUsage, error) {
	limits := quota.DefaultQuota()
	if strings.HasPrefix(userID, "org_") {
		org, err := m.queries.GetOrganizationByID(ctx, userID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			return nil, nil, err
		}
		if org != nil && org.QuotaJSON != nil && strings.TrimSpace(*org.QuotaJSON) != "" {
			_ = json.Unmarshal([]byte(*org.QuotaJSON), &limits)
		}
	} else {
		user, err := m.queries.GetUserByID(ctx, userID)
		if err != nil {
			return nil, nil, err
		}
		if user.QuotaJSON.Valid && user.QuotaJSON.String != "" {
			_ = json.Unmarshal([]byte(user.QuotaJSON.String), &limits)
		}
	}

	usage, err := m.queries.GetUserQuotaUsage(ctx, userID)
	if m.usageProvider != nil {
		usage, err = m.usageProvider(ctx, userID)
	}
	if err != nil {
		return nil, nil, err
	}
	if usage == nil {
		usage = &db.QuotaUsage{}
	}

	return &limits, usage, nil
}
