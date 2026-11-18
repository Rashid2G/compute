package version

import (
	"encoding/json"
	"fmt"
	"runtime"
)

var (
	// ProjectName is the umbrella project name.
	ProjectName = "Compute"

	// Version is the current semantic version.
	Version = "1.1.0"

	// ReleaseName is the release codename / name.
	ReleaseName = "Compute"

	// GitCommit is set at build time via ldflags.
	GitCommit = "dev"

	// BuildDate is set at build time via ldflags (RFC3339).
	BuildDate = "unknown"
)

// Info contains structured version information for a Compute component.
type Info struct {
	Name        string `json:"name"`                   // e.g. "Compute CLI" or "Compute Daemon"
	Service     string `json:"service"`                // e.g. "compute" or "computed"
	Project     string `json:"project"`                // e.g. "Compute"
	Version     string `json:"version"`                // e.g. "1.0.0"
	ReleaseName string `json:"release_name,omitempty"` // e.g. "Compute"
	GitCommit   string `json:"git_commit,omitempty"`   // git commit sha
	BuildDate   string `json:"build_date,omitempty"`   // RFC3339 build date
	GoVersion   string `json:"go_version"`             // runtime.Version()
	OS          string `json:"os"`                     // runtime.GOOS
	Arch        string `json:"arch"`                   // runtime.GOARCH
}

// Get returns the Info struct populated for the given service and display name.
func Get(service, name string) Info {
	return Info{
		Name:        name,
		Service:     service,
		Project:     ProjectName,
		Version:     Version,
		ReleaseName: ReleaseName,
		GitCommit:   GitCommit,
		BuildDate:   BuildDate,
		GoVersion:   runtime.Version(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
	}
}

// String returns a single-line summary of version and name.
func (i Info) String() string {
	rel := ""
	if i.ReleaseName != "" {
		rel = fmt.Sprintf(" (%s)", i.ReleaseName)
	}
	return fmt.Sprintf("%s (%s) v%s%s [%s/%s %s]", i.Name, i.Service, i.Version, rel, i.OS, i.Arch, i.GoVersion)
}

// Detailed returns formatted multi-line information with an optional line prefix indentation.
func (i Info) Detailed(indent string) string {
	var s string
	s += fmt.Sprintf("%sName:        %s (%s)\n", indent, i.Name, i.Service)
	s += fmt.Sprintf("%sVersion:     %s\n", indent, i.Version)
	if i.ReleaseName != "" {
		s += fmt.Sprintf("%sRelease:     %s\n", indent, i.ReleaseName)
	}
	if i.GitCommit != "" && i.GitCommit != "none" && i.GitCommit != "dev" {
		s += fmt.Sprintf("%sGit Commit:  %s\n", indent, i.GitCommit)
	}
	if i.BuildDate != "" && i.BuildDate != "unknown" {
		s += fmt.Sprintf("%sBuild Date:  %s\n", indent, i.BuildDate)
	}
	s += fmt.Sprintf("%sGo Version:  %s\n", indent, i.GoVersion)
	s += fmt.Sprintf("%sOS/Arch:     %s/%s", indent, i.OS, i.Arch)
	return s
}

// JSON returns a JSON formatted string.
func (i Info) JSON() (string, error) {
	data, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
