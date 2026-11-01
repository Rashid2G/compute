package worker

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

type Config struct {
	ControlPlaneURL string `json:"control_plane"`
	WorkerID        string `json:"worker_id,omitempty"`
	Name            string `json:"name"`
	PoolID          string `json:"pool_id,omitempty"`
	Token           string `json:"token,omitempty"`
	StateDir        string `json:"state_dir,omitempty"`
	MaxVCPUs        int    `json:"max_vcpus,omitempty"`
	MaxMemoryMB     int    `json:"max_memory_mb,omitempty"`
	MaxDiskMB       int    `json:"max_disk_mb,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	var c Config
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return c, e
		}
		if strings.EqualFold(strings.TrimPrefix(strings.ToLower(path), "."), "toml") || strings.HasSuffix(strings.ToLower(path), ".toml") {
			parseTOML(b, &c)
		} else if e = json.Unmarshal(b, &c); e != nil {
			return c, e
		}
	}
	env := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	env("COMPUTE_CONTROL_PLANE", &c.ControlPlaneURL)
	env("COMPUTE_WORKER_ID", &c.WorkerID)
	env("COMPUTE_WORKER_NAME", &c.Name)
	env("COMPUTE_POOL_ID", &c.PoolID)
	env("COMPUTE_WORKER_TOKEN", &c.Token)
	env("COMPUTE_WORKER_STATE_DIR", &c.StateDir)
	if c.ControlPlaneURL == "" {
		return c, errors.New("control plane URL is required")
	}
	if c.Name == "" {
		c.Name = "compute-worker"
	}
	return c, nil
}
func parseTOML(b []byte, c *Config) {
	for _, line := range strings.Split(string(b), "\n") {
		p := strings.SplitN(strings.TrimSpace(strings.SplitN(line, "#", 2)[0]), "=", 2)
		if len(p) != 2 {
			continue
		}
		k, v := strings.TrimSpace(p[0]), strings.Trim(strings.TrimSpace(p[1]), "\"'")
		switch k {
		case "control_plane", "control_plane_url":
			c.ControlPlaneURL = v
		case "worker_id":
			c.WorkerID = v
		case "name":
			c.Name = v
		case "pool_id":
			c.PoolID = v
		case "token":
			c.Token = v
		case "state_dir":
			c.StateDir = v
		}
	}
}
