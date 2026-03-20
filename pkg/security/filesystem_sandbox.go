package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AccessMode defines the type of file system access being requested.
type AccessMode string

const (
	AccessRead    AccessMode = "READ"
	AccessWrite   AccessMode = "WRITE"
	AccessExecute AccessMode = "EXECUTE"
	AccessDelete  AccessMode = "DELETE"
)

// SandboxVerdict is the result of a file system access check.
type SandboxVerdict struct {
	// Allowed indicates whether the access should be permitted.
	Allowed bool
	// ResolvedPath is the canonical absolute path after resolution.
	ResolvedPath string
	// Reason is a human-readable explanation of the decision.
	Reason string
	// ThreatLevel is the severity if access is denied.
	ThreatLevel ThreatLevel
}

// SandboxConfig holds the configuration for the FilesystemSandbox.
type SandboxConfig struct {
	// WorkspaceDir is the root directory the agent is allowed to operate in.
	WorkspaceDir string
	// AllowedReadPaths are additional absolute paths the agent can read from.
	AllowedReadPaths []string
	// AllowedWritePaths are additional absolute paths the agent can write to.
	AllowedWritePaths []string
	// BlockedPaths are absolute paths that are always denied, regardless of workspace.
	BlockedPaths []string
	// MaxFileSizeBytes is the maximum file size the agent can read (0 = no limit).
	MaxFileSizeBytes int64
	// AllowSymlinks controls whether symlinks are followed.
	AllowSymlinks bool
}

// DefaultSandboxConfig returns a secure default sandbox configuration.
func DefaultSandboxConfig(workspaceDir string) SandboxConfig {
	return SandboxConfig{
		WorkspaceDir: workspaceDir,
		AllowedReadPaths: []string{
			"/tmp",
			"/var/tmp",
		},
		AllowedWritePaths: []string{
			"/tmp",
			"/var/tmp",
		},
		BlockedPaths: []string{
			// System credentials and secrets
			"/etc/passwd", "/etc/shadow", "/etc/sudoers", "/etc/sudoers.d",
			"/etc/ssh", "/etc/ssl/private",
			// User secrets
			os.ExpandEnv("$HOME/.ssh"),
			os.ExpandEnv("$HOME/.aws"),
			os.ExpandEnv("$HOME/.config/gcloud"),
			os.ExpandEnv("$HOME/.kube"),
			os.ExpandEnv("$HOME/.gnupg"),
			// System directories
			"/proc", "/sys", "/dev",
			"/boot", "/sbin", "/usr/sbin",
			// Root home
			"/root",
		},
		MaxFileSizeBytes: 10 * 1024 * 1024, // 10MB
		AllowSymlinks:    false,
	}
}

// FilesystemSandbox enforces strict file system access controls for AI agents.
// It prevents path traversal, access to sensitive system files, and symlink attacks.
type FilesystemSandbox struct {
	config SandboxConfig
	log    *AuditLog
}

// NewFilesystemSandbox creates a new FilesystemSandbox.
func NewFilesystemSandbox(cfg SandboxConfig, log *AuditLog) (*FilesystemSandbox, error) {
	// Resolve workspace to absolute path
	abs, err := filepath.Abs(cfg.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace: %w", err)
	}
	cfg.WorkspaceDir = abs

	return &FilesystemSandbox{config: cfg, log: log}, nil
}

// CheckAccess validates whether the agent is allowed to access a path.
// It resolves symlinks, checks for path traversal, and validates against
// the whitelist/blacklist.
func (s *FilesystemSandbox) CheckAccess(requestedPath string, mode AccessMode) SandboxVerdict {
	// Step 1: Resolve to absolute path
	absPath, err := filepath.Abs(filepath.Join(s.config.WorkspaceDir, requestedPath))
	if err != nil {
		// If the path is already absolute, use it directly
		absPath, err = filepath.Abs(requestedPath)
		if err != nil {
			return s.deny(requestedPath, ThreatHigh, "Failed to resolve path: "+err.Error())
		}
	}

	// Step 2: Check against hard-blocked paths (always denied)
	for _, blocked := range s.config.BlockedPaths {
		if blocked == "" {
			continue
		}
		blockedAbs, _ := filepath.Abs(blocked)
		if absPath == blockedAbs || strings.HasPrefix(absPath, blockedAbs+string(filepath.Separator)) {
			s.logDenied(absPath, mode, "Blocked path: "+blocked)
			return s.deny(absPath, ThreatCritical, "Access denied: path is in the blocked list ("+blocked+")")
		}
	}

	// Step 3: Resolve symlinks to prevent symlink attacks
	if !s.config.AllowSymlinks {
		realPath, err := filepath.EvalSymlinks(absPath)
		if err == nil && realPath != absPath {
			// The path is a symlink — check where it points
			if !s.isWithinAllowedPaths(realPath, mode) {
				s.logDenied(absPath, mode, "Symlink escapes sandbox: "+realPath)
				return s.deny(absPath, ThreatHigh, "Access denied: symlink resolves outside sandbox ("+realPath+")")
			}
			absPath = realPath
		}
	}

	// Step 4: Check if path is within allowed paths for this access mode
	if !s.isWithinAllowedPaths(absPath, mode) {
		s.logDenied(absPath, mode, "Outside allowed paths")
		return s.deny(absPath, ThreatHigh, "Access denied: path is outside the allowed sandbox boundaries")
	}

	// Step 5: For read operations, check file size limit
	if mode == AccessRead && s.config.MaxFileSizeBytes > 0 {
		info, err := os.Stat(absPath)
		if err == nil && info.Size() > s.config.MaxFileSizeBytes {
			return s.deny(absPath, ThreatLow,
				fmt.Sprintf("Access denied: file size (%d bytes) exceeds limit (%d bytes)",
					info.Size(), s.config.MaxFileSizeBytes))
		}
	}

	// All checks passed
	s.logAllowed(absPath, mode)
	return SandboxVerdict{
		Allowed:      true,
		ResolvedPath: absPath,
		Reason:       "Access granted",
		ThreatLevel:  ThreatNone,
	}
}

// isWithinAllowedPaths checks if a path is within the allowed paths for the given mode.
func (s *FilesystemSandbox) isWithinAllowedPaths(absPath string, mode AccessMode) bool {
	workspace := s.config.WorkspaceDir

	// Always allow within workspace
	if strings.HasPrefix(absPath, workspace+string(filepath.Separator)) || absPath == workspace {
		return true
	}

	// Check mode-specific allowed paths
	var extraPaths []string
	switch mode {
	case AccessRead:
		extraPaths = s.config.AllowedReadPaths
	case AccessWrite, AccessDelete:
		extraPaths = s.config.AllowedWritePaths
	case AccessExecute:
		// Execute is only allowed within workspace
		return false
	}

	for _, allowed := range extraPaths {
		if allowed == "" {
			continue
		}
		allowedAbs, _ := filepath.Abs(allowed)
		if strings.HasPrefix(absPath, allowedAbs+string(filepath.Separator)) || absPath == allowedAbs {
			return true
		}
	}

	return false
}

func (s *FilesystemSandbox) deny(path string, level ThreatLevel, reason string) SandboxVerdict {
	return SandboxVerdict{
		Allowed:      false,
		ResolvedPath: path,
		Reason:       reason,
		ThreatLevel:  level,
	}
}

func (s *FilesystemSandbox) logDenied(path string, mode AccessMode, reason string) {
	if s.log != nil {
		s.log.Record(AuditEntry{
			Timestamp: time.Now(),
			EventType: EventFilesystemDenied,
			Severity:  SeverityHigh,
			Details:   fmt.Sprintf("DENIED [%s] %s — %s", mode, path, reason),
		})
	}
}

func (s *FilesystemSandbox) logAllowed(path string, mode AccessMode) {
	if s.log != nil {
		s.log.Record(AuditEntry{
			Timestamp: time.Now(),
			EventType: EventFilesystemAllowed,
			Severity:  SeverityInfo,
			Details:   fmt.Sprintf("ALLOWED [%s] %s", mode, path),
		})
	}
}
