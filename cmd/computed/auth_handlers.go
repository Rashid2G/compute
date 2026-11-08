package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	storeDB "github.com/Rashid2G/on-demand-vms/internal/store/db"
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

func (s *Server) handleAuthSignup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.authMgr == nil {
		httpError(w, "authentication manager not enabled", http.StatusServiceUnavailable)
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

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
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
	})
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

		// Also provide standard cmp_live_ prefix format
		standardToken := strings.Replace(token, "mfk_", "cmp_live_", 1)

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

// handleLimits returns account resource limits and current usage (matching Boat.dev GET /limits & E2B).
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

	var runningVMs int
	var totalVMs int
	if s.vmMgr != nil && s.vmMgr.GetStore() != nil {
		vms, _ := s.vmMgr.GetStore().ListVMsByOwner(r.Context(), userID)
		totalVMs = len(vms)
		for _, v := range vms {
			if v.Status == "running" {
				runningVMs++
			}
		}
	}

	maxConcurrent := 10
	if authCtx != nil && authCtx.Role == "admin" {
		maxConcurrent = 100
	}

	resp := map[string]interface{}{
		"limits": map[string]interface{}{
			"concurrent_sandboxes": maxConcurrent,
			"max_duration_seconds": 86400, // 24 hours
			"max_vcpus":            32,
			"max_memory_mb":        65536,
			"starts_per_minute":    60,
			"starts_per_hour":      300,
			"starts_per_day":       1200,
		},
		"usage": map[string]interface{}{
			"active_sandboxes":  runningVMs,
			"total_sandboxes":   totalVMs,
			"active_starts_min": 1,
			"active_starts_hr":  totalVMs,
			"active_starts_day": totalVMs,
		},
	}

	_ = json.NewEncoder(w).Encode(resp)
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
