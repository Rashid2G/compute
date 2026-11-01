package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/store/db"
)

const (
	MaxFailedLogins = 5
	LockoutDuration = 15 * time.Minute
)

// CheckAndRecordLoginAttempt handles failed login tracking and account locking.
func CheckAndRecordLoginAttempt(ctx context.Context, queries *db.Queries, user *db.User, passwordMatch bool) error {
	now := time.Now().UTC().UnixMilli()

	if user.LockedUntil != nil && *user.LockedUntil > now {
		remaining := time.Duration(*user.LockedUntil-now) * time.Millisecond
		return fmt.Errorf("account is temporarily locked due to failed logins; try again in %v", remaining.Round(time.Second))
	}

	if passwordMatch {
		if user.FailedLogins > 0 || user.LockedUntil != nil {
			_ = queries.ResetUserFailedLogins(ctx, user.ID)
		}
		return nil
	}

	newFailures := user.FailedLogins + 1
	var lockedUntil *int64
	if newFailures >= MaxFailedLogins {
		lockTime := now + LockoutDuration.Milliseconds()
		lockedUntil = &lockTime
	}

	_ = queries.UpdateUserFailedLogins(ctx, user.ID, newFailures, lockedUntil)
	if lockedUntil != nil {
		return fmt.Errorf("maximum login attempts exceeded; account locked for %v", LockoutDuration)
	}

	return ErrPasswordMismatch
}

// Authorize checks if caller is permitted to access a resource.
// If role == "admin", access is always granted.
// If role == "user", access is only granted if ownerID == caller.ID.
// If not authorized, returns false.
func Authorize(caller *db.User, ownerID string) bool {
	if caller == nil {
		return false
	}
	if caller.Role == "admin" {
		return true
	}
	return caller.ID == ownerID
}
