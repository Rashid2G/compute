package image

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Bootstrap modes accepted by COMPUTE_IMAGES_MODE.
const (
	// ModeDownload fetches the versioned manifest and verifies every artifact.
	ModeDownload = "download"
	// ModePrebuilt validates images that are already present (for example from
	// a local build or a manually copied release bundle).
	ModePrebuilt = "prebuilt"
	// ModeBuild is ModePrebuilt with messages that point at the build scripts.
	ModeBuild = "build"
	// ModeNone skips image validation. Readiness will report the missing files.
	ModeNone = "none"
)

const (
	stateFileName    = ".installed.json"
	manifestMaxBytes = 1 << 20
)

// Manifest is the versioned image bundle descriptor published alongside the
// kernel and rootfs release assets.
type Manifest struct {
	SchemaVersion int              `json:"schema_version"`
	Version       string           `json:"version"`
	Arch          string           `json:"arch"`
	Kernel        ManifestArtifact `json:"kernel"`
	Rootfs        ManifestArtifact `json:"rootfs"`
}

// ManifestArtifact describes one downloadable file and its checksum.
type ManifestArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// BootstrapOptions controls image installation. Zero values are replaced with
// the daemon defaults.
type BootstrapOptions struct {
	Mode           string
	ManifestURL    string
	ImagesDir      string
	KernelName     string
	RootfsName     string
	SSHKeyPath     string
	HTTPClient     *http.Client
	Logf           func(format string, args ...any)
	CurrentArch    string
	SkipSSHKeyGen  bool
	AllowPlainHTTP bool
}

// ArtifactState records one verified file for idempotency and diagnostics.
type ArtifactState struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size"`
}

// State is written to <images>/.installed.json after a successful bootstrap.
type State struct {
	SchemaVersion int           `json:"schema_version"`
	Mode          string        `json:"mode"`
	Version       string        `json:"version,omitempty"`
	Arch          string        `json:"arch"`
	Kernel        ArtifactState `json:"kernel"`
	Rootfs        ArtifactState `json:"rootfs"`
	SSHKey        string        `json:"ssh_key"`
	InstalledAt   time.Time     `json:"installed_at"`
}

// Result reports what Bootstrap did.
type Result struct {
	State        State
	Downloaded   []string
	GeneratedKey bool
	KeyPath      string
}

func (o *BootstrapOptions) normalize() {
	if o.Mode == "" {
		o.Mode = ModeDownload
	}
	o.Mode = strings.ToLower(strings.TrimSpace(o.Mode))
	if o.ImagesDir == "" {
		o.ImagesDir = "/var/lib/compute/images"
	}
	if o.KernelName == "" {
		o.KernelName = "vmlinux"
	}
	if o.RootfsName == "" {
		o.RootfsName = "ubuntu-24.04.ext4"
	}
	if o.SSHKeyPath == "" {
		o.SSHKeyPath = filepath.Join(o.ImagesDir, "ssh", "id_ed25519")
	}
	if o.CurrentArch == "" {
		o.CurrentArch = runtime.GOARCH
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 0}
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
}

// Bootstrap makes the base images and internal SSH key ready. It is
// idempotent: verified files are reused, missing paths are recreated, and
// downloads are written to a temporary file and atomically renamed only after
// the checksum matches.
func Bootstrap(ctx context.Context, opts BootstrapOptions) (*Result, error) {
	opts.normalize()
	switch opts.Mode {
	case ModeDownload, ModePrebuilt, ModeBuild, ModeNone:
	default:
		return nil, fmt.Errorf("invalid COMPUTE_IMAGES_MODE=%q: expected download, prebuilt, build, or none", opts.Mode)
	}

	if err := os.MkdirAll(opts.ImagesDir, 0755); err != nil {
		return nil, fmt.Errorf("create images dir %s: %w", opts.ImagesDir, err)
	}

	result := &Result{}
	kernelPath := filepath.Join(opts.ImagesDir, opts.KernelName)
	rootfsPath := filepath.Join(opts.ImagesDir, opts.RootfsName)

	var manifest *Manifest
	var err error
	if opts.Mode == ModeDownload {
		manifest, err = fetchManifest(ctx, opts)
		if err != nil {
			return nil, err
		}
		if err := verifyManifest(manifest, opts.CurrentArch); err != nil {
			return nil, err
		}
		opts.Logf("[images] manifest %s (%s): kernel=%s rootfs=%s", manifest.Version, manifest.Arch, opts.KernelName, opts.RootfsName)
	}

	if opts.Mode != ModeNone {
		kernelState, downloaded, err := ensureArtifact(ctx, opts, manifestArtifact(manifest, true), kernelPath, "kernel")
		if err != nil {
			return nil, annotateModeError(opts.Mode, err)
		}
		if downloaded {
			result.Downloaded = append(result.Downloaded, kernelPath)
		}
		rootfsState, downloaded, err := ensureArtifact(ctx, opts, manifestArtifact(manifest, false), rootfsPath, "rootfs")
		if err != nil {
			return nil, annotateModeError(opts.Mode, err)
		}
		if downloaded {
			result.Downloaded = append(result.Downloaded, rootfsPath)
		}
		result.State.Kernel = kernelState
		result.State.Rootfs = rootfsState

		if err := validateKernel(kernelPath); err != nil {
			return nil, fmt.Errorf("kernel validation failed: %w", err)
		}
		if err := validateRootfs(rootfsPath); err != nil {
			return nil, fmt.Errorf("rootfs validation failed: %w", err)
		}
	}

	if !opts.SkipSSHKeyGen {
		generated, err := ensureSSHKey(opts.SSHKeyPath)
		if err != nil {
			return nil, err
		}
		result.GeneratedKey = generated
	}
	result.KeyPath = opts.SSHKeyPath

	result.State.SchemaVersion = 1
	result.State.Mode = opts.Mode
	result.State.Arch = opts.CurrentArch
	result.State.SSHKey = opts.SSHKeyPath
	result.State.InstalledAt = time.Now().UTC()
	if manifest != nil {
		result.State.Version = manifest.Version
	}
	if err := writeState(opts.ImagesDir, result.State); err != nil {
		return nil, err
	}
	opts.Logf("[images] bootstrap complete (mode=%s, key=%s)", opts.Mode, opts.SSHKeyPath)
	return result, nil
}

func manifestArtifact(m *Manifest, kernel bool) *ManifestArtifact {
	if m == nil {
		return nil
	}
	if kernel {
		return &m.Kernel
	}
	return &m.Rootfs
}

func annotateModeError(mode string, err error) error {
	if mode == ModeBuild {
		return fmt.Errorf("%w (COMPUTE_IMAGES_MODE=build expects images installed by 'make build-images' or scripts/build-kernel.sh + scripts/build-rootfs.sh under COMPUTE_HOST_DATA_DIR/images, or set COMPUTE_IMAGES_MODE=download with a published manifest)", err)
	}
	if mode == ModePrebuilt {
		return fmt.Errorf("%w (COMPUTE_IMAGES_MODE=prebuilt expects the kernel/rootfs under COMPUTE_HOST_DATA_DIR/images)", err)
	}
	return err
}

func fetchManifest(ctx context.Context, opts BootstrapOptions) (*Manifest, error) {
	raw := strings.TrimSpace(opts.ManifestURL)
	if raw == "" {
		return nil, errors.New("COMPUTE_IMAGE_MANIFEST_URL is not set: publish a checksummed manifest and set its URL in .env, or use COMPUTE_IMAGES_MODE=build/prebuilt with locally built images")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid COMPUTE_IMAGE_MANIFEST_URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("invalid COMPUTE_IMAGE_MANIFEST_URL scheme %q: expected https", u.Scheme)
	}
	if u.Scheme == "http" && !opts.AllowPlainHTTP && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("COMPUTE_IMAGE_MANIFEST_URL must use https (got %q); set COMPUTE_IMAGE_ALLOW_HTTP=true only for local testing", raw)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := opts.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download image manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image manifest: %s returned %s", raw, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, manifestMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image manifest: %w", err)
	}
	if len(body) > manifestMaxBytes {
		return nil, fmt.Errorf("image manifest exceeds %d bytes", manifestMaxBytes)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse image manifest: %w", err)
	}
	return &m, nil
}

func verifyManifest(m *Manifest, currentArch string) error {
	if m == nil {
		return errors.New("empty image manifest")
	}
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported image manifest schema_version %d (expected 1)", m.SchemaVersion)
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("image manifest is missing version")
	}
	if !archMatches(m.Arch, currentArch) {
		return fmt.Errorf("image manifest arch %q does not match this host (%s)", m.Arch, currentArch)
	}
	for name, a := range map[string]ManifestArtifact{"kernel": m.Kernel, "rootfs": m.Rootfs} {
		if strings.TrimSpace(a.URL) == "" {
			return fmt.Errorf("image manifest %s.url is empty", name)
		}
		if err := validateChecksum(a.SHA256); err != nil {
			return fmt.Errorf("image manifest %s: %w", name, err)
		}
		if a.Size < 0 {
			return fmt.Errorf("image manifest %s.size is negative", name)
		}
	}
	return nil
}

func archMatches(manifestArch, current string) bool {
	normalize := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "x86_64", "amd64", "x64":
			return "amd64"
		case "aarch64", "arm64":
			return "arm64"
		}
		return s
	}
	return normalize(manifestArch) == normalize(current)
}

func validateChecksum(sum string) error {
	sum = strings.TrimSpace(strings.ToLower(sum))
	if len(sum) != sha256.Size*2 {
		return fmt.Errorf("sha256 must be %d hex characters", sha256.Size*2)
	}
	if _, err := hex.DecodeString(sum); err != nil {
		return fmt.Errorf("sha256 is not valid hex: %w", err)
	}
	return nil
}

func ensureArtifact(ctx context.Context, opts BootstrapOptions, artifact *ManifestArtifact, path, kind string) (ArtifactState, bool, error) {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		if artifact == nil || strings.TrimSpace(artifact.SHA256) == "" {
			return ArtifactState{File: filepath.Base(path), Size: info.Size()}, false, nil
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return ArtifactState{}, false, fmt.Errorf("checksum %s: %w", path, err)
		}
		if strings.EqualFold(sum, artifact.SHA256) {
			opts.Logf("[images] %s already present and verified: %s", kind, path)
			return ArtifactState{File: filepath.Base(path), SHA256: sum, Size: info.Size()}, false, nil
		}
		if opts.Mode != ModeDownload {
			return ArtifactState{}, false, fmt.Errorf("%s checksum mismatch for %s: got %s, manifest expects %s", kind, path, sum, artifact.SHA256)
		}
		opts.Logf("[images] %s checksum mismatch, re-downloading: %s", kind, path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ArtifactState{}, false, fmt.Errorf("stat %s: %w", path, err)
	}

	if opts.Mode != ModeDownload {
		return ArtifactState{}, false, fmt.Errorf("%s is missing at %s", kind, path)
	}
	if artifact == nil {
		return ArtifactState{}, false, fmt.Errorf("manifest has no %s artifact", kind)
	}
	if err := downloadVerified(ctx, opts.HTTPClient, artifact, path); err != nil {
		return ArtifactState{}, false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ArtifactState{}, false, err
	}
	return ArtifactState{File: filepath.Base(path), SHA256: strings.ToLower(artifact.SHA256), Size: info.Size()}, true, nil
}

func downloadVerified(ctx context.Context, client *http.Client, artifact *ManifestArtifact, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", artifact.URL, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", artifact.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: server returned %s", artifact.URL, resp.Status)
	}

	tmp := dst + ".part"
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, hasher), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("download %s: %w", artifact.URL, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, closeErr)
	}
	if artifact.Size > 0 && written != artifact.Size {
		_ = os.Remove(tmp)
		return fmt.Errorf("download %s: size mismatch: got %d bytes, manifest expects %d", artifact.URL, written, artifact.Size)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, artifact.SHA256) {
		_ = os.Remove(tmp)
		return fmt.Errorf("download %s: sha256 mismatch: got %s, manifest expects %s", artifact.URL, got, artifact.SHA256)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("install %s: %w", dst, err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validateKernel(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if string(head) != "\x7fELF" {
		return fmt.Errorf("%s is not an ELF kernel image (magic %q)", path, head)
	}
	return nil
}

// ext4SuperblockMagic is the 16-bit little-endian 0xEF53 at offset 1024+56.
func validateRootfs(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(1024+56, io.SeekStart); err != nil {
		return err
	}
	magic := make([]byte, 2)
	if _, err := io.ReadFull(f, magic); err != nil {
		return fmt.Errorf("%s: not an ext4 filesystem: %w", path, err)
	}
	if magic[0] != 0x53 || magic[1] != 0xEF {
		return fmt.Errorf("%s is not an ext4 filesystem image (magic %#x%#x)", path, magic[1], magic[0])
	}
	return nil
}

func ensureSSHKey(path string) (bool, error) {
	if data, err := os.ReadFile(path); err == nil {
		if _, err := ssh.ParsePrivateKey(data); err != nil {
			return false, fmt.Errorf("existing SSH key %s is invalid: %w (move it aside to regenerate)", path, err)
		}
		if _, err := os.Stat(path + ".pub"); errors.Is(err, os.ErrNotExist) {
			signer, err := ssh.ParsePrivateKey(data)
			if err != nil {
				return false, err
			}
			pub := ssh.MarshalAuthorizedKey(signer.PublicKey())
			if err := os.WriteFile(path+".pub", pub, 0644); err != nil {
				return false, err
			}
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read SSH key %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, err
	}
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return false, err
	}
	block, err := ssh.MarshalPrivateKey(privKey, "compute-internal")
	if err != nil {
		return false, err
	}
	privPEM := pem.EncodeToMemory(block)
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, privPEM, 0600); err != nil {
		return false, fmt.Errorf("write SSH key %s: %w", path, err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0644); err != nil {
		return false, fmt.Errorf("write SSH public key: %w", err)
	}
	return true, nil
}

func writeState(dir string, state State) error {
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := filepath.Join(dir, stateFileName+".tmp")
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, stateFileName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// LoadState reads <images>/.installed.json, returning os.ErrNotExist when the
// bootstrap has not run yet.
func LoadState(imagesDir string) (*State, error) {
	data, err := os.ReadFile(filepath.Join(imagesDir, stateFileName))
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", stateFileName, err)
	}
	return &state, nil
}

func isLoopbackHost(host string) bool {
	switch strings.Trim(host, "[]") {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}
