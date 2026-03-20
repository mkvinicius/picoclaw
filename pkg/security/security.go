// Package security provides advanced, multi-layer security for PicoClaw agents.
//
// Inspired by the OpenClaw APEX Tri-Brain architecture, this package replaces
// PicoClaw's simple regex blacklist with a 7-layer semantic analysis engine,
// a filesystem sandbox, a rate limiter, and a cryptographic audit log.
//
// # Architecture
//
// The security stack has three main components:
//
//  1. SemanticFirewall: Analyzes all text input (commands, messages) through
//     7 layers: deobfuscation, prompt injection detection, path traversal,
//     privilege escalation, data exfiltration, destructive commands, and
//     obfuscation detection.
//
//  2. FilesystemSandbox: Enforces strict file system access controls,
//     preventing symlink attacks, path traversal, and access to sensitive
//     system files (e.g., /etc/shadow, ~/.ssh).
//
//  3. RateLimiter: Enforces per-session request and command rate limits
//     using a sliding window algorithm, with automatic temporary and
//     permanent banning for repeat offenders.
//
//  4. AuditLog: Records all security events to a tamper-evident JSONL file
//     with configurable severity filtering.
//
// # Usage
//
//	// Create the security stack
//	stack, err := security.NewSecurityStack(security.DefaultSecurityStackConfig("/workspace"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer stack.Close()
//
//	// Check a command before execution
//	result := stack.CheckCommand("session-123", "ls -la /tmp")
//	if !result.Allowed {
//	    fmt.Println("Blocked:", result.Reason)
//	}
//
//	// Check file access before reading
//	access := stack.CheckFileAccess("session-123", "/workspace/data.csv", security.AccessRead)
//	if !access.Allowed {
//	    fmt.Println("Blocked:", access.Reason)
//	}
package security

import (
	"fmt"
	"time"
)

// SecurityStackConfig holds the configuration for the full security stack.
type SecurityStackConfig struct {
	Firewall  FirewallConfig
	Sandbox   SandboxConfig
	RateLimit RateLimitConfig
	AuditLog  AuditLogConfig
}

// DefaultSecurityStackConfig returns a secure default configuration for the
// full security stack, using the given workspace directory.
func DefaultSecurityStackConfig(workspaceDir string) SecurityStackConfig {
	return SecurityStackConfig{
		Firewall:  DefaultFirewallConfig(),
		Sandbox:   DefaultSandboxConfig(workspaceDir),
		RateLimit: DefaultRateLimitConfig(),
		AuditLog:  DefaultAuditLogConfig(),
	}
}

// CommandCheckResult is the combined result of checking a command through
// the full security stack (firewall + rate limiter).
type CommandCheckResult struct {
	// Allowed indicates whether the command should be permitted.
	Allowed bool
	// Reason is a human-readable explanation of the decision.
	Reason string
	// ThreatLevel is the severity of the detected threat.
	ThreatLevel ThreatLevel
	// Category is the type of threat detected.
	Category ThreatCategory
	// FirewallVerdict is the detailed result from the semantic firewall.
	FirewallVerdict FirewallVerdict
	// RateLimitVerdict is the detailed result from the rate limiter.
	RateLimitVerdict RateLimitVerdict
}

// SecurityStack is the unified security layer for PicoClaw.
// It integrates the SemanticFirewall, FilesystemSandbox, RateLimiter,
// and AuditLog into a single easy-to-use interface.
type SecurityStack struct {
	firewall *SemanticFirewall
	sandbox  *FilesystemSandbox
	limiter  *RateLimiter
	log      *AuditLog
}

// NewSecurityStack creates a new SecurityStack with the given configuration.
func NewSecurityStack(cfg SecurityStackConfig) (*SecurityStack, error) {
	// Create audit log first (other components depend on it)
	auditLog, err := NewAuditLog(cfg.AuditLog)
	if err != nil {
		return nil, fmt.Errorf("failed to create audit log: %w", err)
	}

	// Create filesystem sandbox
	sandbox, err := NewFilesystemSandbox(cfg.Sandbox, auditLog)
	if err != nil {
		auditLog.Close()
		return nil, fmt.Errorf("failed to create filesystem sandbox: %w", err)
	}

	stack := &SecurityStack{
		firewall: NewSemanticFirewall(cfg.Firewall),
		sandbox:  sandbox,
		limiter:  NewRateLimiter(cfg.RateLimit, auditLog),
		log:      auditLog,
	}

	// Log startup
	auditLog.Record(AuditEntry{
		Timestamp: time.Now(),
		EventType: EventAgentStarted,
		Severity:  SeverityInfo,
		Details:   "PicoClaw Security Stack initialized (SemanticFirewall + FilesystemSandbox + RateLimiter + AuditLog)",
	})

	return stack, nil
}

// CheckCommand runs a shell command through the full security stack.
// This should be called before executing any shell command.
func (s *SecurityStack) CheckCommand(sessionID, command string) CommandCheckResult {
	// Step 1: Rate limit check
	rlVerdict := s.limiter.CheckCommand(sessionID)
	if !rlVerdict.Allowed {
		s.log.Record(AuditEntry{
			Timestamp: time.Now(),
			EventType: EventCommandBlocked,
			Severity:  SeverityHigh,
			SessionID: sessionID,
			Details:   "Command blocked by rate limiter: " + rlVerdict.Reason,
			RawInput:  command,
		})
		return CommandCheckResult{
			Allowed:          false,
			Reason:           rlVerdict.Reason,
			ThreatLevel:      ThreatMedium,
			Category:         CategoryNone,
			RateLimitVerdict: rlVerdict,
		}
	}

	// Step 2: Semantic firewall check
	fwVerdict := s.firewall.Analyze(command)
	if !fwVerdict.Allowed {
		s.log.Record(AuditEntry{
			Timestamp:   time.Now(),
			EventType:   EventCommandBlocked,
			Severity:    threatToSeverity(fwVerdict.ThreatLevel),
			SessionID:   sessionID,
			Details:     fmt.Sprintf("Command blocked by semantic firewall [%s]: %s", fwVerdict.Category, fwVerdict.Reason),
			RawInput:    command,
			ThreatScore: int(fwVerdict.ThreatLevel) * 25,
		})
		return CommandCheckResult{
			Allowed:         false,
			Reason:          fwVerdict.Reason,
			ThreatLevel:     fwVerdict.ThreatLevel,
			Category:        fwVerdict.Category,
			FirewallVerdict: fwVerdict,
		}
	}

	// All checks passed
	s.log.Record(AuditEntry{
		Timestamp: time.Now(),
		EventType: EventCommandExecuted,
		Severity:  SeverityInfo,
		SessionID: sessionID,
		Details:   "Command allowed: " + command,
	})

	return CommandCheckResult{
		Allowed:          true,
		Reason:           "Command passed all security checks",
		ThreatLevel:      ThreatNone,
		FirewallVerdict:  fwVerdict,
		RateLimitVerdict: rlVerdict,
	}
}

// CheckMessage runs a user message through the semantic firewall.
// This should be called before passing any user message to the AI model.
func (s *SecurityStack) CheckMessage(sessionID, message string) FirewallVerdict {
	// Rate limit check
	rlVerdict := s.limiter.CheckRequest(sessionID)
	if !rlVerdict.Allowed {
		return FirewallVerdict{
			Allowed:     false,
			ThreatLevel: ThreatMedium,
			Category:    CategoryNone,
			Reason:      rlVerdict.Reason,
		}
	}

	// Semantic firewall (focus on prompt injection for messages)
	return s.firewall.Analyze(message)
}

// CheckFileAccess validates whether a session is allowed to access a file.
func (s *SecurityStack) CheckFileAccess(sessionID, path string, mode AccessMode) SandboxVerdict {
	verdict := s.sandbox.CheckAccess(path, mode)
	if !verdict.Allowed {
		s.log.Record(AuditEntry{
			Timestamp: time.Now(),
			EventType: EventFilesystemDenied,
			Severity:  threatToSeverity(verdict.ThreatLevel),
			SessionID: sessionID,
			Details:   fmt.Sprintf("File access denied [%s] %s: %s", mode, path, verdict.Reason),
		})
	}
	return verdict
}

// GetAuditLog returns the audit log for inspection.
func (s *SecurityStack) GetAuditLog() *AuditLog {
	return s.log
}

// Close flushes and closes all resources.
func (s *SecurityStack) Close() error {
	s.log.Record(AuditEntry{
		Timestamp: time.Now(),
		EventType: EventAgentStopped,
		Severity:  SeverityInfo,
		Details:   "PicoClaw Security Stack shutting down",
	})
	return s.log.Close()
}

// threatToSeverity converts a ThreatLevel to a log Severity.
func threatToSeverity(level ThreatLevel) Severity {
	switch level {
	case ThreatNone:
		return SeverityInfo
	case ThreatLow:
		return SeverityLow
	case ThreatMedium:
		return SeverityMedium
	case ThreatHigh:
		return SeverityHigh
	case ThreatCritical:
		return SeverityCritical
	}
	return SeverityInfo
}
