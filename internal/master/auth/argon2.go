package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidPasswordHash = errors.New("invalid password hash format")
	ErrPasswordMismatch    = errors.New("password does not match")
)

const (
	argonMemory      uint32 = 64 * 1024 // 64 MiB
	argonIterations  uint32 = 3
	argonParallelism uint8  = 4
	argonSaltLen            = 16
	argonKeyLen      uint32 = 32
)

// HashPassword hashes a raw password using Argon2id with OWASP-recommended parameters.
// Output: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("failed to generate random salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLen)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	phc := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism, b64Salt, b64Hash)

	return phc, nil
}

// VerifyPassword checks if a plaintext password matches the stored Argon2id PHC string.
func VerifyPassword(password, encodedHash string) error {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return ErrInvalidPasswordHash
	}

	if parts[1] != "argon2id" {
		return fmt.Errorf("%w: unsupported algorithm %s", ErrInvalidPasswordHash, parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return fmt.Errorf("%w: incompatible version", ErrInvalidPasswordHash)
	}

	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return fmt.Errorf("%w: malformed parameters", ErrInvalidPasswordHash)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("%w: invalid salt encoding", ErrInvalidPasswordHash)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("%w: invalid hash encoding", ErrInvalidPasswordHash)
	}

	computedHash := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expectedHash)))

	if subtle.ConstantTimeCompare(expectedHash, computedHash) != 1 {
		return ErrPasswordMismatch
	}

	return nil
}
