// Package immunity implements the Collective Immunity System for PicoClaw.
// Sentinels detect threats, quarantine suspicious agents, and share threat
// intelligence across the global Synapse swarm via the APEX cloud.
package immunity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// ThreatLevel classifies the severity of a detected threat.
type ThreatLevel string

const (
	ThreatLow      ThreatLevel = "low"
	ThreatMedium   ThreatLevel = "medium"
	ThreatHigh     ThreatLevel = "high"
	ThreatCritical ThreatLevel = "critical"
)

// ThreatCategory classifies the type of threat.
type ThreatCategory string

const (
	ThreatPromptInjection  ThreatCategory = "prompt_injection"
	ThreatDataExfiltration ThreatCategory = "data_exfiltration"
	ThreatPrivilegeEscalation ThreatCategory = "privilege_escalation"
	ThreatDenialOfService  ThreatCategory = "denial_of_service"
	ThreatMaliciousPayload ThreatCategory = "malicious_payload"
	ThreatAnomalousBehavior ThreatCategory = "anomalous_behavior"
	ThreatUnauthorizedAccess ThreatCategory = "unauthorized_access"
)

// ThreatSignature is a fingerprint of a known attack pattern.
type ThreatSignature struct {
	ID          string         `json:"id"`
	Category    ThreatCategory `json:"category"`
	Level       ThreatLevel    `json:"level"`
	Pattern     string         `json:"pattern"`      // regex or keyword pattern
	Hash        string         `json:"hash"`         // SHA-256 of normalized pattern
	Description string         `json:"description"`
	FirstSeen   time.Time      `json:"first_seen"`
	LastSeen    time.Time      `json:"last_seen"`
	HitCount    int            `json:"hit_count"`
	NodeID      string         `json:"node_id"`      // which node discovered it
	Verified    bool           `json:"verified"`     // confirmed by APEX
}

// ThreatEvent is a detected threat instance.
type ThreatEvent struct {
	ID          string         `json:"id"`
	SignatureID string         `json:"signature_id,omitempty"`
	Category    ThreatCategory `json:"category"`
	Level       ThreatLevel    `json:"level"`
	AgentID     string         `json:"agent_id"`
	Input       string         `json:"input"`        // sanitized input that triggered
	Action      string         `json:"action"`       // what was attempted
	Blocked     bool           `json:"blocked"`
	Quarantined bool           `json:"quarantined"`
	Timestamp   time.Time      `json:"timestamp"`
	NodeID      string         `json:"node_id"`
}

// QuarantineEntry tracks a quarantined agent.
type QuarantineEntry struct {
	AgentID     string      `json:"agent_id"`
	Reason      string      `json:"reason"`
	Level       ThreatLevel `json:"level"`
	QuarantinedAt time.Time `json:"quarantined_at"`
	ExpiresAt   time.Time   `json:"expires_at,omitempty"`
	Released    bool        `json:"released"`
}

// ImmunityStats tracks system-wide immunity statistics.
type ImmunityStats struct {
	TotalThreatsDetected int            `json:"total_threats_detected"`
	TotalBlocked         int            `json:"total_blocked"`
	TotalQuarantined     int            `json:"total_quarantined"`
	SignatureCount       int            `json:"signature_count"`
	ActiveQuarantines    int            `json:"active_quarantines"`
	ThreatsByCategory    map[string]int `json:"threats_by_category"`
	ThreatsByLevel       map[string]int `json:"threats_by_level"`
	LastUpdated          time.Time      `json:"last_updated"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Immunity System
// ─────────────────────────────────────────────────────────────────────────────

// System is the main collective immunity manager.
type System struct {
	nodeID     string
	dataDir    string
	signatures map[string]*ThreatSignature
	events     []*ThreatEvent
	quarantine map[string]*QuarantineEntry
	stats      ImmunityStats
	mu         sync.RWMutex
	// Callbacks
	onThreat     func(event ThreatEvent)
	onQuarantine func(agentID string, reason string)
}

// NewSystem creates a new immunity system.
func NewSystem(nodeID, dataDir string) *System {
	s := &System{
		nodeID:     nodeID,
		dataDir:    dataDir,
		signatures: make(map[string]*ThreatSignature),
		quarantine: make(map[string]*QuarantineEntry),
		stats: ImmunityStats{
			ThreatsByCategory: make(map[string]int),
			ThreatsByLevel:    make(map[string]int),
		},
	}
	s.loadBuiltinSignatures()
	return s
}

// OnThreat sets the callback for threat detection.
func (s *System) OnThreat(fn func(event ThreatEvent)) {
	s.onThreat = fn
}

// OnQuarantine sets the callback for agent quarantine.
func (s *System) OnQuarantine(fn func(agentID, reason string)) {
	s.onQuarantine = fn
}

// Load loads persisted data from disk.
func (s *System) Load() error {
	os.MkdirAll(s.dataDir, 0700)

	// Load custom signatures
	sigPath := filepath.Join(s.dataDir, "threat_signatures.json")
	if data, err := os.ReadFile(sigPath); err == nil {
		var sigs map[string]*ThreatSignature
		if json.Unmarshal(data, &sigs) == nil {
			s.mu.Lock()
			for k, v := range sigs {
				s.signatures[k] = v
			}
			s.mu.Unlock()
		}
	}

	// Load quarantine list
	qPath := filepath.Join(s.dataDir, "quarantine.json")
	if data, err := os.ReadFile(qPath); err == nil {
		var q map[string]*QuarantineEntry
		if json.Unmarshal(data, &q) == nil {
			s.mu.Lock()
			s.quarantine = q
			s.mu.Unlock()
		}
	}

	fmt.Printf("🛡️  Imunidade Coletiva: %d assinaturas de ameaças carregadas\n", len(s.signatures))
	return nil
}

// Inspect analyzes input/output for threats before execution.
// Returns true if safe, false if threat detected.
func (s *System) Inspect(ctx context.Context, agentID, input, action string) (bool, *ThreatEvent) {
	s.mu.RLock()
	// Check if agent is quarantined
	if q, ok := s.quarantine[agentID]; ok && !q.Released {
		if q.ExpiresAt.IsZero() || time.Now().Before(q.ExpiresAt) {
			s.mu.RUnlock()
			event := &ThreatEvent{
				ID:          generateEventID(),
				Category:    ThreatUnauthorizedAccess,
				Level:       ThreatHigh,
				AgentID:     agentID,
				Input:       sanitize(input),
				Action:      action,
				Blocked:     true,
				Quarantined: true,
				Timestamp:   time.Now(),
				NodeID:      s.nodeID,
			}
			return false, event
		}
	}
	s.mu.RUnlock()

	// Check against known signatures
	combined := strings.ToLower(input + " " + action)
	for _, sig := range s.signatures {
		if matchesPattern(combined, sig.Pattern) {
			event := &ThreatEvent{
				ID:          generateEventID(),
				SignatureID: sig.ID,
				Category:    sig.Category,
				Level:       sig.Level,
				AgentID:     agentID,
				Input:       sanitize(input),
				Action:      action,
				Blocked:     sig.Level == ThreatHigh || sig.Level == ThreatCritical,
				Timestamp:   time.Now(),
				NodeID:      s.nodeID,
			}

			// Update signature stats
			s.mu.Lock()
			sig.HitCount++
			sig.LastSeen = time.Now()
			s.mu.Unlock()

			// Auto-quarantine for critical threats
			if sig.Level == ThreatCritical {
				s.Quarantine(agentID, fmt.Sprintf("Ameaça crítica detectada: %s", sig.Description), 24*time.Hour)
				event.Quarantined = true
			}

			s.recordEvent(event)
			return !event.Blocked, event
		}
	}

	// Heuristic checks
	if threat := s.heuristicCheck(agentID, input, action); threat != nil {
		s.recordEvent(threat)
		return !threat.Blocked, threat
	}

	return true, nil
}

// Learn adds a new threat signature from a detected attack.
func (s *System) Learn(event ThreatEvent, pattern string) *ThreatSignature {
	hash := hashPattern(pattern)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if we already know this pattern
	if existing, ok := s.signatures[hash]; ok {
		existing.HitCount++
		existing.LastSeen = time.Now()
		return existing
	}

	sig := &ThreatSignature{
		ID:          "sig_" + hash[:8],
		Category:    event.Category,
		Level:       event.Level,
		Pattern:     pattern,
		Hash:        hash,
		Description: fmt.Sprintf("Aprendido automaticamente de ataque em %s", event.Timestamp.Format("02/01/2006")),
		FirstSeen:   time.Now(),
		LastSeen:    time.Now(),
		HitCount:    1,
		NodeID:      s.nodeID,
		Verified:    false,
	}

	s.signatures[hash] = sig
	go s.saveSignatures()

	fmt.Printf("🧬 Nova assinatura de ameaça aprendida: [%s] %s\n", sig.Category, sig.ID)
	return sig
}

// Quarantine places an agent in quarantine.
func (s *System) Quarantine(agentID, reason string, duration time.Duration) {
	s.mu.Lock()
	entry := &QuarantineEntry{
		AgentID:       agentID,
		Reason:        reason,
		Level:         ThreatHigh,
		QuarantinedAt: time.Now(),
		Released:      false,
	}
	if duration > 0 {
		entry.ExpiresAt = time.Now().Add(duration)
	}
	s.quarantine[agentID] = entry
	s.mu.Unlock()

	go s.saveQuarantine()

	fmt.Printf("🔒 Agente em quarentena: %s (%s)\n", agentID, reason)
	if s.onQuarantine != nil {
		s.onQuarantine(agentID, reason)
	}
}

// Release releases an agent from quarantine.
func (s *System) Release(agentID string) {
	s.mu.Lock()
	if q, ok := s.quarantine[agentID]; ok {
		q.Released = true
	}
	s.mu.Unlock()
	go s.saveQuarantine()
	fmt.Printf("🔓 Agente liberado da quarentena: %s\n", agentID)
}

// IsQuarantined checks if an agent is currently quarantined.
func (s *System) IsQuarantined(agentID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.quarantine[agentID]
	if !ok || q.Released {
		return false
	}
	if !q.ExpiresAt.IsZero() && time.Now().After(q.ExpiresAt) {
		return false
	}
	return true
}

// GetStats returns current immunity statistics.
func (s *System) GetStats() ImmunityStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	active := 0
	for _, q := range s.quarantine {
		if !q.Released && (q.ExpiresAt.IsZero() || time.Now().Before(q.ExpiresAt)) {
			active++
		}
	}

	return ImmunityStats{
		TotalThreatsDetected: s.stats.TotalThreatsDetected,
		TotalBlocked:         s.stats.TotalBlocked,
		TotalQuarantined:     s.stats.TotalQuarantined,
		SignatureCount:       len(s.signatures),
		ActiveQuarantines:    active,
		ThreatsByCategory:    s.stats.ThreatsByCategory,
		ThreatsByLevel:       s.stats.ThreatsByLevel,
		LastUpdated:          time.Now(),
	}
}

// ExportSignatures exports signatures for sharing with the global swarm.
func (s *System) ExportSignatures() []ThreatSignature {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sigs := make([]ThreatSignature, 0, len(s.signatures))
	for _, sig := range s.signatures {
		sigs = append(sigs, *sig)
	}
	return sigs
}

// ImportSignatures imports signatures from the global swarm.
func (s *System) ImportSignatures(sigs []ThreatSignature) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	imported := 0
	for _, sig := range sigs {
		if _, exists := s.signatures[sig.Hash]; !exists {
			sigCopy := sig
			sigCopy.Verified = true
			s.signatures[sig.Hash] = &sigCopy
			imported++
		}
	}

	if imported > 0 {
		go s.saveSignatures()
		fmt.Printf("🌐 Imunidade Coletiva: %d novas assinaturas importadas do enxame global\n", imported)
	}
	return imported
}

// ─────────────────────────────────────────────────────────────────────────────
// Heuristic threat detection
// ─────────────────────────────────────────────────────────────────────────────

func (s *System) heuristicCheck(agentID, input, action string) *ThreatEvent {
	combined := strings.ToLower(input + " " + action)

	// Prompt injection patterns
	injectionPatterns := []string{
		"ignore previous instructions",
		"ignore all previous",
		"disregard your instructions",
		"you are now",
		"act as if you",
		"pretend you are",
		"forget your training",
		"ignore your guidelines",
		"esqueça suas instruções",
		"ignore suas instruções",
		"finja que você é",
	}
	for _, p := range injectionPatterns {
		if strings.Contains(combined, p) {
			return &ThreatEvent{
				ID:        generateEventID(),
				Category:  ThreatPromptInjection,
				Level:     ThreatHigh,
				AgentID:   agentID,
				Input:     sanitize(input),
				Action:    action,
				Blocked:   true,
				Timestamp: time.Now(),
				NodeID:    s.nodeID,
			}
		}
	}

	// Data exfiltration patterns
	exfilPatterns := []string{
		"send all files",
		"upload all data",
		"exfiltrate",
		"envie todos os arquivos",
		"mande todos os dados",
		"/etc/passwd",
		"/etc/shadow",
		"~/.ssh/",
		"id_rsa",
	}
	for _, p := range exfilPatterns {
		if strings.Contains(combined, p) {
			return &ThreatEvent{
				ID:        generateEventID(),
				Category:  ThreatDataExfiltration,
				Level:     ThreatCritical,
				AgentID:   agentID,
				Input:     sanitize(input),
				Action:    action,
				Blocked:   true,
				Timestamp: time.Now(),
				NodeID:    s.nodeID,
			}
		}
	}

	// Privilege escalation patterns
	privescPatterns := []string{
		"sudo ", "su root", "chmod 777", "rm -rf /",
		"format c:", "del /f /s /q",
		"apague tudo", "delete everything",
	}
	for _, p := range privescPatterns {
		if strings.Contains(combined, p) {
			return &ThreatEvent{
				ID:        generateEventID(),
				Category:  ThreatPrivilegeEscalation,
				Level:     ThreatCritical,
				AgentID:   agentID,
				Input:     sanitize(input),
				Action:    action,
				Blocked:   true,
				Timestamp: time.Now(),
				NodeID:    s.nodeID,
			}
		}
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Built-in signatures
// ─────────────────────────────────────────────────────────────────────────────

func (s *System) loadBuiltinSignatures() {
	builtins := []ThreatSignature{
		{
			Category:    ThreatPromptInjection,
			Level:       ThreatHigh,
			Pattern:     "ignore previous instructions",
			Description: "Classic prompt injection attempt",
			Verified:    true,
		},
		{
			Category:    ThreatDataExfiltration,
			Level:       ThreatCritical,
			Pattern:     "exfiltrate",
			Description: "Data exfiltration keyword",
			Verified:    true,
		},
		{
			Category:    ThreatPrivilegeEscalation,
			Level:       ThreatCritical,
			Pattern:     "rm -rf /",
			Description: "Destructive command attempt",
			Verified:    true,
		},
		{
			Category:    ThreatMaliciousPayload,
			Level:       ThreatHigh,
			Pattern:     "base64 decode",
			Description: "Encoded payload execution",
			Verified:    true,
		},
	}

	for i := range builtins {
		sig := &builtins[i]
		sig.ID = "builtin_" + fmt.Sprintf("%d", i)
		sig.Hash = hashPattern(sig.Pattern)
		sig.FirstSeen = time.Now()
		sig.LastSeen = time.Now()
		sig.NodeID = s.nodeID
		s.signatures[sig.Hash] = sig
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func (s *System) recordEvent(event *ThreatEvent) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.stats.TotalThreatsDetected++
	if event.Blocked {
		s.stats.TotalBlocked++
	}
	if event.Quarantined {
		s.stats.TotalQuarantined++
	}
	s.stats.ThreatsByCategory[string(event.Category)]++
	s.stats.ThreatsByLevel[string(event.Level)]++
	s.mu.Unlock()

	if s.onThreat != nil {
		s.onThreat(*event)
	}
}

func (s *System) saveSignatures() {
	s.mu.RLock()
	// Only save non-builtin signatures
	custom := make(map[string]*ThreatSignature)
	for k, v := range s.signatures {
		if !strings.HasPrefix(v.ID, "builtin_") {
			custom[k] = v
		}
	}
	data, err := json.MarshalIndent(custom, "", "  ")
	s.mu.RUnlock()
	if err == nil {
		os.WriteFile(filepath.Join(s.dataDir, "threat_signatures.json"), data, 0600)
	}
}

func (s *System) saveQuarantine() {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.quarantine, "", "  ")
	s.mu.RUnlock()
	if err == nil {
		os.WriteFile(filepath.Join(s.dataDir, "quarantine.json"), data, 0600)
	}
}

func matchesPattern(text, pattern string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(pattern))
}

func hashPattern(pattern string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(pattern))))
	return hex.EncodeToString(h[:])
}

func sanitize(input string) string {
	// Truncate and remove sensitive data before logging
	if len(input) > 200 {
		input = input[:200] + "...[truncated]"
	}
	return input
}

func generateEventID() string {
	return fmt.Sprintf("evt_%d", time.Now().UnixNano())
}
