package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventType classifies the type of security event being logged.
type EventType string

const (
	EventFirewallBlocked    EventType = "FIREWALL_BLOCKED"
	EventFirewallAllowed    EventType = "FIREWALL_ALLOWED"
	EventFilesystemDenied   EventType = "FILESYSTEM_DENIED"
	EventFilesystemAllowed  EventType = "FILESYSTEM_ALLOWED"
	EventRateLimitExceeded  EventType = "RATE_LIMIT_EXCEEDED"
	EventPromptInjection    EventType = "PROMPT_INJECTION"
	EventObfuscationDetected EventType = "OBFUSCATION_DETECTED"
	EventAuthFailure        EventType = "AUTH_FAILURE"
	EventAgentStarted       EventType = "AGENT_STARTED"
	EventAgentStopped       EventType = "AGENT_STOPPED"
	EventCommandExecuted    EventType = "COMMAND_EXECUTED"
	EventCommandBlocked     EventType = "COMMAND_BLOCKED"
)

// Severity classifies the severity of a security event.
type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityLow      Severity = "LOW"
	SeverityMedium   Severity = "MEDIUM"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
)

// AuditEntry represents a single security audit log entry.
type AuditEntry struct {
	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"timestamp"`
	// EventType is the classification of the event.
	EventType EventType `json:"event_type"`
	// Severity is the severity level.
	Severity Severity `json:"severity"`
	// SessionID is the agent session identifier.
	SessionID string `json:"session_id,omitempty"`
	// UserID is the user or agent identifier.
	UserID string `json:"user_id,omitempty"`
	// Details contains the human-readable description.
	Details string `json:"details"`
	// RawInput is the original input that triggered the event (truncated for safety).
	RawInput string `json:"raw_input,omitempty"`
	// ThreatScore is a numeric threat score (0-100).
	ThreatScore int `json:"threat_score,omitempty"`
}

// AuditLogConfig holds the configuration for the AuditLog.
type AuditLogConfig struct {
	// LogDir is the directory where audit logs are written.
	LogDir string
	// MaxEntriesInMemory is the maximum number of entries kept in memory.
	MaxEntriesInMemory int
	// LogToFile enables writing audit entries to disk.
	LogToFile bool
	// LogToStdout enables printing audit entries to stdout.
	LogToStdout bool
	// MinSeverityToLog is the minimum severity level to log.
	MinSeverityToLog Severity
}

// DefaultAuditLogConfig returns a secure default audit log configuration.
func DefaultAuditLogConfig() AuditLogConfig {
	return AuditLogConfig{
		LogDir:             "/tmp/picoclaw-audit",
		MaxEntriesInMemory: 10000,
		LogToFile:          true,
		LogToStdout:        false,
		MinSeverityToLog:   SeverityInfo,
	}
}

// AuditLog is a thread-safe security audit logger.
type AuditLog struct {
	mu      sync.RWMutex
	config  AuditLogConfig
	entries []AuditEntry
	file    *os.File
}

// NewAuditLog creates a new AuditLog with the given configuration.
func NewAuditLog(cfg AuditLogConfig) (*AuditLog, error) {
	log := &AuditLog{
		config:  cfg,
		entries: make([]AuditEntry, 0, cfg.MaxEntriesInMemory),
	}

	if cfg.LogToFile {
		if err := os.MkdirAll(cfg.LogDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create audit log dir: %w", err)
		}
		filename := fmt.Sprintf("audit_%s.jsonl", time.Now().Format("2006-01-02"))
		path := filepath.Join(cfg.LogDir, filename)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("failed to open audit log file: %w", err)
		}
		log.file = f
	}

	return log, nil
}

// Record adds a new entry to the audit log.
func (l *AuditLog) Record(entry AuditEntry) {
	if !l.shouldLog(entry.Severity) {
		return
	}

	// Truncate raw input for safety (avoid logging sensitive data in full)
	if len(entry.RawInput) > 200 {
		entry.RawInput = entry.RawInput[:200] + "...[truncated]"
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Add to in-memory buffer (circular)
	if len(l.entries) >= l.config.MaxEntriesInMemory {
		l.entries = l.entries[1:]
	}
	l.entries = append(l.entries, entry)

	// Write to file
	if l.config.LogToFile && l.file != nil {
		data, _ := json.Marshal(entry)
		_, _ = l.file.Write(append(data, '\n'))
	}

	// Write to stdout
	if l.config.LogToStdout {
		fmt.Printf("[AUDIT] [%s] [%s] %s\n",
			entry.Severity, entry.EventType, entry.Details)
	}
}

// GetEntries returns all in-memory audit entries.
func (l *AuditLog) GetEntries() []AuditEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]AuditEntry, len(l.entries))
	copy(result, l.entries)
	return result
}

// GetEntriesBySeverity returns entries filtered by minimum severity.
func (l *AuditLog) GetEntriesBySeverity(minSeverity Severity) []AuditEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var result []AuditEntry
	for _, e := range l.entries {
		if severityLevel(e.Severity) >= severityLevel(minSeverity) {
			result = append(result, e)
		}
	}
	return result
}

// Close flushes and closes the audit log file.
func (l *AuditLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

func (l *AuditLog) shouldLog(s Severity) bool {
	return severityLevel(s) >= severityLevel(l.config.MinSeverityToLog)
}

func severityLevel(s Severity) int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityLow:
		return 1
	case SeverityMedium:
		return 2
	case SeverityHigh:
		return 3
	case SeverityCritical:
		return 4
	}
	return 0
}
