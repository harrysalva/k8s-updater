package versions

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	plutoapi "github.com/fairwindsops/pluto/v5/pkg/api"
)

const (
	plutoVersionsURL = "https://raw.githubusercontent.com/FairwindsOps/pluto/main/versions.yaml"
	cacheDir         = ".cache/upgrade-guardian"
	plutoCacheFile   = "pluto-versions.yaml"
	plutoModule      = "github.com/fairwindsops/pluto/v5"
)

// UpdateResult is the response body for POST /api/v1/versions/update.
type UpdateResult struct {
	Tool      string    `json:"tool"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source"`
	MaxK8s    string    `json:"max_k8s"`
	Message   string    `json:"message"`
}

// cachePath returns the absolute path to the cached versions file.
func cachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, cacheDir, plutoCacheFile), nil
}

// PlutoContent returns the best available Pluto versions.yaml content:
// cached file if present and readable, otherwise the embedded bytes.
func PlutoContent(embedded []byte) []byte {
	path, err := cachePath()
	if err != nil {
		return embedded
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return embedded
	}
	return data
}

// PlutoCacheInfo returns metadata about the cached file (exists, modified time).
func PlutoCacheInfo() (exists bool, modTime time.Time) {
	path, err := cachePath()
	if err != nil {
		return false, time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, time.Time{}
	}
	return true, info.ModTime()
}

// UpdatePluto refreshes the Pluto versions.yaml cache. It tries GitHub first;
// if the network is unavailable it falls back to the bundled copy inside the
// Go module cache (populated by `go get` / `make update-deps`).
func UpdatePluto() (*UpdateResult, error) {
	data, source, err := fetchPlutoVersions()
	if err != nil {
		return nil, err
	}

	path, err := cachePath()
	if err != nil {
		return nil, fmt.Errorf("resolve cache path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, fmt.Errorf("write cache file: %w", err)
	}

	maxK8s := computeMaxK8s(data)
	now := time.Now().UTC()

	return &UpdateResult{
		Tool:      "pluto",
		UpdatedAt: now,
		Source:    source,
		MaxK8s:    maxK8s,
		Message:   fmt.Sprintf("Pluto database updated — covers up to k8s %s", maxK8s),
	}, nil
}

// fetchPlutoVersions tries GitHub, then falls back to the Go module cache.
func fetchPlutoVersions() (data []byte, source string, err error) {
	// 1. Try network.
	client := &http.Client{Timeout: 15 * time.Second}
	resp, netErr := client.Get(plutoVersionsURL)
	if netErr == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			data, err = io.ReadAll(resp.Body)
			if err == nil {
				return data, "github", nil
			}
		}
	}

	// 2. Fall back to Go module cache.
	modPath, modErr := plutoModuleCachePath()
	if modErr != nil {
		// Report both errors so the user understands what happened.
		if netErr != nil {
			return nil, "", fmt.Errorf("network unavailable (%v) and module cache not found (%v)", netErr, modErr)
		}
		return nil, "", fmt.Errorf("fetch failed (HTTP %d) and module cache not found (%v)", resp.StatusCode, modErr)
	}
	data, err = os.ReadFile(modPath)
	if err != nil {
		return nil, "", fmt.Errorf("read module cache %s: %w", modPath, err)
	}
	return data, "module-cache", nil
}

// plutoModuleCachePath returns the path to versions.yaml inside the Go module
// cache for the pluto version that is currently in go.sum.
func plutoModuleCachePath() (string, error) {
	// Use runtime/debug to read the build info — it contains the exact module
	// version that was linked into the binary without needing to shell out.
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", fmt.Errorf("build info not available")
	}

	var plutoVer string
	for _, dep := range bi.Deps {
		if dep.Path == plutoModule {
			plutoVer = dep.Version
			break
		}
	}
	if plutoVer == "" {
		return "", fmt.Errorf("pluto module not found in build info")
	}
	// Strip any replace directives marker (e.g. "v5.24.0 => …").
	plutoVer = strings.Fields(plutoVer)[0]

	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("GOPATH unset and UserHomeDir failed: %w", err)
		}
		gopath = filepath.Join(home, "go")
	}

	candidate := filepath.Join(gopath, "pkg", "mod", plutoModule+"@"+plutoVer, "versions.yaml")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("versions.yaml not found at %s", candidate)
	}
	return candidate, nil
}

// computeMaxK8s parses a versions.yaml byte slice and returns the highest k8s version found.
func computeMaxK8s(data []byte) string {
	versions, _, err := plutoapi.GetDefaultVersionList(data)
	if err != nil || len(versions) == 0 {
		return "unknown"
	}
	max := 0
	for _, v := range versions {
		if m := parseMinor(string(v.RemovedIn)); m > max {
			max = m
		}
		if m := parseMinor(string(v.DeprecatedIn)); m > max {
			max = m
		}
	}
	if max == 0 {
		return "unknown"
	}
	return fmt.Sprintf("v1.%d", max)
}
