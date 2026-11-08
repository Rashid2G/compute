package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/master/auth/quota"
	"github.com/Rashid2G/on-demand-vms/internal/master/pki"
	"github.com/Rashid2G/on-demand-vms/internal/store"
	"github.com/Rashid2G/on-demand-vms/internal/store/db"
)

type contextKey string

const (
	AuthContextKey contextKey = "compute_auth_context"
	SessionCookieName         = "compute_session"
)

// AuthContext contains the caller's verified identity, active organization, role, and scopes.
type AuthContext struct {
	User      *db.User    `json:"user"`
	APIKey    *db.APIKey  `json:"api_key,omitempty"`
	Session   *db.Session `json:"session,omitempty"`
	OrgID     string      `json:"org_id"`
	Role      string      `json:"role"`
	Scopes    []string    `json:"scopes"`
	AuthType  string      `json:"auth_type"` // "api_key", "session", "admin_secret"
	ClientIP  string      `json:"client_ip"`
}

// Manager orchestrates user registration, logins, API keys, sessions, and request authentication.
type Manager struct {
	queries        *db.Queries
	masterKey      *pki.MasterKey
	keyMgr         *KeyManager
	sessionMgr     *SessionManager
	adminBootstrap string // optional static admin bearer secret
}

// NewManager creates an authentication and identity manager.
func NewManager(queries *db.Queries, masterKey *pki.MasterKey, sessionTTL time.Duration, adminSecret string) *Manager {
	return &Manager{
		queries:        queries,
		masterKey:      masterKey,
		keyMgr:         NewKeyManager(queries, masterKey),
		sessionMgr:     NewSessionManager(queries, sessionTTL),
		adminBootstrap: adminSecret,
	}
}

// KeyManager returns the underlying API key manager.
func (m *Manager) KeyManager() *KeyManager {
	return m.keyMgr
}

// SessionManager returns the underlying session manager.
func (m *Manager) SessionManager() *SessionManager {
	return m.sessionMgr
}

// Signup registers a new user, hashes password with Argon2id, and provisions their default organization.
func (m *Manager) Signup(ctx context.Context, email, username, displayName, password, role string) (*db.User, error) {
	email = strings.TrimSpace(email)
	username = strings.TrimSpace(strings.ToLower(username))
	if email == "" || username == "" || password == "" {
		return nil, errors.New("email, username, and password are required")
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

	// Create user's personal organization
	orgID := "org_" + strings.TrimPrefix(userID, "usr_")
	org := &db.Organization{
		ID:        orgID,
		Name:      fmt.Sprintf("%s's Workspace", username),
		Slug:      username,
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}
	_ = m.queries.CreateOrganization(ctx, org)

	_ = m.queries.AddOrganizationMember(ctx, &db.OrganizationMember{
		OrganizationID: orgID,
		UserID:         userID,
		Role:           "owner",
		CreatedAt:      now,
	})

	return user, nil
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

// AuthenticateRequest inspects incoming HTTP headers or cookies to establish caller identity.
func (m *Manager) AuthenticateRequest(r *http.Request) (*AuthContext, error) {
	clientIP := extractClientIP(r)

	// 1. Check Bearer token or X-API-Key
	authHeader := r.Header.Get("Authorization")
	var token string
	if strings.HasPrefix(authHeader, "Bearer ") {
		token = strings.TrimPrefix(authHeader, "Bearer ")
	} else if keyHeader := r.Header.Get("X-API-Key"); keyHeader != "" {
		token = keyHeader
	}

	token = strings.TrimSpace(token)

	// Admin Bootstrap Secret Check
	if m.adminBootstrap != "" && token != "" {
		if subtle.ConstantTimeCompare([]byte(token), []byte(m.adminBootstrap)) == 1 {
			adminUser := &db.User{
				ID:          "usr_system_admin",
				Email:       "admin@compute.local",
				Username:    "admin",
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

	// API Key Authentication (format: mfk_... or cmp_...)
	if strings.HasPrefix(token, "mfk_") || strings.HasPrefix(token, "cmp_") {
		// Normalize prefix for KeyManager if using cmp_ format
		lookupToken := token
		if strings.HasPrefix(token, "cmp_") {
			lookupToken = "mfk_" + strings.TrimPrefix(token, "cmp_")
		}
		user, keyRecord, err := m.keyMgr.AuthenticateToken(r.Context(), lookupToken, clientIP)
		if err != nil {
			return nil, err
		}

		orgID := r.Header.Get("X-Compute-Org")
		if orgID == "" {
			orgID = "org_" + strings.TrimPrefix(user.ID, "usr_")
		}

		return &AuthContext{
			User:     user,
			APIKey:   keyRecord,
			OrgID:    orgID,
			Role:     user.Role,
			Scopes:   []string{"*"},
			AuthType: "api_key",
			ClientIP: clientIP,
		}, nil
	}

	// 2. Cookie Authentication (Session)
	cookie, err := r.Cookie(SessionCookieName)
	if err == nil && cookie.Value != "" {
		sessionToken := cookie.Value
		user, session, err := m.sessionMgr.AuthenticateSession(r.Context(), sessionToken)
		if err != nil {
			return nil, err
		}

		orgID := r.Header.Get("X-Compute-Org")
		if orgID == "" {
			orgID = "org_" + strings.TrimPrefix(user.ID, "usr_")
		}

		return &AuthContext{
			User:     user,
			Session:  session,
			OrgID:    orgID,
			Role:     user.Role,
			Scopes:   []string{"*"},
			AuthType: "session",
			ClientIP: clientIP,
		}, nil
	}

	return nil, errors.New("no valid authentication token or session provided")
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

// GetLimits returns quota and limit metrics for a user.
func (m *Manager) GetLimits(ctx context.Context, userID string) (*quota.Quota, *db.QuotaUsage, error) {
	user, err := m.queries.GetUserByID(ctx, userID)
	if err != nil {
		return nil, nil, err
	}

	limits := quota.DefaultQuota()
	if user.QuotaJSON.Valid && user.QuotaJSON.String != "" {
		_ = json.Unmarshal([]byte(user.QuotaJSON.String), &limits)
	}

	usage, err := m.queries.GetUserQuotaUsage(ctx, userID)
	if err != nil {
		return nil, nil, err
	}

	return &limits, usage, nil
}
