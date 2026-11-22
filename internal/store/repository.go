package store

import (
	"context"
	"database/sql"

	storeDB "github.com/Rashid2G/compute/internal/store/db"
)

// Repository defines the unified persistence interface for auth, control-plane,
// compute, and cluster operations.
type Repository interface {
	// Transactions
	WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error

	// Users
	CreateUser(ctx context.Context, u *storeDB.User) error
	GetUserByID(ctx context.Context, id string) (*storeDB.User, error)
	GetUserByEmailOrUsername(ctx context.Context, identifier string) (*storeDB.User, error)
	ListUsers(ctx context.Context) ([]*storeDB.User, error)
	DeleteUser(ctx context.Context, id string) error
	UpdateUserFailedLogins(ctx context.Context, id string, failures int, lockedUntil *int64) error
	ResetUserFailedLogins(ctx context.Context, id string) error
	UpdateUserAdminBootstrap(ctx context.Context, id, email, username, displayName, passwordHash, role, status string) error

	// Sessions
	CreateSession(ctx context.Context, s *storeDB.Session) error
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*storeDB.Session, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteUserSessions(ctx context.Context, userID string) error

	// API Keys
	CreateAPIKey(ctx context.Context, k *storeDB.APIKey) error
	GetAPIKeyByID(ctx context.Context, id string) (*storeDB.APIKey, error)
	GetAPIKeyByNameOrID(ctx context.Context, userID, idOrName string) (*storeDB.APIKey, error)
	ListAPIKeysByUser(ctx context.Context, userID string) ([]*storeDB.APIKey, error)
	RevokeAPIKey(ctx context.Context, id, userID string) error
	UpdateAPIKeyLastUsed(ctx context.Context, id string, ip string) error

	// Organizations
	CreateOrganization(ctx context.Context, org *storeDB.Organization) error
	GetOrganizationByID(ctx context.Context, id string) (*storeDB.Organization, error)
	GetOrganizationBySlug(ctx context.Context, slug string) (*storeDB.Organization, error)
	ListOrganizationsForUser(ctx context.Context, userID string) ([]*storeDB.Organization, error)
	AddOrganizationMember(ctx context.Context, member *storeDB.OrganizationMember) error
	GetOrganizationMember(ctx context.Context, orgID, userID string) (*storeDB.OrganizationMember, error)
	UpdateOrganizationIdentity(ctx context.Context, id, name, slug string) error

	// Quotas
	GetUserQuotaUsage(ctx context.Context, userID string) (*storeDB.QuotaUsage, error)

	// VPCs & Networking
	CreateVPC(ctx context.Context, vpc *storeDB.VPC) error
	GetVPC(ctx context.Context, ownerID, idOrName string) (*storeDB.VPC, error)
	ListVPCs(ctx context.Context, ownerID string) ([]*storeDB.VPC, error)
	DeleteVPC(ctx context.Context, ownerID, idOrName string) error

	// Volumes & Storage
	CreateVolume(ctx context.Context, vol *storeDB.Volume) error
	GetVolume(ctx context.Context, ownerID, idOrName string) (*storeDB.Volume, error)
	ListVolumes(ctx context.Context, ownerID string) ([]*storeDB.Volume, error)
	DeleteVolume(ctx context.Context, ownerID, idOrName string) error

	// Images
	CreateImage(ctx context.Context, img *storeDB.Image) error
	GetImage(ctx context.Context, ownerID, idOrName string) (*storeDB.Image, error)

	// Templates
	CreateTemplate(ctx context.Context, tpl *storeDB.Template) error
	GetTemplate(ctx context.Context, ownerID, idOrName string) (*storeDB.Template, error)
	ListTemplates(ctx context.Context, ownerID string) ([]*storeDB.Template, error)

	// Instances (ADR-004)
	CreateInstance(ctx context.Context, inst *storeDB.Instance) error
	GetInstance(ctx context.Context, ownerID, idOrName string) (*storeDB.Instance, error)
	ListInstances(ctx context.Context, ownerID string) ([]*storeDB.Instance, error)
	DeleteInstance(ctx context.Context, ownerID, idOrName string) error
}

// Compile-time check that Store implements Repository
var _ Repository = (*Store)(nil)

func (s *Store) CreateUser(ctx context.Context, u *storeDB.User) error {
	return s.Queries().CreateUser(ctx, u)
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*storeDB.User, error) {
	return s.ReadQueries().GetUserByID(ctx, id)
}

func (s *Store) GetUserByEmailOrUsername(ctx context.Context, identifier string) (*storeDB.User, error) {
	return s.ReadQueries().GetUserByEmailOrUsername(ctx, identifier)
}

func (s *Store) ListUsers(ctx context.Context) ([]*storeDB.User, error) {
	return s.ReadQueries().ListUsers(ctx)
}

func (s *Store) DeleteUser(ctx context.Context, id string) error {
	return s.Queries().DeleteUser(ctx, id)
}

func (s *Store) UpdateUserFailedLogins(ctx context.Context, id string, failures int, lockedUntil *int64) error {
	return s.Queries().UpdateUserFailedLogins(ctx, id, failures, lockedUntil)
}

func (s *Store) ResetUserFailedLogins(ctx context.Context, id string) error {
	return s.Queries().ResetUserFailedLogins(ctx, id)
}

func (s *Store) UpdateUserAdminBootstrap(ctx context.Context, id, email, username, displayName, passwordHash, role, status string) error {
	return s.Queries().UpdateUserAdminBootstrap(ctx, id, email, username, displayName, passwordHash, role, status)
}

func (s *Store) CreateSession(ctx context.Context, sess *storeDB.Session) error {
	return s.Queries().CreateSession(ctx, sess)
}

func (s *Store) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*storeDB.Session, error) {
	return s.ReadQueries().GetSessionByTokenHash(ctx, tokenHash)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	return s.Queries().DeleteSession(ctx, id)
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	return s.Queries().DeleteUserSessions(ctx, userID)
}

func (s *Store) CreateAPIKey(ctx context.Context, k *storeDB.APIKey) error {
	return s.Queries().CreateAPIKey(ctx, k)
}

func (s *Store) GetAPIKeyByID(ctx context.Context, id string) (*storeDB.APIKey, error) {
	return s.ReadQueries().GetAPIKeyByID(ctx, id)
}

func (s *Store) GetAPIKeyByNameOrID(ctx context.Context, userID, idOrName string) (*storeDB.APIKey, error) {
	return s.ReadQueries().GetAPIKeyByNameOrID(ctx, userID, idOrName)
}

func (s *Store) ListAPIKeysByUser(ctx context.Context, userID string) ([]*storeDB.APIKey, error) {
	return s.ReadQueries().ListAPIKeysByUser(ctx, userID)
}

func (s *Store) RevokeAPIKey(ctx context.Context, id, userID string) error {
	return s.Queries().RevokeAPIKey(ctx, id, userID)
}

func (s *Store) UpdateAPIKeyLastUsed(ctx context.Context, id string, ip string) error {
	return s.Queries().UpdateAPIKeyLastUsed(ctx, id, ip)
}

func (s *Store) CreateOrganization(ctx context.Context, org *storeDB.Organization) error {
	return s.Queries().CreateOrganization(ctx, org)
}

func (s *Store) GetOrganizationByID(ctx context.Context, id string) (*storeDB.Organization, error) {
	return s.ReadQueries().GetOrganizationByID(ctx, id)
}

func (s *Store) GetOrganizationBySlug(ctx context.Context, slug string) (*storeDB.Organization, error) {
	return s.ReadQueries().GetOrganizationBySlug(ctx, slug)
}

func (s *Store) ListOrganizationsForUser(ctx context.Context, userID string) ([]*storeDB.Organization, error) {
	return s.ReadQueries().ListOrganizationsForUser(ctx, userID)
}

func (s *Store) AddOrganizationMember(ctx context.Context, member *storeDB.OrganizationMember) error {
	return s.Queries().AddOrganizationMember(ctx, member)
}

func (s *Store) GetOrganizationMember(ctx context.Context, orgID, userID string) (*storeDB.OrganizationMember, error) {
	return s.ReadQueries().GetOrganizationMember(ctx, orgID, userID)
}

func (s *Store) UpdateOrganizationIdentity(ctx context.Context, id, name, slug string) error {
	return s.Queries().UpdateOrganizationIdentity(ctx, id, name, slug)
}

func (s *Store) GetUserQuotaUsage(ctx context.Context, userID string) (*storeDB.QuotaUsage, error) {
	return s.ReadQueries().GetUserQuotaUsage(ctx, userID)
}

func (s *Store) CreateVPC(ctx context.Context, vpc *storeDB.VPC) error {
	return s.Queries().CreateVPC(ctx, vpc)
}

func (s *Store) GetVPC(ctx context.Context, ownerID, idOrName string) (*storeDB.VPC, error) {
	return s.ReadQueries().GetVPC(ctx, ownerID, idOrName)
}

func (s *Store) ListVPCs(ctx context.Context, ownerID string) ([]*storeDB.VPC, error) {
	return s.ReadQueries().ListVPCs(ctx, ownerID)
}

func (s *Store) DeleteVPC(ctx context.Context, ownerID, idOrName string) error {
	return s.Queries().DeleteVPC(ctx, ownerID, idOrName)
}

func (s *Store) CreateVolume(ctx context.Context, vol *storeDB.Volume) error {
	return s.Queries().CreateVolume(ctx, vol)
}

func (s *Store) GetVolume(ctx context.Context, ownerID, idOrName string) (*storeDB.Volume, error) {
	return s.ReadQueries().GetVolume(ctx, ownerID, idOrName)
}

func (s *Store) ListVolumes(ctx context.Context, ownerID string) ([]*storeDB.Volume, error) {
	return s.ReadQueries().ListVolumes(ctx, ownerID)
}

func (s *Store) DeleteVolume(ctx context.Context, ownerID, idOrName string) error {
	return s.Queries().DeleteVolume(ctx, ownerID, idOrName)
}

func (s *Store) CreateImage(ctx context.Context, img *storeDB.Image) error {
	return s.Queries().CreateImage(ctx, img)
}

func (s *Store) GetImage(ctx context.Context, ownerID, idOrName string) (*storeDB.Image, error) {
	return s.ReadQueries().GetImage(ctx, ownerID, idOrName)
}

func (s *Store) CreateTemplate(ctx context.Context, tpl *storeDB.Template) error {
	return s.Queries().CreateTemplate(ctx, tpl)
}

func (s *Store) GetTemplate(ctx context.Context, ownerID, idOrName string) (*storeDB.Template, error) {
	return s.ReadQueries().GetTemplate(ctx, ownerID, idOrName)
}

func (s *Store) ListTemplates(ctx context.Context, ownerID string) ([]*storeDB.Template, error) {
	return s.ReadQueries().ListTemplates(ctx, ownerID)
}

func (s *Store) CreateInstance(ctx context.Context, inst *storeDB.Instance) error {
	return s.Queries().CreateInstance(ctx, inst)
}

func (s *Store) GetInstance(ctx context.Context, ownerID, idOrName string) (*storeDB.Instance, error) {
	return s.ReadQueries().GetInstance(ctx, ownerID, idOrName)
}

func (s *Store) ListInstances(ctx context.Context, ownerID string) ([]*storeDB.Instance, error) {
	return s.ReadQueries().ListInstances(ctx, ownerID)
}

func (s *Store) DeleteInstance(ctx context.Context, ownerID, idOrName string) error {
	return s.Queries().DeleteInstance(ctx, ownerID, idOrName)
}
