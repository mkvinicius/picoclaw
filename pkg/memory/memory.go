// Package memory provides long-term persistent memory for PicoClaw agents.
// Stores conversation history, user preferences, business context and learned facts.
// Uses a simple file-based vector store (no external DB required).
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// MemoryType classifies a memory entry.
type MemoryType string

const (
	MemoryConversation MemoryType = "conversation" // chat history
	MemoryFact         MemoryType = "fact"         // learned facts about user/business
	MemoryPreference   MemoryType = "preference"   // user preferences
	MemoryTask         MemoryType = "task"         // completed tasks
	MemoryContext      MemoryType = "context"      // business/project context
)

// MemoryEntry represents a single memory item.
type MemoryEntry struct {
	ID        string            `json:"id"`
	Type      MemoryType        `json:"type"`
	Content   string            `json:"content"`
	Summary   string            `json:"summary"`
	Tags      []string          `json:"tags"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	AccessCount int             `json:"access_count"`
	Importance  float64         `json:"importance"` // 0.0 - 1.0
	Vector    []float64         `json:"vector,omitempty"` // semantic embedding (simplified)
}

// ConversationTurn represents a single turn in a conversation.
type ConversationTurn struct {
	Role      string    `json:"role"` // user, assistant, system
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// UserProfile stores persistent user/business information.
type UserProfile struct {
	Name         string            `json:"name"`
	Business     string            `json:"business"`
	Industry     string            `json:"industry"`
	Preferences  map[string]string `json:"preferences"`
	Goals        []string          `json:"goals"`
	LastSeen     time.Time         `json:"last_seen"`
	TotalSessions int              `json:"total_sessions"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Memory Store
// ─────────────────────────────────────────────────────────────────────────────

// Store is the main memory manager for PicoClaw.
type Store struct {
	dataDir      string
	entries      map[string]*MemoryEntry
	conversations []ConversationTurn
	profile      *UserProfile
	mu           sync.RWMutex
	maxHistory   int // max conversation turns to keep in memory
}

// NewStore creates a new memory store.
func NewStore(dataDir string) *Store {
	return &Store{
		dataDir:    dataDir,
		entries:    make(map[string]*MemoryEntry),
		maxHistory: 100,
		profile: &UserProfile{
			Preferences: make(map[string]string),
		},
	}
}

// Load loads all memory from disk.
func (s *Store) Load() error {
	if err := os.MkdirAll(s.dataDir, 0700); err != nil {
		return err
	}

	// Load entries
	entriesPath := filepath.Join(s.dataDir, "memories.json")
	if data, err := os.ReadFile(entriesPath); err == nil {
		var entries map[string]*MemoryEntry
		if json.Unmarshal(data, &entries) == nil {
			s.mu.Lock()
			s.entries = entries
			s.mu.Unlock()
		}
	}

	// Load conversation history
	convPath := filepath.Join(s.dataDir, "conversations.json")
	if data, err := os.ReadFile(convPath); err == nil {
		var turns []ConversationTurn
		if json.Unmarshal(data, &turns) == nil {
			s.mu.Lock()
			s.conversations = turns
			s.mu.Unlock()
		}
	}

	// Load user profile
	profilePath := filepath.Join(s.dataDir, "profile.json")
	if data, err := os.ReadFile(profilePath); err == nil {
		var profile UserProfile
		if json.Unmarshal(data, &profile) == nil {
			s.mu.Lock()
			s.profile = &profile
			s.mu.Unlock()
		}
	}

	s.mu.Lock()
	s.profile.LastSeen = time.Now()
	s.profile.TotalSessions++
	s.mu.Unlock()

	fmt.Printf("🧠 Memória carregada: %d memórias, %d conversas\n", len(s.entries), len(s.conversations))
	return nil
}

// AddConversationTurn adds a new turn to the conversation history.
func (s *Store) AddConversationTurn(role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.conversations = append(s.conversations, ConversationTurn{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})

	// Keep only last N turns in memory (older ones are summarized)
	if len(s.conversations) > s.maxHistory {
		s.conversations = s.conversations[len(s.conversations)-s.maxHistory:]
	}

	go s.saveConversations()
}

// GetRecentConversation returns the last N conversation turns.
func (s *Store) GetRecentConversation(n int) []ConversationTurn {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if n <= 0 || n > len(s.conversations) {
		n = len(s.conversations)
	}
	result := make([]ConversationTurn, n)
	copy(result, s.conversations[len(s.conversations)-n:])
	return result
}

// Remember stores a new memory entry.
func (s *Store) Remember(memType MemoryType, content, summary string, tags []string, importance float64) *MemoryEntry {
	entry := &MemoryEntry{
		ID:          fmt.Sprintf("mem_%d", time.Now().UnixNano()),
		Type:        memType,
		Content:     content,
		Summary:     summary,
		Tags:        tags,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Importance:  importance,
		Vector:      simpleEmbed(content),
	}

	s.mu.Lock()
	s.entries[entry.ID] = entry
	s.mu.Unlock()

	go s.saveEntries()
	return entry
}

// Recall searches for relevant memories using semantic similarity.
func (s *Store) Recall(query string, limit int, memTypes ...MemoryType) []MemoryEntry {
	queryVec := simpleEmbed(query)
	queryLower := strings.ToLower(query)

	s.mu.RLock()
	var candidates []struct {
		entry *MemoryEntry
		score float64
	}

	for _, entry := range s.entries {
		// Filter by type if specified
		if len(memTypes) > 0 {
			found := false
			for _, t := range memTypes {
				if entry.Type == t {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Calculate relevance score
		vecScore := cosineSimilarity(queryVec, entry.Vector)
		textScore := textSimilarity(queryLower, strings.ToLower(entry.Content+" "+entry.Summary))
		recencyScore := recencyBoost(entry.UpdatedAt)
		importanceScore := entry.Importance

		score := vecScore*0.4 + textScore*0.3 + recencyScore*0.15 + importanceScore*0.15

		if score > 0.1 {
			candidates = append(candidates, struct {
				entry *MemoryEntry
				score float64
			}{entry, score})
		}
	}
	s.mu.RUnlock()

	// Sort by score
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// Return top N
	if limit <= 0 {
		limit = 5
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	results := make([]MemoryEntry, len(candidates))
	for i, c := range candidates {
		results[i] = *c.entry
		// Update access count
		s.mu.Lock()
		c.entry.AccessCount++
		s.mu.Unlock()
	}

	return results
}

// UpdateProfile updates the user/business profile.
func (s *Store) UpdateProfile(updates map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, value := range updates {
		switch key {
		case "name":
			s.profile.Name = value
		case "business":
			s.profile.Business = value
		case "industry":
			s.profile.Industry = value
		default:
			s.profile.Preferences[key] = value
		}
	}

	go s.saveProfile()
}

// GetProfile returns the current user profile.
func (s *Store) GetProfile() UserProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return *s.profile
}

// BuildContext builds a context string for the AI from recent memories and profile.
func (s *Store) BuildContext(ctx context.Context, query string) string {
	profile := s.GetProfile()
	relevant := s.Recall(query, 5)
	recent := s.GetRecentConversation(10)

	var sb strings.Builder

	// User/business context
	if profile.Name != "" || profile.Business != "" {
		sb.WriteString("=== CONTEXTO DO USUÁRIO ===\n")
		if profile.Name != "" {
			sb.WriteString(fmt.Sprintf("Nome: %s\n", profile.Name))
		}
		if profile.Business != "" {
			sb.WriteString(fmt.Sprintf("Empresa/Negócio: %s\n", profile.Business))
		}
		if profile.Industry != "" {
			sb.WriteString(fmt.Sprintf("Setor: %s\n", profile.Industry))
		}
		if len(profile.Goals) > 0 {
			sb.WriteString(fmt.Sprintf("Objetivos: %s\n", strings.Join(profile.Goals, ", ")))
		}
		sb.WriteString("\n")
	}

	// Relevant memories
	if len(relevant) > 0 {
		sb.WriteString("=== MEMÓRIAS RELEVANTES ===\n")
		for _, m := range relevant {
			sb.WriteString(fmt.Sprintf("- [%s] %s\n", m.Type, m.Summary))
		}
		sb.WriteString("\n")
	}

	// Recent conversation
	if len(recent) > 0 {
		sb.WriteString("=== CONVERSA RECENTE ===\n")
		for _, turn := range recent {
			role := "Usuário"
			if turn.Role == "assistant" {
				role = "Assistente"
			}
			sb.WriteString(fmt.Sprintf("%s: %s\n", role, turn.Content))
		}
	}

	return sb.String()
}

// Forget removes a memory entry.
func (s *Store) Forget(id string) {
	s.mu.Lock()
	delete(s.entries, id)
	s.mu.Unlock()
	go s.saveEntries()
}

// Stats returns memory statistics.
func (s *Store) Stats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	typeCounts := make(map[string]int)
	for _, e := range s.entries {
		typeCounts[string(e.Type)]++
	}

	return map[string]interface{}{
		"total_memories":   len(s.entries),
		"total_turns":      len(s.conversations),
		"type_breakdown":   typeCounts,
		"user_name":        s.profile.Name,
		"total_sessions":   s.profile.TotalSessions,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Persistence helpers
// ─────────────────────────────────────────────────────────────────────────────

func (s *Store) saveEntries() {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.entries, "", "  ")
	s.mu.RUnlock()
	if err == nil {
		os.WriteFile(filepath.Join(s.dataDir, "memories.json"), data, 0600)
	}
}

func (s *Store) saveConversations() {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.conversations, "", "  ")
	s.mu.RUnlock()
	if err == nil {
		os.WriteFile(filepath.Join(s.dataDir, "conversations.json"), data, 0600)
	}
}

func (s *Store) saveProfile() {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.profile, "", "  ")
	s.mu.RUnlock()
	if err == nil {
		os.WriteFile(filepath.Join(s.dataDir, "profile.json"), data, 0600)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Simplified vector embedding (no external dependencies)
// Uses character n-gram frequency as a lightweight semantic representation
// ─────────────────────────────────────────────────────────────────────────────

const vecDim = 64

func simpleEmbed(text string) []float64 {
	text = strings.ToLower(text)
	vec := make([]float64, vecDim)

	// Character trigrams
	for i := 0; i < len(text)-2; i++ {
		trigram := text[i : i+3]
		hash := 0
		for _, c := range trigram {
			hash = hash*31 + int(c)
		}
		idx := ((hash % vecDim) + vecDim) % vecDim
		vec[idx]++
	}

	// Word unigrams
	words := strings.Fields(text)
	for _, w := range words {
		hash := 0
		for _, c := range w {
			hash = hash*37 + int(c)
		}
		idx := ((hash % vecDim) + vecDim) % vecDim
		vec[idx] += 2 // words have higher weight
	}

	// Normalize
	norm := 0.0
	for _, v := range vec {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}

	return vec
}

func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	dot, normA, normB := 0.0, 0.0, 0.0
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func textSimilarity(query, content string) float64 {
	queryWords := strings.Fields(query)
	if len(queryWords) == 0 {
		return 0
	}
	matches := 0
	for _, w := range queryWords {
		if strings.Contains(content, w) {
			matches++
		}
	}
	return float64(matches) / float64(len(queryWords))
}

func recencyBoost(t time.Time) float64 {
	age := time.Since(t)
	if age < 24*time.Hour {
		return 1.0
	}
	if age < 7*24*time.Hour {
		return 0.7
	}
	if age < 30*24*time.Hour {
		return 0.4
	}
	return 0.1
}
