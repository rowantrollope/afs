// Package filehistory stores immutable per-file history independently of live
// inode and checkpoint content. Every key shares the workspace's Redis hash tag.
package filehistory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	ModeOff   = "off"
	ModeAll   = "all"
	ModePaths = "paths"
)

// Policy controls capture and retention. Zero limits are unlimited. Capture is
// disabled by default and exclusions always win over inclusions.
type Policy struct {
	Mode         string   `json:"mode"`
	Include      []string `json:"include,omitempty"`
	Exclude      []string `json:"exclude,omitempty"`
	MaxVersions  int      `json:"max_versions,omitempty"`
	MaxAgeDays   int      `json:"max_age_days,omitempty"`
	MaxBytes     int64    `json:"max_bytes,omitempty"`
	MaxFileBytes int64    `json:"max_file_bytes,omitempty"`
}

func Prefix(id string) string { return "afs:{" + id + "}:history:" }

// NormalizePath returns an absolute workspace-relative path. Parent traversal
// is rejected even when path cleaning could keep it inside the workspace.
func NormalizePath(value string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("history path contains NUL")
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", fmt.Errorf("history path must not contain '..'")
		}
	}
	value = path.Clean("/" + value)
	if value == "/" {
		return "", fmt.Errorf("history path must name a file")
	}
	return value, nil
}

func (p Policy) Validate() error {
	if p.Mode != "" && p.Mode != ModeOff && p.Mode != ModeAll && p.Mode != ModePaths {
		return fmt.Errorf("versioning mode must be off, all, or paths")
	}
	if p.Mode == ModePaths && len(p.Include) == 0 {
		return fmt.Errorf("paths mode requires at least one include pattern")
	}
	if p.MaxVersions < 0 || p.MaxAgeDays < 0 || p.MaxBytes < 0 || p.MaxFileBytes < 0 {
		return fmt.Errorf("versioning limits must be nonnegative (zero means unlimited)")
	}
	if len(p.Include)+len(p.Exclude) > 128 {
		return fmt.Errorf("versioning supports at most 128 include and exclude patterns")
	}
	for _, patterns := range [][]string{p.Include, p.Exclude} {
		for _, pattern := range patterns {
			if _, err := compileGlob(pattern); err != nil {
				return err
			}
		}
	}
	return nil
}

func compileGlob(pattern string) (*historyGlob, error) {
	if strings.TrimSpace(pattern) == "" || len(pattern) > 1024 || strings.ContainsAny(pattern, "\x00") {
		return nil, fmt.Errorf("invalid history pattern %q", pattern)
	}
	for _, component := range strings.Split(pattern, "/") {
		if component == ".." {
			return nil, fmt.Errorf("history pattern must not contain '..'")
		}
	}
	segments := strings.Split(strings.Trim(strings.TrimSpace(pattern), "/"), "/")
	for _, segment := range segments {
		if _, err := path.Match(segment, ""); err != nil {
			return nil, fmt.Errorf("invalid history pattern %q: %w", pattern, err)
		}
	}
	return &historyGlob{segments: segments}, nil
}

// Match the original path.Match segment grammar, including classes and escapes.
// Only an entire ** segment crosses directories. Iterative state propagation
// avoids the original recursive backtracking for repeated ** segments.
type historyGlob struct{ segments []string }

func (g *historyGlob) MatchString(value string) bool {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		return false
	}
	parts := strings.Split(value, "/")
	positions := make([]bool, len(parts)+1)
	positions[0] = true
	for _, segment := range g.segments {
		next := make([]bool, len(parts)+1)
		if segment == "**" {
			active := false
			for j := range positions {
				active = active || positions[j]
				next[j] = active
			}
		} else {
			for j := 0; j < len(parts); j++ {
				if positions[j] {
					next[j+1], _ = path.Match(segment, parts[j])
				}
			}
		}
		positions = next
	}
	return positions[len(parts)]
}

// Normalize preserves the original policy's defaults and ordered glob deduping.
func (p Policy) Normalize() Policy {
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = ModeOff
	}
	normalize := func(values []string) []string {
		var out []string
		seen := map[string]bool{}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" && !seen[value] {
				out = append(out, value)
				seen[value] = true
			}
		}
		return out
	}
	p.Include, p.Exclude = normalize(p.Include), normalize(p.Exclude)
	return p
}

// Matches reports whether a normalized path is selected for capture.
func (p Policy) Matches(name string) bool {
	if p.Mode == ModeOff || p.Mode == "" {
		return false
	}
	name, err := NormalizePath(name)
	if err != nil {
		return false
	}
	match := func(patterns []string) bool {
		for _, pattern := range patterns {
			re, err := compileGlob(pattern)
			if err == nil && re.MatchString(name) {
				return true
			}
		}
		return false
	}
	return !match(p.Exclude) && (p.Mode == ModeAll || p.Mode == ModePaths && match(p.Include))
}

func GetPolicy(ctx context.Context, rdb *redis.Client, id string) (Policy, error) {
	p := Policy{Mode: ModeOff}
	data, err := rdb.Get(ctx, Prefix(id)+"policy").Bytes()
	if errors.Is(err, redis.Nil) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("decode history policy: %w", err)
	}
	if p.Mode == "" {
		p.Mode = ModeOff
	}
	return p, p.Validate()
}

// SetPolicy atomically replaces the workspace policy. All writers must run a
// history-aware version before capture is enabled; older writers cannot honor it.
func SetPolicy(ctx context.Context, rdb *redis.Client, id string, p Policy) error {
	p = p.Normalize()
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Mode == "" {
		p.Mode = ModeOff
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return setPolicyScript.Run(ctx, rdb, []string{
		Prefix(id) + "policy", Prefix(id) + "prune_cursor",
		policyGenerationKey(id), policyImportLockKey(id),
	}, data).Err()
}
