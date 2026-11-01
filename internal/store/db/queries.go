package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("resource conflict")
)

// Queries wraps a sql.DB or sql.Tx for executing queries.
type Queries struct {
	db DBTX
}

type DBTX interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func New(db DBTX) *Queries {
	return &Queries{db: db}
}

// ===== Users =====

func (q *Queries) CreateUser(ctx context.Context, u *User) error {
	query := `
		INSERT INTO users (
			id, email, username, display_name, password_hash, role, status,
			must_change_pw, failed_logins, locked_until, quota_json, created_at, updated_at, revision
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := q.db.ExecContext(ctx, query,
		u.ID, u.Email, u.Username, u.DisplayName, u.PasswordHash, u.Role, u.Status,
		u.MustChangePW, u.FailedLogins, u.LockedUntil, u.QuotaJSON, u.CreatedAt, u.UpdatedAt, u.Revision,
	)
	return err
}

func (q *Queries) GetUserByID(ctx context.Context, id string) (*User, error) {
	query := `SELECT id, email, username, display_name, password_hash, role, status, must_change_pw, failed_logins, locked_until, quota_json, created_at, updated_at, revision FROM users WHERE id = ?`
	row := q.db.QueryRowContext(ctx, query, id)
	return scanUser(row)
}

func (q *Queries) GetUserByEmailOrUsername(ctx context.Context, identifier string) (*User, error) {
	query := `SELECT id, email, username, display_name, password_hash, role, status, must_change_pw, failed_logins, locked_until, quota_json, created_at, updated_at, revision FROM users WHERE email = ? COLLATE NOCASE OR username = ?`
	row := q.db.QueryRowContext(ctx, query, identifier, identifier)
	return scanUser(row)
}

func (q *Queries) UpdateUserFailedLogins(ctx context.Context, id string, failures int, lockedUntil *int64) error {
	query := `UPDATE users SET failed_logins = ?, locked_until = ?, updated_at = ? WHERE id = ?`
	_, err := q.db.ExecContext(ctx, query, failures, lockedUntil, NowMillis(), id)
	return err
}

func (q *Queries) ResetUserFailedLogins(ctx context.Context, id string) error {
	query := `UPDATE users SET failed_logins = 0, locked_until = NULL, updated_at = ? WHERE id = ?`
	_, err := q.db.ExecContext(ctx, query, NowMillis(), id)
	return err
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var lockedUntil sql.NullInt64
	err := row.Scan(
		&u.ID, &u.Email, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &u.Status,
		&u.MustChangePW, &u.FailedLogins, &lockedUntil, &u.QuotaJSON, &u.CreatedAt, &u.UpdatedAt, &u.Revision,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if lockedUntil.Valid {
		u.LockedUntil = &lockedUntil.Int64
	}
	return &u, nil
}

// ===== API Keys =====

func (q *Queries) CreateAPIKey(ctx context.Context, k *APIKey) error {
	query := `
		INSERT INTO api_keys (id, user_id, name, secret_hash, prefix, scopes, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := q.db.ExecContext(ctx, query,
		k.ID, k.UserID, k.Name, k.SecretHash, k.Prefix, k.Scopes, k.ExpiresAt, k.CreatedAt,
	)
	return err
}

func (q *Queries) GetAPIKeyByID(ctx context.Context, id string) (*APIKey, error) {
	query := `SELECT id, user_id, name, secret_hash, prefix, scopes, expires_at, last_used_at, last_used_ip, created_at, revoked_at FROM api_keys WHERE id = ?`
	row := q.db.QueryRowContext(ctx, query, id)
	var k APIKey
	var expiresAt, lastUsedAt, revokedAt sql.NullInt64
	var lastUsedIP sql.NullString
	err := row.Scan(
		&k.ID, &k.UserID, &k.Name, &k.SecretHash, &k.Prefix, &k.Scopes,
		&expiresAt, &lastUsedAt, &lastUsedIP, &k.CreatedAt, &revokedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.Int64
	}
	if lastUsedAt.Valid {
		k.LastUsedAt = &lastUsedAt.Int64
	}
	if lastUsedIP.Valid {
		k.LastUsedIP = &lastUsedIP.String
	}
	if revokedAt.Valid {
		k.RevokedAt = &revokedAt.Int64
	}
	return &k, nil
}

func (q *Queries) RevokeAPIKey(ctx context.Context, id, userID string) error {
	query := `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`
	res, err := q.db.ExecContext(ctx, query, NowMillis(), id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (q *Queries) UpdateAPIKeyLastUsed(ctx context.Context, id string, ip string) error {
	query := `UPDATE api_keys SET last_used_at = ?, last_used_ip = ? WHERE id = ?`
	_, err := q.db.ExecContext(ctx, query, NowMillis(), ip, id)
	return err
}

// ===== Sessions =====

func (q *Queries) CreateSession(ctx context.Context, s *Session) error {
	query := `
		INSERT INTO sessions (id, user_id, token_hash, csrf_hash, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := q.db.ExecContext(ctx, query,
		s.ID, s.UserID, s.TokenHash, s.CSRFHash, s.IP, s.UserAgent, s.CreatedAt, s.LastSeenAt, s.ExpiresAt,
	)
	return err
}

func (q *Queries) GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*Session, error) {
	query := `SELECT id, user_id, token_hash, csrf_hash, ip, user_agent, created_at, last_seen_at, expires_at FROM sessions WHERE token_hash = ?`
	row := q.db.QueryRowContext(ctx, query, tokenHash)
	var s Session
	err := row.Scan(&s.ID, &s.UserID, &s.TokenHash, &s.CSRFHash, &s.IP, &s.UserAgent, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (q *Queries) DeleteSession(ctx context.Context, id string) error {
	query := `DELETE FROM sessions WHERE id = ?`
	_, err := q.db.ExecContext(ctx, query, id)
	return err
}

// ===== Quota Calculation =====

type QuotaUsage struct {
	TotalInstances   int
	RunningInstances int
	AllocatedVCPUs   int
	AllocatedMemory  int
	AllocatedDiskMB  int
	TotalVPCs        int
	TotalAPIKeys     int
}

func (q *Queries) GetUserQuotaUsage(ctx context.Context, userID string) (*QuotaUsage, error) {
	var usage QuotaUsage

	// Compute instance counts, vCPUs, memory
	err := q.db.QueryRowContext(ctx, `
		SELECT 
			COUNT(*),
			COALESCE(SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(vcpus), 0),
			COALESCE(SUM(memory_mb), 0)
		FROM instances 
		WHERE owner_id = ? AND desired_state != 'deleted'
	`, userID).Scan(&usage.TotalInstances, &usage.RunningInstances, &usage.AllocatedVCPUs, &usage.AllocatedMemory)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate instance usage: %w", err)
	}

	// Volume sizes
	err = q.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size_mb), 0) FROM volumes WHERE owner_id = ? AND status != 'failed'
	`, userID).Scan(&usage.AllocatedDiskMB)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate storage usage: %w", err)
	}

	// Total VPCs
	err = q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vpcs WHERE owner_id = ?`, userID).Scan(&usage.TotalVPCs)
	if err != nil {
		return nil, fmt.Errorf("failed to count vpcs: %w", err)
	}

	// Total API keys
	err = q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE user_id = ? AND revoked_at IS NULL`, userID).Scan(&usage.TotalAPIKeys)
	if err != nil {
		return nil, fmt.Errorf("failed to count api keys: %w", err)
	}

	return &usage, nil
}

// ===== Instances =====

func (q *Queries) CreateInstance(ctx context.Context, inst *Instance) error {
	query := `
		INSERT INTO instances (
			id, owner_id, name, kind, image_id, template_id, vcpus, memory_mb,
			node_id, vpc_id, desired_state, state, state_reason, idle_policy_json,
			ports_json, env_json, cmd_json, restart_policy, last_active_at,
			started_at, created_at, updated_at, revision
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := q.db.ExecContext(ctx, query,
		inst.ID, inst.OwnerID, inst.Name, inst.Kind, inst.ImageID, inst.TemplateID,
		inst.VCPUs, inst.MemoryMB, inst.NodeID, inst.VPCID, inst.DesiredState,
		inst.State, inst.StateReason, inst.IdlePolicyJSON, inst.PortsJSON,
		inst.EnvJSON, inst.CmdJSON, inst.RestartPolicy, inst.LastActiveAt,
		inst.StartedAt, inst.CreatedAt, inst.UpdatedAt, inst.Revision,
	)
	return err
}

func (q *Queries) GetInstance(ctx context.Context, ownerID, idOrName string) (*Instance, error) {
	query := `
		SELECT id, owner_id, name, kind, image_id, template_id, vcpus, memory_mb,
		       node_id, vpc_id, desired_state, state, state_reason, idle_policy_json,
		       ports_json, env_json, cmd_json, restart_policy, last_active_at,
		       started_at, created_at, updated_at, revision
		FROM instances
		WHERE (id = ? OR (name = ? AND (owner_id = ? OR ? = '')))
		ORDER BY CASE WHEN id = ? THEN 1 WHEN name = ? THEN 2 ELSE 3 END
		LIMIT 1;
	`
	row := q.db.QueryRowContext(ctx, query, idOrName, idOrName, ownerID, ownerID, idOrName, idOrName)
	var inst Instance
	var templateID, nodeID, cmdJSON sql.NullString
	var lastActiveAt, startedAt sql.NullInt64

	err := row.Scan(
		&inst.ID, &inst.OwnerID, &inst.Name, &inst.Kind, &inst.ImageID, &templateID,
		&inst.VCPUs, &inst.MemoryMB, &nodeID, &inst.VPCID, &inst.DesiredState,
		&inst.State, &inst.StateReason, &inst.IdlePolicyJSON, &inst.PortsJSON,
		&inst.EnvJSON, &cmdJSON, &inst.RestartPolicy, &lastActiveAt,
		&startedAt, &inst.CreatedAt, &inst.UpdatedAt, &inst.Revision,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if templateID.Valid {
		inst.TemplateID = &templateID.String
	}
	if nodeID.Valid {
		inst.NodeID = &nodeID.String
	}
	if cmdJSON.Valid {
		inst.CmdJSON = &cmdJSON.String
	}
	if lastActiveAt.Valid {
		inst.LastActiveAt = &lastActiveAt.Int64
	}
	if startedAt.Valid {
		inst.StartedAt = &startedAt.Int64
	}
	return &inst, nil
}

func (q *Queries) ListInstances(ctx context.Context, ownerID string) ([]*Instance, error) {
	query := `
		SELECT id, owner_id, name, kind, image_id, template_id, vcpus, memory_mb,
		       node_id, vpc_id, desired_state, state, state_reason, idle_policy_json,
		       ports_json, env_json, cmd_json, restart_policy, last_active_at,
		       started_at, created_at, updated_at, revision
		FROM instances
		WHERE (? = '' OR owner_id = ?)
		ORDER BY created_at DESC;
	`
	rows, err := q.db.QueryContext(ctx, query, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []*Instance
	for rows.Next() {
		var inst Instance
		var templateID, nodeID, cmdJSON sql.NullString
		var lastActiveAt, startedAt sql.NullInt64

		err := rows.Scan(
			&inst.ID, &inst.OwnerID, &inst.Name, &inst.Kind, &inst.ImageID, &templateID,
			&inst.VCPUs, &inst.MemoryMB, &nodeID, &inst.VPCID, &inst.DesiredState,
			&inst.State, &inst.StateReason, &inst.IdlePolicyJSON, &inst.PortsJSON,
			&inst.EnvJSON, &cmdJSON, &inst.RestartPolicy, &lastActiveAt,
			&startedAt, &inst.CreatedAt, &inst.UpdatedAt, &inst.Revision,
		)
		if err != nil {
			return nil, err
		}
		if templateID.Valid {
			inst.TemplateID = &templateID.String
		}
		if nodeID.Valid {
			inst.NodeID = &nodeID.String
		}
		if cmdJSON.Valid {
			inst.CmdJSON = &cmdJSON.String
		}
		if lastActiveAt.Valid {
			inst.LastActiveAt = &lastActiveAt.Int64
		}
		if startedAt.Valid {
			inst.StartedAt = &startedAt.Int64
		}
		instances = append(instances, &inst)
	}
	return instances, rows.Err()
}

func (q *Queries) DeleteInstance(ctx context.Context, ownerID, idOrName string) error {
	query := `DELETE FROM instances WHERE (id = ? OR (name = ? AND (owner_id = ? OR ? = '')));`
	res, err := q.db.ExecContext(ctx, query, idOrName, idOrName, ownerID, ownerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ===== Volumes =====

func (q *Queries) CreateVolume(ctx context.Context, vol *Volume) error {
	query := `
		INSERT INTO volumes (
			id, owner_id, name, node_id, size_mb, kind, source_image_id,
			source_volume_id, instance_id, device_index, read_only, status, used_mb, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := q.db.ExecContext(ctx, query,
		vol.ID, vol.OwnerID, vol.Name, vol.NodeID, vol.SizeMB, vol.Kind,
		vol.SourceImageID, vol.SourceVolumeID, vol.InstanceID, vol.DeviceIndex,
		vol.ReadOnly, vol.Status, vol.UsedMB, vol.CreatedAt,
	)
	return err
}

func (q *Queries) GetVolume(ctx context.Context, ownerID, idOrName string) (*Volume, error) {
	query := `
		SELECT id, owner_id, name, node_id, size_mb, kind, source_image_id,
		       source_volume_id, instance_id, device_index, read_only, status, used_mb, created_at
		FROM volumes
		WHERE (id = ? OR (name = ? AND (owner_id = ? OR ? = '')))
		ORDER BY CASE WHEN id = ? THEN 1 WHEN name = ? THEN 2 ELSE 3 END
		LIMIT 1;
	`
	row := q.db.QueryRowContext(ctx, query, idOrName, idOrName, ownerID, ownerID, idOrName, idOrName)
	var vol Volume
	var nodeID, sourceImageID, sourceVolumeID, instanceID sql.NullString
	var deviceIndex sql.NullInt64

	err := row.Scan(
		&vol.ID, &vol.OwnerID, &vol.Name, &nodeID, &vol.SizeMB, &vol.Kind,
		&sourceImageID, &sourceVolumeID, &instanceID, &deviceIndex,
		&vol.ReadOnly, &vol.Status, &vol.UsedMB, &vol.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if nodeID.Valid {
		vol.NodeID = &nodeID.String
	}
	if sourceImageID.Valid {
		vol.SourceImageID = &sourceImageID.String
	}
	if sourceVolumeID.Valid {
		vol.SourceVolumeID = &sourceVolumeID.String
	}
	if instanceID.Valid {
		vol.InstanceID = &instanceID.String
	}
	if deviceIndex.Valid {
		idx := int(deviceIndex.Int64)
		vol.DeviceIndex = &idx
	}
	return &vol, nil
}

func (q *Queries) ListVolumes(ctx context.Context, ownerID string) ([]*Volume, error) {
	query := `
		SELECT id, owner_id, name, node_id, size_mb, kind, source_image_id,
		       source_volume_id, instance_id, device_index, read_only, status, used_mb, created_at
		FROM volumes
		WHERE (? = '' OR owner_id = ?)
		ORDER BY created_at DESC;
	`
	rows, err := q.db.QueryContext(ctx, query, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var volumes []*Volume
	for rows.Next() {
		var vol Volume
		var nodeID, sourceImageID, sourceVolumeID, instanceID sql.NullString
		var deviceIndex sql.NullInt64

		err := rows.Scan(
			&vol.ID, &vol.OwnerID, &vol.Name, &nodeID, &vol.SizeMB, &vol.Kind,
			&sourceImageID, &sourceVolumeID, &instanceID, &deviceIndex,
			&vol.ReadOnly, &vol.Status, &vol.UsedMB, &vol.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		if nodeID.Valid {
			vol.NodeID = &nodeID.String
		}
		if sourceImageID.Valid {
			vol.SourceImageID = &sourceImageID.String
		}
		if sourceVolumeID.Valid {
			vol.SourceVolumeID = &sourceVolumeID.String
		}
		if instanceID.Valid {
			vol.InstanceID = &instanceID.String
		}
		if deviceIndex.Valid {
			idx := int(deviceIndex.Int64)
			vol.DeviceIndex = &idx
		}
		volumes = append(volumes, &vol)
	}
	return volumes, rows.Err()
}

func (q *Queries) DeleteVolume(ctx context.Context, ownerID, idOrName string) error {
	query := `DELETE FROM volumes WHERE (id = ? OR (name = ? AND (owner_id = ? OR ? = '')));`
	res, err := q.db.ExecContext(ctx, query, idOrName, idOrName, ownerID, ownerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ===== VPCs =====

func (q *Queries) CreateVPC(ctx context.Context, vpc *VPC) error {
	query := `
		INSERT INTO vpcs (id, owner_id, name, cidr, vni, is_default, egress, created_at, revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := q.db.ExecContext(ctx, query,
		vpc.ID, vpc.OwnerID, vpc.Name, vpc.CIDR, vpc.VNI, vpc.IsDefault, vpc.Egress, vpc.CreatedAt, vpc.Revision,
	)
	return err
}

func (q *Queries) GetVPC(ctx context.Context, ownerID, idOrName string) (*VPC, error) {
	query := `
		SELECT id, owner_id, name, cidr, vni, is_default, egress, created_at, revision
		FROM vpcs
		WHERE (id = ? OR (name = ? AND (owner_id = ? OR ? = '')))
		ORDER BY CASE WHEN id = ? THEN 1 WHEN name = ? THEN 2 ELSE 3 END
		LIMIT 1;
	`
	row := q.db.QueryRowContext(ctx, query, idOrName, idOrName, ownerID, ownerID, idOrName, idOrName)
	var vpc VPC
	err := row.Scan(&vpc.ID, &vpc.OwnerID, &vpc.Name, &vpc.CIDR, &vpc.VNI, &vpc.IsDefault, &vpc.Egress, &vpc.CreatedAt, &vpc.Revision)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &vpc, nil
}

func (q *Queries) ListVPCs(ctx context.Context, ownerID string) ([]*VPC, error) {
	query := `
		SELECT id, owner_id, name, cidr, vni, is_default, egress, created_at, revision
		FROM vpcs
		WHERE (? = '' OR owner_id = ?)
		ORDER BY created_at DESC;
	`
	rows, err := q.db.QueryContext(ctx, query, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vpcs []*VPC
	for rows.Next() {
		var vpc VPC
		err := rows.Scan(&vpc.ID, &vpc.OwnerID, &vpc.Name, &vpc.CIDR, &vpc.VNI, &vpc.IsDefault, &vpc.Egress, &vpc.CreatedAt, &vpc.Revision)
		if err != nil {
			return nil, err
		}
		vpcs = append(vpcs, &vpc)
	}
	return vpcs, rows.Err()
}

func (q *Queries) DeleteVPC(ctx context.Context, ownerID, idOrName string) error {
	query := `DELETE FROM vpcs WHERE (id = ? OR (name = ? AND (owner_id = ? OR ? = '')));`
	res, err := q.db.ExecContext(ctx, query, idOrName, idOrName, ownerID, ownerID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ===== Templates =====

func (q *Queries) CreateTemplate(ctx context.Context, tpl *Template) error {
	query := `
		INSERT INTO templates (id, owner_id, name, description, spec_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?);
	`
	_, err := q.db.ExecContext(ctx, query,
		tpl.ID, tpl.OwnerID, tpl.Name, tpl.Description, tpl.SpecJSON, tpl.CreatedAt, tpl.UpdatedAt,
	)
	return err
}

func (q *Queries) GetTemplate(ctx context.Context, ownerID, idOrName string) (*Template, error) {
	query := `
		SELECT id, owner_id, name, description, spec_json, created_at, updated_at
		FROM templates
		WHERE (id = ? OR (name = ? AND (owner_id = ? OR owner_id IS NULL OR ? = '')))
		ORDER BY CASE WHEN id = ? THEN 1 WHEN name = ? THEN 2 ELSE 3 END
		LIMIT 1;
	`
	row := q.db.QueryRowContext(ctx, query, idOrName, idOrName, ownerID, ownerID, idOrName, idOrName)
	var tpl Template
	var owner sql.NullString
	err := row.Scan(&tpl.ID, &owner, &tpl.Name, &tpl.Description, &tpl.SpecJSON, &tpl.CreatedAt, &tpl.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if owner.Valid {
		tpl.OwnerID = &owner.String
	}
	return &tpl, nil
}

func (q *Queries) ListTemplates(ctx context.Context, ownerID string) ([]*Template, error) {
	query := `
		SELECT id, owner_id, name, description, spec_json, created_at, updated_at
		FROM templates
		WHERE (? = '' OR owner_id = ? OR owner_id IS NULL)
		ORDER BY created_at DESC;
	`
	rows, err := q.db.QueryContext(ctx, query, ownerID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []*Template
	for rows.Next() {
		var tpl Template
		var owner sql.NullString
		err := rows.Scan(&tpl.ID, &owner, &tpl.Name, &tpl.Description, &tpl.SpecJSON, &tpl.CreatedAt, &tpl.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if owner.Valid {
			tpl.OwnerID = &owner.String
		}
		templates = append(templates, &tpl)
	}
	return templates, rows.Err()
}

// ===== Images =====

func (q *Queries) CreateImage(ctx context.Context, img *Image) error {
	query := `
		INSERT INTO images (
			id, owner_id, kind, name, os_family, arch, kernel_id,
			rootfs_sha256, oci_digest, oci_config_json, size_bytes,
			min_disk_mb, status, description, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err := q.db.ExecContext(ctx, query,
		img.ID, img.OwnerID, img.Kind, img.Name, img.OSFamily, img.Arch,
		img.KernelID, img.RootfsSHA256, img.OCIDigest, img.OCIConfigJSON,
		img.SizeBytes, img.MinDiskMB, img.Status, img.Description, img.CreatedAt,
	)
	return err
}

func (q *Queries) GetImage(ctx context.Context, ownerID, idOrName string) (*Image, error) {
	query := `
		SELECT id, owner_id, kind, name, os_family, arch, kernel_id,
		       rootfs_sha256, oci_digest, oci_config_json, size_bytes,
		       min_disk_mb, status, description, created_at
		FROM images
		WHERE (id = ? OR (name = ? AND (owner_id = ? OR owner_id IS NULL OR ? = '')))
		ORDER BY CASE WHEN id = ? THEN 1 WHEN name = ? THEN 2 ELSE 3 END
		LIMIT 1;
	`
	row := q.db.QueryRowContext(ctx, query, idOrName, idOrName, ownerID, ownerID, idOrName, idOrName)
	var img Image
	var owner, osFamily, kernelID, rootfsSHA256, ociDigest, ociConfigJSON sql.NullString

	err := row.Scan(
		&img.ID, &owner, &img.Kind, &img.Name, &osFamily, &img.Arch,
		&kernelID, &rootfsSHA256, &ociDigest, &ociConfigJSON, &img.SizeBytes,
		&img.MinDiskMB, &img.Status, &img.Description, &img.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if owner.Valid {
		img.OwnerID = &owner.String
	}
	if osFamily.Valid {
		img.OSFamily = &osFamily.String
	}
	if kernelID.Valid {
		img.KernelID = &kernelID.String
	}
	if rootfsSHA256.Valid {
		img.RootfsSHA256 = &rootfsSHA256.String
	}
	if ociDigest.Valid {
		img.OCIDigest = &ociDigest.String
	}
	if ociConfigJSON.Valid {
		img.OCIConfigJSON = &ociConfigJSON.String
	}
	return &img, nil
}

// ===== API Keys by Name or ID =====

func (q *Queries) GetAPIKeyByNameOrID(ctx context.Context, userID, idOrName string) (*APIKey, error) {
	query := `
		SELECT id, user_id, name, secret_hash, prefix, scopes, expires_at, last_used_at, last_used_ip, created_at, revoked_at
		FROM api_keys
		WHERE (id = ? OR (name = ? AND user_id = ?))
		ORDER BY CASE WHEN id = ? THEN 1 WHEN name = ? THEN 2 ELSE 3 END
		LIMIT 1;
	`
	row := q.db.QueryRowContext(ctx, query, idOrName, idOrName, userID, idOrName, idOrName)
	var k APIKey
	var expiresAt, lastUsedAt, revokedAt sql.NullInt64
	var lastUsedIP sql.NullString
	err := row.Scan(
		&k.ID, &k.UserID, &k.Name, &k.SecretHash, &k.Prefix, &k.Scopes,
		&expiresAt, &lastUsedAt, &lastUsedIP, &k.CreatedAt, &revokedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.Int64
	}
	if lastUsedAt.Valid {
		k.LastUsedAt = &lastUsedAt.Int64
	}
	if lastUsedIP.Valid {
		k.LastUsedIP = &lastUsedIP.String
	}
	if revokedAt.Valid {
		k.RevokedAt = &revokedAt.Int64
	}
	return &k, nil
}
