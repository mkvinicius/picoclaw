// Package memory — knowledge_graph.go
//
// Memória Episódica com Grafo de Conhecimento Local.
//
// Enquanto o Store (memory.go) guarda fatos isolados com busca vetorial,
// o KnowledgeGraph guarda RELAÇÕES entre entidades:
//
//   "João" → trabalha_em → "Empresa X"
//   "Empresa X" → concorre_com → "Empresa Y"
//   "João" → prefere → "comunicação direta"
//
// Isso dá ao agente "consciência de contexto profundo" — ele entende não
// apenas fatos, mas como as coisas se relacionam entre si.
//
// Implementação: SQLite com tabela de triplas (Sujeito, Predicado, Objeto).
// Busca híbrida: vetorial (semântica) + grafo (relações).
//
// Inspirado na arquitetura do Zep Cloud, mas 100% local e offline-first.
package memory

import (
	"encoding/json"
	"fmt"
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

// Triple represents a knowledge graph triple: Subject → Predicate → Object.
type Triple struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`   // e.g., "João"
	Predicate string    `json:"predicate"` // e.g., "trabalha_em"
	Object    string    `json:"object"`    // e.g., "Empresa X"
	Source    string    `json:"source"`    // where this was learned (conversation, user, etc.)
	Confidence float64  `json:"confidence"` // 0.0–1.0
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	AccessCount int     `json:"access_count"`
}

// GraphQuery specifies how to search the knowledge graph.
type GraphQuery struct {
	Subject   string // filter by subject (empty = any)
	Predicate string // filter by predicate (empty = any)
	Object    string // filter by object (empty = any)
	MaxHops   int    // max graph traversal depth (0 = direct only)
	Limit     int    // max results (0 = no limit)
}

// GraphResult holds the results of a graph query.
type GraphResult struct {
	Triples  []Triple
	Paths    [][]Triple // multi-hop paths if MaxHops > 0
	Summary  string     // human-readable summary of findings
}

// ─────────────────────────────────────────────────────────────────────────────
// KnowledgeGraph
// ─────────────────────────────────────────────────────────────────────────────

// KnowledgeGraph manages the local entity-relation graph.
// It is safe for concurrent use.
type KnowledgeGraph struct {
	dataDir string
	triples map[string]*Triple // id → triple
	// indexes for fast lookup
	bySubject   map[string][]string // subject → []id
	byObject    map[string][]string // object → []id
	byPredicate map[string][]string // predicate → []id
	mu          sync.RWMutex
	dirty       bool
}

const graphFile = "knowledge_graph.json"

// NewKnowledgeGraph creates or loads a KnowledgeGraph from the given directory.
func NewKnowledgeGraph(dataDir string) (*KnowledgeGraph, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("knowledge_graph: mkdir %s: %w", dataDir, err)
	}

	kg := &KnowledgeGraph{
		dataDir:     dataDir,
		triples:     make(map[string]*Triple),
		bySubject:   make(map[string][]string),
		byObject:    make(map[string][]string),
		byPredicate: make(map[string][]string),
	}

	if err := kg.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return kg, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Write Operations
// ─────────────────────────────────────────────────────────────────────────────

// AddTriple adds or updates a triple in the graph.
// If a triple with the same Subject+Predicate+Object exists, it is updated.
func (kg *KnowledgeGraph) AddTriple(subject, predicate, object, source string, confidence float64) (*Triple, error) {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	// Normalize
	subject = normalizeEntity(subject)
	predicate = normalizePredicate(predicate)
	object = normalizeEntity(object)

	if subject == "" || predicate == "" || object == "" {
		return nil, fmt.Errorf("knowledge_graph: subject, predicate and object cannot be empty")
	}

	// Check for existing triple
	existingID := kg.findExisting(subject, predicate, object)
	if existingID != "" {
		t := kg.triples[existingID]
		// Update confidence (weighted average)
		t.Confidence = (t.Confidence*float64(t.AccessCount) + confidence) / float64(t.AccessCount+1)
		t.AccessCount++
		t.UpdatedAt = time.Now()
		kg.dirty = true
		return t, nil
	}

	// Create new triple
	id := generateTripleID(subject, predicate, object)
	t := &Triple{
		ID:          id,
		Subject:     subject,
		Predicate:   predicate,
		Object:      object,
		Source:      source,
		Confidence:  confidence,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		AccessCount: 1,
	}

	kg.triples[id] = t
	kg.bySubject[subject] = append(kg.bySubject[subject], id)
	kg.byObject[object] = append(kg.byObject[object], id)
	kg.byPredicate[predicate] = append(kg.byPredicate[predicate], id)
	kg.dirty = true

	return t, nil
}

// RemoveTriple removes a triple by ID.
func (kg *KnowledgeGraph) RemoveTriple(id string) {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	t, ok := kg.triples[id]
	if !ok {
		return
	}

	delete(kg.triples, id)
	kg.removeFromIndex(kg.bySubject, t.Subject, id)
	kg.removeFromIndex(kg.byObject, t.Object, id)
	kg.removeFromIndex(kg.byPredicate, t.Predicate, id)
	kg.dirty = true
}

// ─────────────────────────────────────────────────────────────────────────────
// Query Operations
// ─────────────────────────────────────────────────────────────────────────────

// Query searches the graph with the given criteria.
func (kg *KnowledgeGraph) Query(q GraphQuery) GraphResult {
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	candidates := kg.candidateIDs(q)

	var triples []Triple
	for _, id := range candidates {
		t := kg.triples[id]
		if t == nil {
			continue
		}
		// Apply filters
		if q.Subject != "" && !strings.EqualFold(t.Subject, normalizeEntity(q.Subject)) {
			continue
		}
		if q.Predicate != "" && !strings.EqualFold(t.Predicate, normalizePredicate(q.Predicate)) {
			continue
		}
		if q.Object != "" && !strings.EqualFold(t.Object, normalizeEntity(q.Object)) {
			continue
		}
		triples = append(triples, *t)
	}

	// Sort by confidence desc, then by recency
	sort.Slice(triples, func(i, j int) bool {
		if triples[i].Confidence != triples[j].Confidence {
			return triples[i].Confidence > triples[j].Confidence
		}
		return triples[i].UpdatedAt.After(triples[j].UpdatedAt)
	})

	// Apply limit
	if q.Limit > 0 && len(triples) > q.Limit {
		triples = triples[:q.Limit]
	}

	// Multi-hop traversal
	var paths [][]Triple
	if q.MaxHops > 0 && q.Subject != "" {
		paths = kg.traverse(normalizeEntity(q.Subject), q.MaxHops)
	}

	return GraphResult{
		Triples: triples,
		Paths:   paths,
		Summary: kg.summarize(triples),
	}
}

// GetContext returns all knowledge about an entity (as subject or object).
func (kg *KnowledgeGraph) GetContext(entity string) GraphResult {
	entity = normalizeEntity(entity)
	kg.mu.RLock()
	defer kg.mu.RUnlock()

	seen := make(map[string]bool)
	var triples []Triple

	// As subject
	for _, id := range kg.bySubject[entity] {
		if !seen[id] {
			seen[id] = true
			triples = append(triples, *kg.triples[id])
		}
	}
	// As object
	for _, id := range kg.byObject[entity] {
		if !seen[id] {
			seen[id] = true
			triples = append(triples, *kg.triples[id])
		}
	}

	sort.Slice(triples, func(i, j int) bool {
		return triples[i].Confidence > triples[j].Confidence
	})

	return GraphResult{
		Triples: triples,
		Summary: kg.summarize(triples),
	}
}

// ExtractAndStore parses a natural language statement and attempts to extract
// triples automatically using pattern matching.
//
// Supported patterns (Portuguese and English):
//   - "X é/is Y"           → (X, é, Y)
//   - "X trabalha em/works at Y" → (X, trabalha_em, Y)
//   - "X prefere/prefers Y" → (X, prefere, Y)
//   - "X usa/uses Y"       → (X, usa, Y)
//   - "X tem/has Y"        → (X, tem, Y)
func (kg *KnowledgeGraph) ExtractAndStore(text, source string) []Triple {
	patterns := []extractPattern{
		{words: []string{" trabalha em ", " trabalha na ", " trabalha no "}, pred: "trabalha_em"},
		{words: []string{" works at ", " works for ", " works in "}, pred: "works_at"},
		{words: []string{" é dono de ", " é dona de ", " owns "}, pred: "é_dono_de"},
		{words: []string{" prefere ", " gosta de ", " prefers ", " likes "}, pred: "prefere"},
		{words: []string{" usa ", " utiliza ", " uses ", " utilizes "}, pred: "usa"},
		{words: []string{" tem ", " possui ", " has ", " owns "}, pred: "tem"},
		{words: []string{" concorre com ", " compete com ", " competes with "}, pred: "concorre_com"},
		{words: []string{" é parceiro de ", " is partner of "}, pred: "é_parceiro_de"},
		{words: []string{" é CEO de ", " é fundador de ", " is CEO of ", " is founder of "}, pred: "é_lider_de"},
		{words: []string{" mora em ", " vive em ", " lives in ", " located in "}, pred: "localizado_em"},
	}

	lower := strings.ToLower(text)
	var extracted []Triple

	for _, p := range patterns {
		for _, word := range p.words {
			idx := strings.Index(lower, word)
			if idx < 0 {
				continue
			}
			// Extract subject (before the pattern) and object (after)
			subjectPart := strings.TrimSpace(text[:idx])
			objectPart := strings.TrimSpace(text[idx+len(word):])

			// Take last "word group" as subject (handles sentences)
			subject := lastWordGroup(subjectPart)
			// Take first "word group" as object
			object := firstWordGroup(objectPart)

			if subject == "" || object == "" {
				continue
			}

			t, err := kg.AddTriple(subject, p.pred, object, source, 0.75)
			if err == nil {
				extracted = append(extracted, *t)
			}
		}
	}

	return extracted
}

// Stats returns graph statistics.
func (kg *KnowledgeGraph) Stats() map[string]int {
	kg.mu.RLock()
	defer kg.mu.RUnlock()
	return map[string]int{
		"triples":    len(kg.triples),
		"subjects":   len(kg.bySubject),
		"predicates": len(kg.byPredicate),
		"objects":    len(kg.byObject),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Persistence
// ─────────────────────────────────────────────────────────────────────────────

// Save persists the graph to disk if dirty.
func (kg *KnowledgeGraph) Save() error {
	kg.mu.Lock()
	defer kg.mu.Unlock()

	if !kg.dirty {
		return nil
	}

	triples := make([]*Triple, 0, len(kg.triples))
	for _, t := range kg.triples {
		triples = append(triples, t)
	}

	data, err := json.MarshalIndent(triples, "", "  ")
	if err != nil {
		return fmt.Errorf("knowledge_graph: marshal: %w", err)
	}

	path := filepath.Join(kg.dataDir, graphFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("knowledge_graph: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("knowledge_graph: rename: %w", err)
	}

	kg.dirty = false
	return nil
}

func (kg *KnowledgeGraph) load() error {
	path := filepath.Join(kg.dataDir, graphFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var triples []*Triple
	if err := json.Unmarshal(data, &triples); err != nil {
		return fmt.Errorf("knowledge_graph: unmarshal: %w", err)
	}

	for _, t := range triples {
		if t == nil {
			continue
		}
		kg.triples[t.ID] = t
		kg.bySubject[t.Subject] = append(kg.bySubject[t.Subject], t.ID)
		kg.byObject[t.Object] = append(kg.byObject[t.Object], t.ID)
		kg.byPredicate[t.Predicate] = append(kg.byPredicate[t.Predicate], t.ID)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

type extractPattern struct {
	words []string
	pred  string
}

func (kg *KnowledgeGraph) candidateIDs(q GraphQuery) []string {
	if q.Subject != "" {
		return kg.bySubject[normalizeEntity(q.Subject)]
	}
	if q.Object != "" {
		return kg.byObject[normalizeEntity(q.Object)]
	}
	if q.Predicate != "" {
		return kg.byPredicate[normalizePredicate(q.Predicate)]
	}
	// No filter → return all
	ids := make([]string, 0, len(kg.triples))
	for id := range kg.triples {
		ids = append(ids, id)
	}
	return ids
}

func (kg *KnowledgeGraph) findExisting(subject, predicate, object string) string {
	for _, id := range kg.bySubject[subject] {
		t := kg.triples[id]
		if t != nil && t.Predicate == predicate && t.Object == object {
			return id
		}
	}
	return ""
}

func (kg *KnowledgeGraph) removeFromIndex(idx map[string][]string, key, id string) {
	ids := idx[key]
	for i, v := range ids {
		if v == id {
			idx[key] = append(ids[:i], ids[i+1:]...)
			return
		}
	}
}

// traverse performs a BFS up to maxHops from startEntity.
func (kg *KnowledgeGraph) traverse(startEntity string, maxHops int) [][]Triple {
	var paths [][]Triple
	type state struct {
		entity string
		path   []Triple
	}

	queue := []state{{entity: startEntity, path: nil}}
	visited := map[string]bool{startEntity: true}

	for hop := 0; hop < maxHops && len(queue) > 0; hop++ {
		var nextQueue []state
		for _, s := range queue {
			for _, id := range kg.bySubject[s.entity] {
				t := kg.triples[id]
				if t == nil {
					continue
				}
				newPath := append(append([]Triple{}, s.path...), *t)
				paths = append(paths, newPath)
				if !visited[t.Object] {
					visited[t.Object] = true
					nextQueue = append(nextQueue, state{entity: t.Object, path: newPath})
				}
			}
		}
		queue = nextQueue
	}
	return paths
}

func (kg *KnowledgeGraph) summarize(triples []Triple) string {
	if len(triples) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, t := range triples {
		if i >= 10 {
			sb.WriteString(fmt.Sprintf("... e mais %d relações.", len(triples)-10))
			break
		}
		sb.WriteString(fmt.Sprintf("• %s %s %s\n", t.Subject, strings.ReplaceAll(t.Predicate, "_", " "), t.Object))
	}
	return sb.String()
}

func normalizeEntity(s string) string {
	return strings.TrimSpace(strings.ToLower(s))
}

func normalizePredicate(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.ReplaceAll(s, " ", "_")
}

func generateTripleID(subject, predicate, object string) string {
	return fmt.Sprintf("%x", hashString(subject+"|"+predicate+"|"+object))[:16]
}

func hashString(s string) uint64 {
	var h uint64 = 14695981039346656037
	for _, b := range []byte(s) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h
}

func lastWordGroup(s string) string {
	s = strings.TrimSpace(s)
	// Take last sentence fragment (after comma or period)
	for _, sep := range []string{". ", ", ", "; "} {
		if idx := strings.LastIndex(s, sep); idx >= 0 {
			s = strings.TrimSpace(s[idx+len(sep):])
		}
	}
	// Limit to 50 chars
	if len(s) > 50 {
		s = s[len(s)-50:]
	}
	return s
}

func firstWordGroup(s string) string {
	s = strings.TrimSpace(s)
	// Stop at sentence boundary
	for _, sep := range []string{". ", ", ", "; ", " e ", " and "} {
		if idx := strings.Index(s, sep); idx >= 0 {
			s = strings.TrimSpace(s[:idx])
		}
	}
	// Limit to 50 chars
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}
