package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	"github.com/Rashid2G/on-demand-vms/internal/master/auth/quota"
	storeDB "github.com/Rashid2G/on-demand-vms/internal/store/db"
	"github.com/Rashid2G/on-demand-vms/pkg/api"
)

type SignupRequest struct {
	Email       string `json:"email"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

type LoginRequest struct {
	Username string `json:"username"` // username or email
	Password string `json:"password"`
}

type CreateAPIKeyRequest struct {
	Name      string `json:"name"`
	ExpiresIn int64  `json:"expires_in_seconds,omitempty"`
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminUsername := strings.TrimSpace(s.cfg.Server.AdminUsername)
	if adminUsername == "" && s.authMgr != nil {
		adminUsername, _ = s.authMgr.AdminIdentity()
	}
	if adminUsername == "" {
		adminUsername = "admin"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(api.AuthStatusResponse{
		AuthEnabled:   s.cfg.Server.AuthEnabled,
		SignupEnabled: s.cfg.Server.SignupEnabled,
		AdminUsername: adminUsername,
	})
}

func (s *Server) handleAuthSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.authMgr == nil {
		httpError(w, "authentication manager not enabled", http.StatusServiceUnavailable)
		return
	}

	callerIsAdmin := false
	if authCtx, err := s.authMgr.AuthenticateRequest(r); err == nil && authCtx != nil && isAdminAuthContext(authCtx) {
		callerIsAdmin = true
	}

	if !s.cfg.Server.SignupEnabled && !callerIsAdmin {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "signup_disabled",
			"message": "Public registration is disabled on this Compute instance. Sign in with an administrator account or set COMPUTE_SIGNUP_ENABLED=true in .env.",
		})
		return
	}

	var req SignupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	user, err := s.authMgr.Signup(r.Context(), req.Email, req.Username, req.DisplayName, req.Password, "user")
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp := map[string]interface{}{
		"status": "ok",
		"user": map[string]interface{}{
			"id":           user.ID,
			"email":        user.Email,
			"username":     user.Username,
			"display_name": user.DisplayName,
			"role":         user.Role,
			"status":       user.Status,
			"created_at":   user.CreatedAt,
		},
	}

	// For public self-registration, automatically issue a session and set the
	// HTTP-only cookie so the user is immediately signed in.
	if s.cfg.Server.SignupEnabled && !callerIsAdmin {
		token, csrf, _, sessErr := s.authMgr.SessionManager().CreateSession(r.Context(), user.ID, r.RemoteAddr, r.UserAgent())
		if sessErr == nil && token != "" {
			http.SetCookie(w, &http.Cookie{
				Name:     auth.SessionCookieName,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   7 * 24 * 3600,
			})
			resp["token"] = token
			resp["csrf"] = csrf
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.authMgr == nil {
		httpError(w, "authentication manager not enabled", http.StatusServiceUnavailable)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	ip := r.RemoteAddr
	userAgent := r.UserAgent()
	user, token, csrf, err := s.authMgr.Login(r.Context(), req.Username, req.Password, ip, userAgent)
	if err != nil {
		httpError(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Set HTTP-only cookie
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	})

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"token":  token,
		"csrf":   csrf,
		"user": map[string]interface{}{
			"id":           user.ID,
			"email":        user.Email,
			"username":     user.Username,
			"display_name": user.DisplayName,
			"role":         user.Role,
			"status":       user.Status,
		},
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Revoke the backing session so the token cannot be replayed.
	if s.authMgr != nil {
		sessionID := ""
		if authCtx, err := s.authMgr.AuthenticateRequest(r); err == nil && authCtx != nil && authCtx.Session != nil {
			sessionID = authCtx.Session.ID
		}
		if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && cookie.Value != "" {
			_ = s.authMgr.SessionManager().RevokeSession(r.Context(), cookie.Value)
		}
		if sessionID != "" {
			_ = s.authMgr.SessionManager().RevokeSessionByID(r.Context(), sessionID)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"user": map[string]interface{}{
			"id":           authCtx.User.ID,
			"email":        authCtx.User.Email,
			"username":     authCtx.User.Username,
			"display_name": authCtx.User.DisplayName,
			"role":         authCtx.User.Role,
			"status":       authCtx.User.Status,
		},
		"org_id":    authCtx.OrgID,
		"role":      authCtx.Role,
		"auth_type": authCtx.AuthType,
		"scopes":    authCtx.Scopes,
	})
}

func (s *Server) handleAuthKeys(w http.ResponseWriter, r *http.Request) {
	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if s.metaStore == nil {
			_ = json.NewEncoder(w).Encode([]interface{}{})
			return
		}
		queries := storeDB.New(s.metaStore.Reader())
		keys, err := queries.ListAPIKeysByUser(r.Context(), authCtx.User.ID)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if keys == nil {
			keys = []*storeDB.APIKey{}
		}
		_ = json.NewEncoder(w).Encode(keys)

	case http.MethodPost:
		var req CreateAPIKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			req.Name = "api-key"
		}

		var expiresAt *int64
		if req.ExpiresIn > 0 {
			exp := storeDB.NowMillis() + (req.ExpiresIn * 1000)
			expiresAt = &exp
		}

		token, keyRecord, err := s.authMgr.KeyManager().GenerateAPIKey(r.Context(), authCtx.User.ID, req.Name, expiresAt)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Provide standard cmp_live_ prefix format
		standardToken := token
		if strings.HasPrefix(token, "cmp_") && !strings.HasPrefix(token, "cmp_live_") {
			standardToken = strings.Replace(token, "cmp_", "cmp_live_", 1)
		} else if strings.HasPrefix(token, "mfk_") {
			standardToken = strings.Replace(token, "mfk_", "cmp_live_", 1)
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"token":  standardToken,
			"key":    keyRecord,
		})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAuthKeyActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/keys/")
	keyID := strings.Trim(path, "/")
	if keyID == "" {
		httpError(w, "missing key id", http.StatusBadRequest)
		return
	}

	if s.metaStore == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}

	queries := storeDB.New(s.metaStore.Writer())
	if err := queries.RevokeAPIKey(r.Context(), keyID, authCtx.User.ID); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "revoked",
		"id":     keyID,
	})
}

func (s *Server) handleOrgs(w http.ResponseWriter, r *http.Request) {
	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if s.metaStore == nil {
		_ = json.NewEncoder(w).Encode([]interface{}{})
		return
	}

	queries := storeDB.New(s.metaStore.Reader())

	switch r.Method {
	case http.MethodGet:
		orgs, err := queries.ListOrganizationsForUser(r.Context(), authCtx.User.ID)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if orgs == nil {
			orgs = []*storeDB.Organization{}
		}
		_ = json.NewEncoder(w).Encode(orgs)

	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			httpError(w, "name is required", http.StatusBadRequest)
			return
		}
		if req.Slug == "" {
			req.Slug = strings.ToLower(strings.ReplaceAll(req.Name, " ", "-"))
		}

		now := storeDB.NowMillis()
		orgID := fmt.Sprintf("org_%d", now)
		org := &storeDB.Organization{
			ID:        orgID,
			Name:      req.Name,
			Slug:      req.Slug,
			Status:    "active",
			CreatedAt: now,
			UpdatedAt: now,
		}

		writerQueries := storeDB.New(s.metaStore.Writer())
		if err := writerQueries.CreateOrganization(r.Context(), org); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}

		_ = writerQueries.AddOrganizationMember(r.Context(), &storeDB.OrganizationMember{
			OrganizationID: orgID,
			UserID:         authCtx.User.ID,
			Role:           "owner",
			CreatedAt:      now,
		})

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(org)

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleOrgActions(w http.ResponseWriter, r *http.Request) {
	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/orgs/")
	orgID := strings.Trim(path, "/")
	if orgID == "" {
		httpError(w, "missing org id", http.StatusBadRequest)
		return
	}

	if s.metaStore == nil {
		httpError(w, "store not available", http.StatusInternalServerError)
		return
	}

	queries := storeDB.New(s.metaStore.Reader())
	org, err := queries.GetOrganizationByID(r.Context(), orgID)
	if err != nil {
		httpError(w, "organization not found", http.StatusNotFound)
		return
	}

	// Verify membership
	if authCtx.Role != "admin" {
		member, err := queries.GetOrganizationMember(r.Context(), orgID, authCtx.User.ID)
		if err != nil || member == nil {
			httpError(w, "organization not found", http.StatusNotFound)
			return
		}
	}

	_ = json.NewEncoder(w).Encode(org)
}

// handleLimits returns account resource limits and current usage.
func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authCtx := auth.GetAuth(r.Context())
	var userID string
	if authCtx != nil && authCtx.User != nil {
		userID = authCtx.User.ID
	}

	limits := quota.DefaultQuota()
	usage := storeDB.QuotaUsage{}
	if s.authMgr != nil && userID != "" {
		if l, u, err := s.authMgr.GetLimits(r.Context(), userID); err == nil && l != nil && u != nil {
			limits = *l
			usage = *u
		}
	} else if u, err := s.ownerUsage(r.Context(), userID); err == nil && u != nil {
		usage = *u
	}

	maxConcurrent := limits.RunningInstances
	if maxConcurrent <= 0 || !s.authEnabled() {
		maxConcurrent = 10
	}
	if authCtx != nil && authCtx.Role == "admin" && maxConcurrent < 100 {
		maxConcurrent = 100
	}

	resp := map[string]interface{}{
		"limits": map[string]interface{}{
			"concurrent_sandboxes": maxConcurrent,
			"max_duration_seconds": 86400, // 24 hours
			"max_vcpus":            limits.VCPUs,
			"max_memory_mb":        limits.MemoryMB,
			"starts_per_minute":    60,
			"starts_per_hour":      300,
			"starts_per_day":       1200,
			"instances":            limits.Instances,
			"running_instances":    limits.RunningInstances,
			"vcpus":                limits.VCPUs,
			"memory_mb":            limits.MemoryMB,
			"disk_gb":              limits.DiskGB,
			"api_keys":             limits.APIKeys,
		},
		"quota": limits,
		"usage": map[string]interface{}{
			"active_sandboxes":  usage.RunningInstances,
			"total_sandboxes":   usage.TotalInstances,
			"active_starts_min": 1,
			"active_starts_hr":  usage.TotalInstances,
			"active_starts_day": usage.TotalInstances,
			"instances":         usage.TotalInstances,
			"running_instances": usage.RunningInstances,
			"vcpus":             usage.AllocatedVCPUs,
			"memory_mb":         usage.AllocatedMemory,
			"disk_gb":           usage.AllocatedDiskMB / 1024,
			"api_keys":          usage.TotalAPIKeys,
		},
	}

	_ = json.NewEncoder(w).Encode(resp)
}

func sanitizeUserRecord(u *storeDB.User) map[string]interface{} {
	return map[string]interface{}{
		"id":           u.ID,
		"email":        u.Email,
		"username":     u.Username,
		"display_name": u.DisplayName,
		"role":         u.Role,
		"status":       u.Status,
		"created_at":   u.CreatedAt,
		"updated_at":   u.UpdatedAt,
	}
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if s.authMgr == nil {
		httpError(w, "authentication manager not enabled", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		users, err := s.authMgr.Users(r.Context())
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]map[string]interface{}, 0, len(users))
		for _, u := range users {
			if u != nil {
				out = append(out, sanitizeUserRecord(u))
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)

	case http.MethodPost:
		var req struct {
			Email       string `json:"email"`
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			Password    string `json:"password"`
			Role        string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		role := strings.TrimSpace(strings.ToLower(req.Role))
		if role == "" {
			role = "user"
		}
		user, err := s.authMgr.Signup(r.Context(), req.Email, req.Username, req.DisplayName, req.Password, role)
		if err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "ok",
			"user":   sanitizeUserRecord(user),
		})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) resolveUserByIDOrUsername(ctx context.Context, idOrUsername string) (*storeDB.User, error) {
	user, err := s.authMgr.UserByID(ctx, idOrUsername)
	if err != nil {
		return nil, err
	}
	if user == nil {
		user, err = s.authMgr.UserByUsername(ctx, idOrUsername)
		if err != nil {
			return nil, err
		}
	}
	return user, nil
}

func (s *Server) handleUserActions(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAccess(w, r) {
		return
	}
	if s.authMgr == nil {
		httpError(w, "authentication manager not enabled", http.StatusServiceUnavailable)
		return
	}

	idOrUsername := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/users/"), "/")
	if idOrUsername == "" {
		httpError(w, "missing user id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		user, err := s.resolveUserByIDOrUsername(r.Context(), idOrUsername)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if user == nil {
			httpError(w, "user not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sanitizeUserRecord(user))

	case http.MethodDelete:
		user, err := s.resolveUserByIDOrUsername(r.Context(), idOrUsername)
		if err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if user == nil {
			httpError(w, "user not found", http.StatusNotFound)
			return
		}
		authCtx := auth.GetAuth(r.Context())
		if authCtx != nil && authCtx.User != nil && authCtx.User.ID == user.ID {
			httpError(w, "cannot delete your own account", http.StatusBadRequest)
			return
		}
		configuredAdminUsername, _ := s.authMgr.AdminIdentity()
		if user.ID == "usr_system_admin" ||
			user.Username == configuredAdminUsername ||
			(s.cfg.Server.AdminUsername != "" && strings.EqualFold(user.Username, strings.TrimSpace(s.cfg.Server.AdminUsername))) {
			httpError(w, "cannot delete primary bootstrap administrator", http.StatusForbidden)
			return
		}
		if err := s.authMgr.DeleteUser(r.Context(), user.ID); err != nil {
			if errors.Is(err, storeDB.ErrNotFound) {
				httpError(w, "user not found", http.StatusNotFound)
				return
			}
			if errors.Is(err, auth.ErrProtectedAdmin) {
				httpError(w, err.Error(), http.StatusForbidden)
				return
			}
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Revoke any scoped access API keys in the control-plane store created by this user.
		if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
			if keys, err := s.vmMgr.GetStore().ListAccessAPIKeysByCreator(r.Context(), user.Username); err == nil {
				for _, k := range keys {
					if k != nil && k.ID != "" {
						_ = s.vmMgr.GetStore().DeleteAccessAPIKey(r.Context(), k.ID)
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "deleted",
			"id":     user.ID,
		})

	default:
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func parseQueryInt(r *http.Request, key string, defaultVal int) int {
	valStr := r.URL.Query().Get(key)
	if valStr == "" {
		return defaultVal
	}
	if v, err := strconv.Atoi(valStr); err == nil {
		return v
	}
	return defaultVal
}
