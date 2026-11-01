package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Rashid2G/on-demand-vms/internal/master/pki"
	"github.com/Rashid2G/on-demand-vms/internal/store"
	"github.com/Rashid2G/on-demand-vms/internal/store/db"
)

var (
	ErrInvalidAPIKeyFormat = errors.New("invalid API key format")
	ErrAPIKeyExpired       = errors.New("API key has expired")
	ErrAPIKeyRevoked       = errors.New("API key has been revoked")
	ErrAPIKeyMismatch      = errors.New("invalid API key secret")
)

// KeyManager handles issuance and verification of machine API keys.
type KeyManager struct {
	queries   *db.Queries
	masterKey *pki.MasterKey
}

func NewKeyManager(queries *db.Queries, masterKey *pki.MasterKey) *KeyManager {
	return &KeyManager{
		queries:   queries,
		masterKey: masterKey,
	}
}

// GenerateAPIKey creates a new API key for a user, returning the full token ONCE.
func (km *KeyManager) GenerateAPIKey(ctx context.Context, userID, name string, expiresAt *int64) (token string, keyRecord *db.APIKey, err error) {
	keyID := store.NewAPIKeyID()
	ulidStr := strings.TrimPrefix(keyID, "key_")

	// 32 random bytes for secret
	secretBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, secretBytes); err != nil {
		return "", nil, fmt.Errorf("failed to generate random secret: %w", err)
	}
	secretStr := base64.RawURLEncoding.EncodeToString(secretBytes)

	token = fmt.Sprintf("mfk_%s_%s", ulidStr, secretStr)
	prefix := token
	if len(prefix) > 12 {
		prefix = token[:12]
	}

	// Hash secret with masterKey pepper
	secretHash := km.masterKey.HMAC([]byte(secretStr))

	record := &db.APIKey{
		ID:         keyID,
		UserID:     userID,
		Name:       name,
		SecretHash: secretHash,
		Prefix:     prefix,
		Scopes:     `["*"]`,
		ExpiresAt:  expiresAt,
		CreatedAt:  time.Now().UTC().UnixMilli(),
	}

	if err := km.queries.CreateAPIKey(ctx, record); err != nil {
		return "", nil, fmt.Errorf("failed to store API key: %w", err)
	}

	return token, record, nil
}

// AuthenticateToken verifies the Bearer API key token and returns the corresponding User and APIKey records.
func (km *KeyManager) AuthenticateToken(ctx context.Context, token string, clientIP string) (*db.User, *db.APIKey, error) {
	parts := strings.SplitN(token, "_", 3)
	if len(parts) != 3 || parts[0] != "mfk" {
		return nil, nil, ErrInvalidAPIKeyFormat
	}

	keyID := "key_" + parts[1]
	secretStr := parts[2]

	keyRecord, err := km.queries.GetAPIKeyByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, nil, ErrInvalidAPIKeyFormat
		}
		return nil, nil, err
	}

	if keyRecord.RevokedAt != nil {
		return nil, nil, ErrAPIKeyRevoked
	}

	now := time.Now().UTC().UnixMilli()
	if keyRecord.ExpiresAt != nil && *keyRecord.ExpiresAt <= now {
		return nil, nil, ErrAPIKeyExpired
	}

	// Constant-time HMAC comparison
	computedHash := km.masterKey.HMAC([]byte(secretStr))
	if subtle.ConstantTimeCompare(keyRecord.SecretHash, computedHash) != 1 {
		return nil, nil, ErrAPIKeyMismatch
	}

	user, err := km.queries.GetUserByID(ctx, keyRecord.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch user for API key: %w", err)
	}

	if user.Status != "active" {
		return nil, nil, errors.New("user account is disabled")
	}

	// Update last used asynchronously
	go func() {
		_ = km.queries.UpdateAPIKeyLastUsed(context.Background(), keyRecord.ID, clientIP)
	}()

	return user, keyRecord, nil
}
