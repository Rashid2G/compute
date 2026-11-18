package oci

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RootfsBuilder creates bootable ext4 disks from root filesystem trees.
type RootfsBuilder struct {
	agentBinPath string
}

func NewRootfsBuilder(agentBinPath string) *RootfsBuilder {
	return &RootfsBuilder{
		agentBinPath: agentBinPath,
	}
}

// BuildExt4 creates an ext4 filesystem image populated with files from rootDir,
// injecting compute-agent as /sbin/compute-init.
func (b *RootfsBuilder) BuildExt4(ctx context.Context, rootDir, outputPath string, sizeMB int) error {
	if sizeMB <= 0 {
		sizeMB = 1024
	}

	// Ensure required mount points exist in rootDir
	dirs := []string{"/proc", "/sys", "/dev", "/dev/pts", "/run", "/tmp", "/etc", "/sbin", "/bin"}
	for _, d := range dirs {
		_ = os.MkdirAll(filepath.Join(rootDir, d), 0755)
	}

	// Inject compute-agent as /sbin/compute-init
	if b.agentBinPath != "" {
		initTarget := filepath.Join(rootDir, "sbin", "compute-init")
		if err := copyExecutable(b.agentBinPath, initTarget); err != nil {
			return fmt.Errorf("failed to inject compute-init: %w", err)
		}
	}

	// Create output dir if needed
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for rootfs: %w", err)
	}

	_ = os.Remove(outputPath)

	// Execute mkfs.ext4 -F -d <rootDir> -L mfroot <outputPath> <sizeMB>M
	mkfsBin, err := lookSystemBinary("mkfs.ext4")
	if err != nil {
		return fmt.Errorf("mkfs.ext4 failed: %w", err)
	}
	sizeArg := fmt.Sprintf("%dM", sizeMB)
	cmd := exec.CommandContext(ctx, mkfsBin, "-F", "-q", "-d", rootDir, "-L", "mfroot", outputPath, sizeArg)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs.ext4 failed (%v): %s", err, string(output))
	}

	return nil
}

func lookSystemBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, candidate := range []string{"/usr/sbin/" + name, "/sbin/" + name, "/usr/local/sbin/" + name} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %q not found in $PATH, /usr/sbin, or /sbin", name)
}

// ExtractTarToDir extracts a tarball to targetDir.
func ExtractTarToDir(tarReader io.Reader, targetDir string) error {
	tr := tar.NewReader(tarReader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		cleanPath := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanPath, "..") || strings.HasPrefix(cleanPath, "/") {
			continue // Prevent directory traversal
		}

		target := filepath.Join(targetDir, cleanPath)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0755)
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		}
	}
	return nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
