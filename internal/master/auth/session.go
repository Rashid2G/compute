package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/store"
	"github.com/Rashid2G/on-demand-vms/internal/store/db"
)

var (
	ErrSessionExpired = errors.New("session has expired")
	ErrInvalidSession = errors.New("invalid session token")
)

type SessionManager struct {
	queries *db.Queries
	ttl     time.Duration
}

func NewSessionManager(queries *db.Queries, ttl time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour // 7 days default
	}
	return &SessionManager{
		queries: queries,
		ttl:     ttl,
	}
}

// CreateSession generates a new browser session token and CSRF token.
func (sm *SessionManager) CreateSession(ctx context.Context, userID, ip, userAgent string) (rawToken, csrfToken string, record *db.Session, err error) {
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		return "", "", nil, fmt.Errorf("failed to generate random session token: %w", err)
	}
	rawToken = hex.EncodeToString(tokenBytes)

	csrfBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, csrfBytes); err != nil {
		return "", "", nil, fmt.Errorf("failed to generate random csrf token: %w", err)
	}
	csrfToken = hex.EncodeToString(csrfBytes)

	tokenHash := sha256.Sum256([]byte(rawToken))
	csrfHash := sha256.Sum256([]byte(csrfToken))

	now := time.Now().UTC().UnixMilli()
	expiresAt := now + sm.ttl.Milliseconds()

	sessionRecord := &db.Session{
		ID:         store.NewSessionID(),
		UserID:     userID,
		TokenHash:  tokenHash[:],
		CSRFHash:   csrfHash[:],
		IP:         ip,
		UserAgent:  userAgent,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expiresAt,
	}

	if err := sm.queries.CreateSession(ctx, sessionRecord); err != nil {
		return "", "", nil, fmt.Errorf("failed to persist session: %w", err)
	}

	return rawToken, csrfToken, sessionRecord, nil
}

// RevokeSession invalidates a raw session token, if it exists.
func (sm *SessionManager) RevokeSession(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	tokenHash := sha256.Sum256([]byte(rawToken))
	sessionRecord, err := sm.queries.GetSessionByTokenHash(ctx, tokenHash[:])
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		return err
	}
	return sm.queries.DeleteSession(ctx, sessionRecord.ID)
}

// RevokeSessionByID invalidates a session by its record ID.
func (sm *SessionManager) RevokeSessionByID(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	return sm.queries.DeleteSession(ctx, id)
}

// AuthenticateSession validates rawToken against the stored token_hash.
func (sm *SessionManager) AuthenticateSession(ctx context.Context, rawToken string) (*db.User, *db.Session, error) {
	tokenHash := sha256.Sum256([]byte(rawToken))

	sessionRecord, err := sm.queries.GetSessionByTokenHash(ctx, tokenHash[:])
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, nil, ErrInvalidSession
		}
		return nil, nil, err
	}

	now := time.Now().UTC().UnixMilli()
	if sessionRecord.ExpiresAt <= now {
		_ = sm.queries.DeleteSession(ctx, sessionRecord.ID)
		return nil, nil, ErrSessionExpired
	}

	user, err := sm.queries.GetUserByID(ctx, sessionRecord.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load user for session: %w", err)
	}

	if user.Status != "active" {
		return nil, nil, errors.New("user account is disabled")
	}

	return user, sessionRecord, nil
}
