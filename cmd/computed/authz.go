package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/config"
	"github.com/Rashid2G/on-demand-vms/internal/db"
	"github.com/Rashid2G/on-demand-vms/internal/master/auth"
	storeDB "github.com/Rashid2G/on-demand-vms/internal/store/db"
	"github.com/Rashid2G/on-demand-vms/internal/vm"
)

// safeConfig returns the daemon configuration as it may be exposed to the
// caller. Bootstrap secrets are always redacted; non-admin callers also never
// receive local paths.
func (s *Server) safeConfig(isAdmin bool) config.GlobalConfig {
	cfg := s.cfg
	cfg.Resources = s.vmMgr.GetResourceLimits()
	cfg.Network = s.vmMgr.GetNetworkManager().GetConfig()
	cfg.Defaults = s.vmMgr.GetDefaults()
	cfg.Server.AdminSecret = ""
	cfg.Server.AdminPassword = ""
	if !isAdmin {
		cfg.Server.AuthDBPath = ""
		cfg.Server.DBPath = ""
	}
	return cfg
}

// authEnabled reports whether tenant isolation and quota enforcement are
// configured. It intentionally reports true even when the authentication
// manager failed to initialize: callers must fail closed rather than fall back
// to the legacy single-tenant identity.
func (s *Server) authEnabled() bool {
	return s.cfg.Server.AuthEnabled
}

// authUnavailable reports whether authentication is configured but its backend
// failed to initialize. Protected routes must reject requests in that state.
func (s *Server) authUnavailable(w http.ResponseWriter) bool {
	if !s.cfg.Server.AuthEnabled || s.authMgr != nil {
		return false
	}
	httpError(w, "authentication backend unavailable", http.StatusServiceUnavailable)
	return true
}

// currentOwnerID returns the effective owner for the caller and whether the
// caller is an administrator. In legacy (auth-disabled) mode every caller is an
// administrator acting as the "default" owner.
func (s *Server) currentOwnerID(r *http.Request) (string, bool) {
	if !s.authEnabled() {
		return "default", true
	}
	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil {
		return "", false
	}
	if isAdminAuthContext(authCtx) {
		return authCtx.User.ID, true
	}
	return authCtx.User.ID, false
}

// callerIdentity returns the authenticated owner ID, admin flag, and whether the
// caller may proceed. When authentication is disabled every caller is a legacy
// "default" administrator. When it is enabled a missing AuthContext fails
// closed instead of silently granting the legacy identity.
func (s *Server) callerIdentity(r *http.Request) (ownerID string, isAdmin bool, ok bool) {
	if !s.authEnabled() {
		return "default", true, true
	}
	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil || strings.TrimSpace(authCtx.User.ID) == "" {
		return "", false, false
	}
	return authCtx.User.ID, isAdminAuthContext(authCtx), true
}

// authorizeCreateRequest resolves the caller identity for a VM create/clone
// request and populates the manager's internal ownership fields. It writes a
// 401 response and returns ok=false when an authenticated identity is required
// but absent, so creation can never fall back to owner "default".
func (s *Server) authorizeCreateRequest(w http.ResponseWriter, r *http.Request, req *vm.CreateRequest) (ownerID string, isAdmin bool, ok bool) {
	ownerID, isAdmin, ok = s.callerIdentity(r)
	if !ok {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return "", false, false
	}
	if isAdmin {
		if strings.TrimSpace(req.OwnerID) == "" {
			req.OwnerID = ownerID
		}
		req.AllowCrossOwner = true
		return ownerID, true, true
	}
	// Non-admin callers can never spoof ownership.
	req.OwnerID = ownerID
	req.AuthorizedOwnerIDs = s.ownerIDsFor(r)
	if len(req.AuthorizedOwnerIDs) == 0 {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return "", false, false
	}
	return ownerID, false, true
}

// ownerIDsFor returns every identity a caller may own resources under: their
// user ID plus their active organization, if different.
func (s *Server) ownerIDsFor(r *http.Request) []string {
	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil {
		return nil
	}
	ids := []string{authCtx.User.ID}
	if authCtx.OrgID != "" && authCtx.OrgID != authCtx.User.ID {
		ids = append(ids, authCtx.OrgID)
	}
	return ids
}

// canAccessOwner reports whether the caller may access a resource owned by
// resourceOwnerID. Non-admin callers must match their user ID or organization;
// the legacy "default" owner is never accessible when auth is enabled.
func (s *Server) canAccessOwner(r *http.Request, resourceOwnerID string) bool {
	if !s.authEnabled() {
		return true
	}
	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil || authCtx.User == nil {
		return false
	}
	if isAdminAuthContext(authCtx) {
		return true
	}
	if resourceOwnerID == "" {
		return false
	}
	if resourceOwnerID == authCtx.User.ID {
		return true
	}
	return authCtx.OrgID != "" && resourceOwnerID == authCtx.OrgID
}

// accessibleOwnerSet returns every owner identity the caller may see. It is
// used to filter records that carry an owner column (network allocations,
// queue items) so same-named resources from different tenants never leak.
func (s *Server) accessibleOwnerSet(r *http.Request) map[string]bool {
	set := make(map[string]bool)
	for _, id := range s.ownerIDsFor(r) {
		if id != "" {
			set[id] = true
		}
	}
	return set
}

// resolveAccessibleVM resolves a VM by internal ID, external ID, name, IP, or
// public IP while respecting tenant ownership. Administrators (and legacy
// auth-disabled deployments) keep the global lookup. This prevents duplicate
// names from resolving to another tenant's VM.
func (s *Server) resolveAccessibleVM(ctx context.Context, r *http.Request, idOrName string) (*db.VM, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return nil, nil
	}
	if _, isAdmin := s.currentOwnerID(r); isAdmin || !s.authEnabled() {
		return s.vmMgr.GetVM(ctx, idOrName)
	}
	for _, ownerID := range s.ownerIDsFor(r) {
		if ownerID == "" {
			continue
		}
		v, err := s.vmMgr.GetStore().GetVMByOwner(ctx, ownerID, idOrName)
		if err != nil {
			return nil, err
		}
		if v != nil && s.canAccessOwner(r, v.OwnerID) {
			return v, nil
		}
	}
	return nil, nil
}

// resolveAccessibleVolume resolves a volume by internal ID, external ID, or
// name while respecting tenant ownership. Duplicate volume names across tenants
// are allowed, so an owner-scoped lookup is required. Administrators (and
// legacy auth-disabled deployments) keep the global lookup.
func (s *Server) resolveAccessibleVolume(ctx context.Context, r *http.Request, idOrName string) (*db.Volume, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return nil, nil
	}
	if _, isAdmin := s.currentOwnerID(r); isAdmin || !s.authEnabled() {
		return s.vmMgr.GetStore().GetVolume(ctx, idOrName)
	}
	for _, ownerID := range s.ownerIDsFor(r) {
		if ownerID == "" {
			continue
		}
		vol, err := s.vmMgr.GetStore().GetVolumeByOwner(ctx, ownerID, idOrName)
		if err != nil {
			return nil, err
		}
		if vol != nil && s.canAccessOwner(r, vol.OwnerID) {
			return vol, nil
		}
	}
	return nil, nil
}

// resolveAccessibleSSHKey resolves an SSH key by ID or name within the caller's
// owner scope so duplicate key names across tenants never collide.
func (s *Server) resolveAccessibleSSHKey(ctx context.Context, r *http.Request, idOrName string) (*db.SSHKey, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return nil, nil
	}
	if _, isAdmin := s.currentOwnerID(r); isAdmin || !s.authEnabled() {
		return s.vmMgr.GetStore().GetSSHKey(ctx, idOrName)
	}
	for _, ownerID := range s.ownerIDsFor(r) {
		if ownerID == "" {
			continue
		}
		key, err := s.vmMgr.GetStore().GetSSHKeyByOwner(ctx, ownerID, idOrName)
		if err != nil {
			return nil, err
		}
		if key != nil && s.canAccessOwner(r, key.OwnerID) {
			return key, nil
		}
	}
	return nil, nil
}

// vmIdentifier returns a lookup key that uniquely identifies a VM even when
// different tenants use the same name.
func vmIdentifier(v *db.VM) string {
	if v == nil {
		return ""
	}
	if v.InternalID != "" {
		return v.InternalID
	}
	return v.ID
}

// resolveAccessibleImage resolves a custom image name/ID while respecting
// tenant ownership. Duplicate image names across tenants are allowed.
func (s *Server) resolveAccessibleImage(ctx context.Context, r *http.Request, name string) (*db.CustomImage, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return nil, nil
	}
	if _, isAdmin := s.currentOwnerID(r); isAdmin || !s.authEnabled() {
		return s.vmMgr.GetImage(ctx, name)
	}
	for _, ownerID := range s.ownerIDsFor(r) {
		if ownerID == "" {
			continue
		}
		img, err := s.vmMgr.GetStore().GetImageByOwner(ctx, ownerID, name)
		if err != nil {
			return nil, err
		}
		if img != nil && s.canAccessOwner(r, img.OwnerID) {
			return img, nil
		}
	}
	// Legacy "default" images are not shared with authenticated tenants.
	return nil, nil
}

// resolveAccessibleTemplate resolves a template name/ID while respecting tenant
// ownership. Duplicate template names across tenants are allowed.
func (s *Server) resolveAccessibleTemplate(ctx context.Context, r *http.Request, name string) (*db.Template, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return nil, nil
	}
	if _, isAdmin := s.currentOwnerID(r); isAdmin || !s.authEnabled() {
		return s.vmMgr.GetTemplate(ctx, name)
	}
	for _, ownerID := range s.ownerIDsFor(r) {
		if ownerID == "" {
			continue
		}
		tpl, err := s.vmMgr.GetStore().GetTemplateByOwner(ctx, ownerID, name)
		if err != nil {
			return nil, err
		}
		if tpl != nil && s.canAccessOwner(r, tpl.OwnerID) {
			return tpl, nil
		}
	}
	// Legacy "default" templates are not shared with authenticated tenants.
	return nil, nil
}

func imageIdentifier(img *db.CustomImage) string {
	if img == nil {
		return ""
	}
	if img.InternalID != "" {
		return img.InternalID
	}
	return img.ID
}

func templateIdentifier(tpl *db.Template) string {
	if tpl == nil {
		return ""
	}
	if tpl.InternalID != "" {
		return tpl.InternalID
	}
	return tpl.ID
}

// requireAdminAccess writes a 403 response and returns false when the caller is
// not an administrator and authentication is enabled.
func (s *Server) requireAdminAccess(w http.ResponseWriter, r *http.Request) bool {
	if !s.authEnabled() {
		return true
	}
	if s.authUnavailable(w) {
		return false
	}
	_, isAdmin := s.currentOwnerID(r)
	if isAdmin {
		return true
	}
	httpError(w, "administrative privileges required", http.StatusForbidden)
	return false
}

// authenticateRequestIfNeeded establishes an AuthContext for routes that are not
// wrapped by wrapAuth (for example the preview proxy) so ownership checks work.
func (s *Server) authenticateRequestIfNeeded(r *http.Request) (*http.Request, *auth.AuthContext, error) {
	if authCtx := auth.GetAuth(r.Context()); authCtx != nil {
		return r, authCtx, nil
	}
	if !s.authEnabled() {
		return r, nil, nil
	}
	if s.authMgr == nil {
		return r, nil, fmt.Errorf("authentication backend unavailable")
	}
	authCtx, err := s.authMgr.AuthenticateRequest(r)
	if err != nil {
		return r, nil, err
	}
	ctx := context.WithValue(r.Context(), auth.AuthContextKey, authCtx)
	return r.WithContext(ctx), authCtx, nil
}

// listVMsForOwners lists every VM owned by any of ownerIDs, de-duplicated.
func (s *Server) listVMsForOwners(ctx context.Context, ownerIDs []string) []*db.VM {
	store := s.vmMgr.GetStore()
	seen := make(map[string]bool)
	var out []*db.VM
	for _, oid := range ownerIDs {
		if oid == "" {
			continue
		}
		vms, err := store.ListVMsByOwner(ctx, oid)
		if err != nil {
			continue
		}
		for _, v := range vms {
			if v == nil || seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	return out
}

// listVolumesForOwners lists every volume owned by any of ownerIDs, de-duplicated.
func (s *Server) listVolumesForOwners(ctx context.Context, ownerIDs []string) []*db.Volume {
	store := s.vmMgr.GetStore()
	seen := make(map[string]bool)
	var out []*db.Volume
	for _, oid := range ownerIDs {
		if oid == "" {
			continue
		}
		vols, err := store.ListVolumesByOwner(ctx, oid)
		if err != nil {
			continue
		}
		for _, v := range vols {
			if v == nil || seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	return out
}

// listJobsForOwners lists every job owned by any of ownerIDs, de-duplicated.
func (s *Server) listJobsForOwners(ctx context.Context, ownerIDs []string) []*db.Job {
	store := s.vmMgr.GetStore()
	seen := make(map[string]bool)
	var out []*db.Job
	for _, oid := range ownerIDs {
		if oid == "" {
			continue
		}
		jobs, err := store.ListJobs(ctx, oid)
		if err != nil {
			continue
		}
		for _, j := range jobs {
			if j == nil || seen[j.ID] {
				continue
			}
			seen[j.ID] = true
			out = append(out, j)
		}
	}
	return out
}

// listAccessibleVMs lists every VM the caller can see.
func (s *Server) listAccessibleVMs(ctx context.Context, r *http.Request) ([]*db.VM, error) {
	_, isAdmin := s.currentOwnerID(r)
	if isAdmin {
		return s.vmMgr.ListVMs(ctx)
	}
	if s.ownerIDsFor(r) == nil {
		return []*db.VM{}, nil
	}
	return s.listVMsForOwners(ctx, s.ownerIDsFor(r)), nil
}

// listAccessibleVolumes lists every volume the caller can see.
func (s *Server) listAccessibleVolumes(ctx context.Context, r *http.Request) ([]*db.Volume, error) {
	_, isAdmin := s.currentOwnerID(r)
	store := s.vmMgr.GetStore()
	if isAdmin {
		return store.ListVolumes(ctx)
	}
	return s.listVolumesForOwners(ctx, s.ownerIDsFor(r)), nil
}

// listAccessibleJobs lists every job the caller can see.
func (s *Server) listAccessibleJobs(ctx context.Context, r *http.Request) ([]*db.Job, error) {
	_, isAdmin := s.currentOwnerID(r)
	store := s.vmMgr.GetStore()
	if isAdmin {
		return store.ListJobs(ctx, "")
	}
	return s.listJobsForOwners(ctx, s.ownerIDsFor(r)), nil
}

// ownerUsage computes live resource usage from the control-plane store.
func (s *Server) ownerUsage(ctx context.Context, ownerID string) (*storeDB.QuotaUsage, error) {
	usage := &storeDB.QuotaUsage{}
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil {
		return usage, nil
	}
	store := s.vmMgr.GetStore()

	vols, err := store.ListVolumesByOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	rootVolumes := make(map[string]*db.Volume, len(vols))
	for _, vol := range vols {
		if vol == nil {
			continue
		}
		if strings.HasPrefix(vol.ID, "vol_root_") {
			rootVolumes[vol.ID] = vol
			continue
		}
		usage.AllocatedDiskMB += vol.SizeGB * 1024
	}

	vms, err := store.ListVMsByOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	defaultDiskMB := 0
	if defaults := s.vmMgr.GetDefaults(); defaults.DiskSizeMB > 0 {
		defaultDiskMB = defaults.DiskSizeMB
	}
	if defaultDiskMB <= 0 {
		defaultDiskMB = config.DiskSize50GB
	}
	for _, v := range vms {
		if v == nil {
			continue
		}
		usage.TotalInstances++
		if v.Status == db.StatusRunning {
			usage.RunningInstances++
		}
		usage.AllocatedVCPUs += v.VCPUs
		usage.AllocatedMemory += v.MemoryMB

		// Live rootfs disk usage is derived from the VM record so quotas apply
		// immediately after creation, without waiting for a /volumes sync.
		diskMB := v.DiskMB
		if diskMB <= 0 {
			if rv := rootVolumes[rootVolumeIDFor(v)]; rv != nil && rv.SizeGB > 0 {
				diskMB = rv.SizeGB * 1024
			} else {
				diskMB = defaultDiskMB
			}
		}
		usage.AllocatedDiskMB += diskMB
	}

	if s.metaStore != nil && ownerID != "" {
		queries := storeDB.New(s.metaStore.Reader())
		if keys, err := queries.ListAPIKeysByUser(ctx, ownerID); err == nil {
			usage.TotalAPIKeys = len(keys)
		}
	}
	return usage, nil
}

// estimateRequestResources mirrors the VM manager's defaulting so quota checks
// can run before a VM is created. Templates (whether named through the template
// field or resolved from the image field) contribute their resource specs.
func (s *Server) estimateRequestResources(ctx context.Context, req vm.CreateRequest) (vcpus, memoryMB, diskMB int) {
	defaults := s.vmMgr.GetDefaults()
	vcpus = req.VCPUs
	memoryMB = req.MemoryMB
	diskMB = req.DiskMB

	var tpl *db.Template
	if req.Template != "" {
		tpl, _ = s.vmMgr.GetTemplate(ctx, req.Template)
	} else if req.Image != "" {
		// Only consult templates when the name is not a custom image, matching
		// the manager's resolution order.
		if img, err := s.vmMgr.GetImage(ctx, req.Image); err != nil || img == nil {
			tpl, _ = s.vmMgr.GetTemplate(ctx, req.Image)
		}
	}
	if tpl != nil {
		if req.Tier == "" && vcpus <= 0 && tpl.VCPUs > 0 {
			vcpus = tpl.VCPUs
		}
		if memoryMB <= 0 && tpl.MemoryMB > 0 {
			memoryMB = tpl.MemoryMB
		}
		if diskMB <= 0 && tpl.DiskMB > 0 {
			diskMB = tpl.DiskMB
		}
	}

	if vcpus <= 0 {
		switch lowerTier(req.Tier) {
		case "1vcpu", "1", "micro", "nano":
			vcpus = 1
			if memoryMB <= 0 {
				memoryMB = 2048
			}
		case "2vcpu", "2", "small", "standard":
			vcpus = 2
			if memoryMB <= 0 {
				memoryMB = 4096
			}
		case "4vcpu", "4", "medium", "compute", "default":
			vcpus = 4
			if memoryMB <= 0 {
				memoryMB = 8192
			}
		}
	}
	if vcpus <= 0 {
		vcpus = defaults.VCPUs
		if vcpus <= 0 {
			vcpus = 1
		}
	}
	if memoryMB <= 0 {
		memoryMB = defaults.MemoryMB
	}
	if diskMB <= 0 {
		diskMB = defaults.DiskSizeMB
		if diskMB < 0 {
			diskMB = 0
		}
	}

	if normalizedVCPUs, normalizedMem, normalizedDisk, err := config.ValidateAndNormalizeSpecs(vcpus, memoryMB, diskMB); err == nil {
		return normalizedVCPUs, normalizedMem, normalizedDisk
	}
	return vcpus, memoryMB, diskMB
}

// configureAuthIntegrations wires live usage and scoped-key verification into
// the authentication manager.
func (s *Server) configureAuthIntegrations() {
	if s.authMgr == nil {
		return
	}
	s.authMgr.SetUsageProvider(func(ctx context.Context, userID string) (*storeDB.QuotaUsage, error) {
		return s.ownerUsage(ctx, userID)
	})
	s.authMgr.SetExternalAuthenticator(s.authenticateAccessAPIKey)
}

// authenticateAccessAPIKey verifies scoped keys stored in access_api_keys and
// maps them back to the issuing user.
func (s *Server) authenticateAccessAPIKey(ctx context.Context, token, clientIP string) (*auth.AuthContext, error) {
	if s.vmMgr == nil || s.vmMgr.GetStore() == nil || s.metaStore == nil {
		return nil, fmt.Errorf("scoped key store unavailable")
	}
	key, err := s.vmMgr.GetStore().GetAccessAPIKeyByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if key == nil || key.Token == "" {
		return nil, fmt.Errorf("invalid access key")
	}
	if isAccessKeyExpired(key.ExpiresAt) {
		return nil, fmt.Errorf("access key has expired")
	}

	queries := storeDB.New(s.metaStore.Reader())
	adminUsername := strings.TrimSpace(strings.ToLower(s.cfg.Server.AdminUsername))
	if adminUsername == "" {
		adminUsername = "admin"
	}
	adminEmail := strings.TrimSpace(s.cfg.Server.AdminEmail)
	if adminEmail == "" {
		adminEmail = "admin@compute.local"
	}

	user, err := queries.GetUserByEmailOrUsername(ctx, key.CreatedBy)
	if (err != nil || user == nil) && (key.CreatedBy == "admin" || key.CreatedBy == "usr_system_admin" || strings.EqualFold(key.CreatedBy, adminUsername)) {
		for _, candidate := range []string{adminUsername, adminEmail, "admin"} {
			if u, lookupErr := queries.GetUserByEmailOrUsername(ctx, candidate); lookupErr == nil && u != nil && u.Role == "admin" {
				user = u
				err = nil
				break
			}
		}
		if err != nil || user == nil {
			user = &storeDB.User{
				ID:          "usr_system_admin",
				Email:       adminEmail,
				Username:    adminUsername,
				DisplayName: "System Administrator",
				Role:        "admin",
				Status:      "active",
			}
			err = nil
		}
	}
	if err != nil || user == nil {
		return nil, fmt.Errorf("access key owner not found")
	}

	if user.Status != "" && user.Status != "active" {
		return nil, fmt.Errorf("access key owner account is inactive (status: %s)", user.Status)
	}

	authCtx := s.authContextForAccessKey(user, key, clientIP)
	if authCtx == nil {
		return nil, fmt.Errorf("access key owner account is inactive (status: %s)", user.Status)
	}
	return authCtx, nil
}

// authContextForAccessKey maps a verified access key to an AuthContext without
// inadvertently inheriting full administrator privileges when scopes are narrow.
func (s *Server) authContextForAccessKey(user *storeDB.User, key *db.AccessAPIKey, clientIP string) *auth.AuthContext {
	if user == nil || (user.Status != "" && user.Status != "active") {
		return nil
	}

	scopes := []string{"*"}
	if strings.TrimSpace(key.Scope) != "" {
		scopes = strings.FieldsFunc(key.Scope, func(r rune) bool { return r == ',' || r == ' ' })
		if len(scopes) == 0 {
			scopes = []string{"*"}
		}
	}

	// A scoped key MUST NOT inherit isAdmin = true simply because its creator was an admin!
	// Only set isAdmin = true if key.Role == "admin" OR if key.Scopes explicitly contains "admin" / "*" / "all".
	// If key.Role != "admin" and scopes are restricted (e.g. read:vms, write:vms, etc.), set isAdmin = false.
	hasAdminScope := false
	for _, sc := range scopes {
		scl := strings.ToLower(strings.TrimSpace(sc))
		if scl == "admin" || scl == "*" || scl == "all" {
			hasAdminScope = true
			break
		}
	}

	isAdmin := false
	if user.Role == "admin" && (key.Role == "admin" || hasAdminScope) {
		isAdmin = true
	}

	effectiveRole := "user"
	clonedUser := *user
	if isAdmin {
		effectiveRole = "admin"
	} else {
		clonedUser.Role = "user"
	}

	orgID := "org_" + strings.TrimPrefix(user.ID, "usr_")
	return &auth.AuthContext{
		User:     &clonedUser,
		OrgID:    orgID,
		Role:     effectiveRole,
		Scopes:   scopes,
		AuthType: "api_key",
		ClientIP: clientIP,
	}
}

func isAccessKeyExpired(expiresAt string) bool {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return false
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02"} {
		if ts, err := time.Parse(layout, expiresAt); err == nil {
			return time.Now().After(ts)
		}
	}
	if ts, ok := parseRelativeExpiration(expiresAt); ok {
		return time.Now().After(ts)
	}
	return false
}

func parseRelativeExpiration(s string) (time.Time, bool) {
	raw := strings.TrimSpace(strings.ToLower(s))
	raw = strings.TrimPrefix(raw, "in ")
	raw = strings.TrimSpace(raw)

	for _, suffix := range []string{"days", "day", "d"} {
		if strings.HasSuffix(raw, suffix) {
			numStr := strings.TrimSpace(strings.TrimSuffix(raw, suffix))
			if days, err := strconv.Atoi(numStr); err == nil {
				return time.Now().UTC().AddDate(0, 0, days), true
			}
		}
	}

	for _, suffix := range []string{"hours", "hour"} {
		if strings.HasSuffix(raw, suffix) {
			numStr := strings.TrimSpace(strings.TrimSuffix(raw, suffix))
			if hrs, err := strconv.Atoi(numStr); err == nil {
				return time.Now().UTC().Add(time.Duration(hrs) * time.Hour), true
			}
		}
	}

	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().UTC().Add(d), true
	}

	return time.Time{}, false
}

func normalizeAccessKeyExpiration(expiresAt string) string {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" || strings.EqualFold(expiresAt, "in 90 days") || strings.EqualFold(expiresAt, "90d") {
		return time.Now().UTC().AddDate(0, 0, 90).Format(time.RFC3339)
	}

	for _, layout := range []string{time.RFC3339, time.RFC3339Nano} {
		if ts, err := time.Parse(layout, expiresAt); err == nil {
			return ts.UTC().Format(time.RFC3339)
		}
	}

	if ts, err := time.Parse("2006-01-02", expiresAt); err == nil {
		return time.Date(ts.Year(), ts.Month(), ts.Day(), 23, 59, 59, 0, time.UTC).Format(time.RFC3339)
	}

	if ts, ok := parseRelativeExpiration(expiresAt); ok {
		return ts.UTC().Format(time.RFC3339)
	}

	return expiresAt
}

func lowerTier(tier string) string {
	out := make([]byte, 0, len(tier))
	for i := 0; i < len(tier); i++ {
		c := tier[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == ' ' || c == '\t' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// isAdminAuthContext reports whether a verified auth context belongs to an admin.
func isAdminAuthContext(authCtx *auth.AuthContext) bool {
	if authCtx == nil {
		return false
	}
	if authCtx.Role != "" {
		return authCtx.Role == "admin"
	}
	return authCtx.User != nil && authCtx.User.Role == "admin"
}

// enforceScope checks that the authenticated caller's key scopes permit the
// requested HTTP action and target resource. If scopes are insufficient, it
// writes a 403 Forbidden response and returns false.
func (s *Server) enforceScope(w http.ResponseWriter, r *http.Request) bool {
	authCtx := auth.GetAuth(r.Context())
	if authCtx == nil {
		return true
	}
	// Sessions and admin bootstrap secret are not scoped API keys.
	if authCtx.AuthType != "api_key" {
		return true
	}

	// Always allow self-identity inspection for any valid token.
	if r.URL.Path == "/api/v1/auth/me" && r.Method == http.MethodGet {
		return true
	}

	action := "read"
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		action = "read"
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		action = "write"
	default:
		action = "write"
	}

	resource := extractResourceFromPath(r.URL.Path)

	if isSSHEndpoint(r.URL.Path) {
		action = "ssh"
		resource = "vms"
	}

	if !s.scopePermits(authCtx, action, resource) {
		httpError(w, fmt.Sprintf("forbidden: insufficient scope for %s:%s", action, resource), http.StatusForbidden)
		return false
	}
	return true
}

func isSSHEndpoint(path string) bool {
	clean := strings.TrimPrefix(path, "/api/v1/")
	clean = strings.Trim(clean, "/")
	parts := strings.Split(clean, "/")
	if len(parts) >= 1 && parts[0] == "ssh" {
		return true
	}
	if len(parts) >= 2 && (parts[0] == "vms" || parts[0] == "sandboxes") && parts[len(parts)-1] == "ssh" {
		return true
	}
	return false
}

func extractResourceFromPath(path string) string {
	clean := strings.TrimPrefix(path, "/api/v1/")
	clean = strings.TrimPrefix(clean, "/")
	parts := strings.Split(clean, "/")
	if len(parts) == 0 || parts[0] == "" {
		return "root"
	}
	return parts[0]
}

func (s *Server) scopePermits(authCtx *auth.AuthContext, action, resource string) bool {
	if authCtx == nil {
		return false
	}
	if len(authCtx.Scopes) == 0 {
		return false
	}

	isAdmin := isAdminAuthContext(authCtx)

	// SSH access requires ssh:vms, ssh:sandboxes, *:vms, vms:*, *, or admin scope.
	// Narrow scopes such as read:vms or write:vms MUST NOT grant SSH access.
	if action == "ssh" {
		for _, raw := range authCtx.Scopes {
			scope := strings.ToLower(strings.TrimSpace(raw))
			if scope == "*" || scope == "all" {
				return true
			}
			if scope == "admin" && isAdmin {
				return true
			}
			if scope == "ssh:vms" || scope == "vms:ssh" || scope == "ssh:sandboxes" || scope == "sandboxes:ssh" ||
				scope == "*:vms" || scope == "vms:*" || scope == "*:sandboxes" || scope == "sandboxes:*" ||
				scope == "ssh:*" || scope == "*:ssh" {
				return true
			}
		}
		return false
	}

	adminResources := map[string]bool{
		"webhooks":       true,
		"workers":        true,
		"worker-tokens":  true,
		"pools":          true,
		"agent-profiles": true,
		"environments":   true,
		"users":          true,
	}

	// If this is an admin-only resource, only admin keys can access it.
	if adminResources[resource] && !isAdmin {
		return false
	}

	for _, raw := range authCtx.Scopes {
		scope := strings.ToLower(strings.TrimSpace(raw))
		if scope == "" {
			continue
		}

		// Full access wildcards
		if scope == "*" || scope == "all" {
			return true
		}

		// Admin scope
		if scope == "admin" {
			if isAdmin {
				return true
			}
			continue
		}

		// Standard user scope grants read and write to non-admin resources
		if scope == "user" {
			if !adminResources[resource] {
				return true
			}
			continue
		}

		// Action wildcards: read:*, write:*
		if scope == action+":*" {
			if !adminResources[resource] || isAdmin {
				return true
			}
		}

		// Resource wildcards: *:vms, vms:*, vms
		if scope == "*:"+resource || scope == resource+":*" || scope == resource {
			return true
		}

		// Exact action:resource match (e.g. read:vms, write:vms)
		if scope == action+":"+resource || scope == resource+":"+action {
			return true
		}

		// Handle resource aliases (e.g. sandboxes <-> vms, worker-tokens <-> workers, api-keys <-> keys)
		if resource == "sandboxes" {
			if scope == "*:vms" || scope == "vms:*" || scope == "vms" || scope == action+":vms" || scope == "vms:"+action {
				return true
			}
		} else if resource == "vms" {
			if scope == "*:sandboxes" || scope == "sandboxes:*" || scope == "sandboxes" || scope == action+":sandboxes" || scope == "sandboxes:"+action {
				return true
			}
		} else if resource == "worker-tokens" {
			if scope == "*:workers" || scope == "workers:*" || scope == "workers" || scope == action+":workers" || scope == "workers:"+action {
				if !adminResources[resource] || isAdmin {
					return true
				}
			}
		} else if resource == "api-keys" {
			if scope == "*:keys" || scope == "keys:*" || scope == "keys" || scope == action+":keys" || scope == "keys:"+action {
				return true
			}
		} else if resource == "keys" {
			if scope == "*:api-keys" || scope == "api-keys:*" || scope == "api-keys" || scope == action+":api-keys" || scope == "api-keys:"+action {
				return true
			}
		}
	}

	return false
}

// enforceCreateQuota rejects a new VM when it would exceed the owner's quota.
func (s *Server) enforceCreateQuota(ctx context.Context, r *http.Request, ownerID string, vcpus, memoryMB, diskMB int) error {
	if !s.authEnabled() || ownerID == "" {
		return nil
	}
	if s.authMgr == nil {
		return fmt.Errorf("quota check failed: authentication backend unavailable")
	}
	if isAdminAuthContext(auth.GetAuth(r.Context())) {
		return nil
	}
	limits, usage, err := s.authMgr.GetLimits(ctx, ownerID)
	if err != nil {
		// Fail closed: a quota lookup failure must not silently bypass limits.
		return fmt.Errorf("quota check failed: %w", err)
	}
	if limits == nil || usage == nil {
		return fmt.Errorf("quota check failed: quota limits unavailable")
	}
	if limits.Instances > 0 && usage.TotalInstances+1 > limits.Instances {
		return fmt.Errorf("quota exceeded: instance limit is %d (currently %d)", limits.Instances, usage.TotalInstances)
	}
	if limits.VCPUs > 0 && usage.AllocatedVCPUs+vcpus > limits.VCPUs {
		return fmt.Errorf("quota exceeded: vcpu limit is %d (currently %d, requested %d)", limits.VCPUs, usage.AllocatedVCPUs, vcpus)
	}
	if limits.MemoryMB > 0 && usage.AllocatedMemory+memoryMB > limits.MemoryMB {
		return fmt.Errorf("quota exceeded: memory limit is %d MB (currently %d MB, requested %d MB)", limits.MemoryMB, usage.AllocatedMemory, memoryMB)
	}
	if limits.DiskGB > 0 && usage.AllocatedDiskMB+diskMB > limits.DiskGB*1024 {
		return fmt.Errorf("quota exceeded: disk limit is %d GB (currently %d MB, requested %d MB)", limits.DiskGB, usage.AllocatedDiskMB, diskMB)
	}
	return nil
}

// enforceDiskQuota rejects new disk allocation (for example a data volume) when
// it would exceed the owner's disk quota.
func (s *Server) enforceDiskQuota(ctx context.Context, r *http.Request, ownerID string, diskMB int) error {
	if !s.authEnabled() || ownerID == "" || diskMB <= 0 {
		return nil
	}
	if s.authMgr == nil {
		return fmt.Errorf("quota check failed: authentication backend unavailable")
	}
	if isAdminAuthContext(auth.GetAuth(r.Context())) {
		return nil
	}
	limits, usage, err := s.authMgr.GetLimits(ctx, ownerID)
	if err != nil {
		return fmt.Errorf("quota check failed: %w", err)
	}
	if limits == nil || usage == nil {
		return fmt.Errorf("quota check failed: quota limits unavailable")
	}
	if limits.DiskGB > 0 && usage.AllocatedDiskMB+diskMB > limits.DiskGB*1024 {
		return fmt.Errorf("quota exceeded: disk limit is %d GB (currently %d MB, requested %d MB)", limits.DiskGB, usage.AllocatedDiskMB, diskMB)
	}
	return nil
}

// enforceRunQuota rejects starting a VM when it would exceed the owner's
// concurrent-running quota.
func (s *Server) enforceRunQuota(ctx context.Context, r *http.Request, v *db.VM) error {
	if v == nil || !s.authEnabled() || v.OwnerID == "" {
		return nil
	}
	if s.authMgr == nil {
		return fmt.Errorf("quota check failed: authentication backend unavailable")
	}
	if v.Status == db.StatusRunning {
		return nil
	}
	if isAdminAuthContext(auth.GetAuth(r.Context())) {
		return nil
	}
	limits, _, err := s.authMgr.GetLimits(ctx, v.OwnerID)
	if err != nil {
		return fmt.Errorf("quota check failed: %w", err)
	}
	if limits == nil {
		return fmt.Errorf("quota check failed: quota limits unavailable")
	}

	running, err := s.vmMgr.GetStore().ListVMsByOwner(ctx, v.OwnerID)
	if err != nil {
		return fmt.Errorf("quota check failed: %w", err)
	}
	runningCount, runningVCPUs, runningMemory := 0, 0, 0
	for _, rv := range running {
		if rv == nil || rv.Status != db.StatusRunning || rv.ID == v.ID {
			continue
		}
		runningCount++
		runningVCPUs += rv.VCPUs
		runningMemory += rv.MemoryMB
	}
	if limits.RunningInstances > 0 && runningCount+1 > limits.RunningInstances {
		return fmt.Errorf("quota exceeded: concurrent running instance limit is %d", limits.RunningInstances)
	}
	if limits.VCPUs > 0 && runningVCPUs+v.VCPUs > limits.VCPUs {
		return fmt.Errorf("quota exceeded: running vcpu limit is %d", limits.VCPUs)
	}
	if limits.MemoryMB > 0 && runningMemory+v.MemoryMB > limits.MemoryMB {
		return fmt.Errorf("quota exceeded: running memory limit is %d MB", limits.MemoryMB)
	}
	return nil
}
