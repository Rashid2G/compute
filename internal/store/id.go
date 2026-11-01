package store

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Prefixes for time-sortable ULIDs
const (
	PrefixUser      = "usr"
	PrefixSession   = "ses"
	PrefixAPIKey    = "key"
	PrefixNode      = "nod"
	PrefixJoinToken = "jtk"
	PrefixImage     = "img"
	PrefixKernel    = "krn"
	PrefixRegistry  = "reg"
	PrefixTemplate  = "tpl"
	PrefixVPC       = "vpc"
	PrefixInstance  = "ins"
	PrefixSecret    = "sec"
	PrefixSSHKey    = "ssh"
	PrefixVolume    = "vol"
	PrefixSnapshot  = "snp"
	PrefixOperation = "op"
	PrefixAudit     = "aud"
)

// NewID generates a time-sortable ULID with the given prefix: <prefix>_<ULID>
func NewID(prefix string) string {
	t := time.Now().UTC()
	entropy := ulid.Monotonic(rand.Reader, 0)
	id := ulid.MustNew(ulid.Timestamp(t), entropy)
	return fmt.Sprintf("%s_%s", prefix, id.String())
}

func NewUserID() string      { return NewID(PrefixUser) }
func NewSessionID() string   { return NewID(PrefixSession) }
func NewAPIKeyID() string    { return NewID(PrefixAPIKey) }
func NewNodeID() string      { return NewID(PrefixNode) }
func NewJoinTokenID() string { return NewID(PrefixJoinToken) }
func NewImageID() string     { return NewID(PrefixImage) }
func NewRegistryID() string  { return NewID(PrefixRegistry) }
func NewTemplateID() string  { return NewID(PrefixTemplate) }
func NewVPCID() string       { return NewID(PrefixVPC) }
func NewInstanceID() string  { return NewID(PrefixInstance) }
func NewSecretID() string    { return NewID(PrefixSecret) }
func NewSSHKeyID() string    { return NewID(PrefixSSHKey) }
func NewVolumeID() string    { return NewID(PrefixVolume) }
func NewSnapshotID() string  { return NewID(PrefixSnapshot) }
func NewOperationID() string { return NewID(PrefixOperation) }
func NewAuditID() string     { return NewID(PrefixAudit) }

// ValidateID verifies if the ID matches the expected prefix and valid ULID structure
func ValidateID(id, expectedPrefix string) bool {
	parts := strings.Split(id, "_")
	if len(parts) != 2 || parts[0] != expectedPrefix {
		return false
	}
	_, err := ulid.Parse(parts[1])
	return err == nil
}
