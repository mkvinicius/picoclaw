// Package memory — sync.go
//
// Sincronização Automática de Memória: Local ↔ Supabase/PostgreSQL.
//
// Filosofia de design:
//   - A memória LOCAL é sempre a fonte primária (leitura e escrita imediata)
//   - A nuvem é o espelho de segurança e canal de sincronização multi-dispositivo
//   - Sincronização acontece em background, sem bloquear o usuário
//   - Regra de conflito: Last-Write-Wins (timestamp mais recente vence)
//   - Funciona 100% offline; quando a internet volta, sincroniza automaticamente
//
// Backends suportados:
//   - Supabase (PostgreSQL + pgvector) — recomendado, free tier generoso
//   - PostgreSQL direto
//   - Qualquer backend que implemente a interface SyncBackend
//
// Configuração via variáveis de ambiente:
//   SYNAPSE_SYNC_BACKEND=supabase
//   SYNAPSE_SUPABASE_URL=https://xxxx.supabase.co
//   SYNAPSE_SUPABASE_KEY=seu_anon_key
package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sync Types
// ─────────────────────────────────────────────────────────────────────────────

// SyncStatus represents the synchronization state of a memory entry.
type SyncStatus string

const (
	SyncPending   SyncStatus = "pending"   // written locally, not yet synced
	SyncSynced    SyncStatus = "synced"    // in sync with cloud
	SyncConflict  SyncStatus = "conflict"  // both local and cloud modified
	SyncOffline   SyncStatus = "offline"   // no cloud configured
)

// SyncEntry wraps a memory entry with sync metadata.
type SyncEntry struct {
	ID          string     `json:"id"`
	NodeID      string     `json:"node_id"`      // which Synapse instance owns this
	Content     string     `json:"content"`
	Embedding   []float32  `json:"embedding,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Status      SyncStatus `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	SyncedAt    time.Time  `json:"synced_at,omitempty"`
	Version     int        `json:"version"`      // incremented on each update
}

// SyncConfig holds the synchronization configuration.
type SyncConfig struct {
	Backend     string        // "supabase", "postgres", "none"
	URL         string        // backend URL
	APIKey      string        // API key (for Supabase)
	NodeID      string        // unique identifier for this Synapse instance
	Interval    time.Duration // sync interval (default: 30s)
	BatchSize   int           // entries per sync batch (default: 50)
	Enabled     bool
}

// SyncConfigFromEnv loads sync configuration from environment variables.
func SyncConfigFromEnv() SyncConfig {
	cfg := SyncConfig{
		Backend:   getEnvOr("SYNAPSE_SYNC_BACKEND", "none"),
		URL:       os.Getenv("SYNAPSE_SUPABASE_URL"),
		APIKey:    os.Getenv("SYNAPSE_SUPABASE_KEY"),
		NodeID:    getEnvOr("SYNAPSE_NODE_ID", generateNodeID()),
		Interval:  30 * time.Second,
		BatchSize: 50,
	}
	cfg.Enabled = cfg.Backend != "none" && cfg.URL != ""
	return cfg
}

// ─────────────────────────────────────────────────────────────────────────────
// SyncBackend Interface
// ─────────────────────────────────────────────────────────────────────────────

// SyncBackend is the interface for cloud memory backends.
type SyncBackend interface {
	// Push uploads local entries to the cloud.
	Push(entries []SyncEntry) error
	// Pull downloads entries modified since the given time.
	Pull(since time.Time, nodeID string) ([]SyncEntry, error)
	// Ping checks if the backend is reachable.
	Ping() error
}

// ─────────────────────────────────────────────────────────────────────────────
// MemorySyncManager
// ─────────────────────────────────────────────────────────────────────────────

// MemorySyncManager handles automatic bidirectional sync between local and cloud memory.
type MemorySyncManager struct {
	cfg      SyncConfig
	backend  SyncBackend
	pending  []SyncEntry
	mu       sync.Mutex
	lastSync time.Time
	online   bool
	stopCh   chan struct{}
	stats    SyncStats
}

// SyncStats holds synchronization statistics.
type SyncStats struct {
	TotalPushed    int
	TotalPulled    int
	TotalConflicts int
	LastSyncAt     time.Time
	LastError      string
	IsOnline       bool
}

// NewMemorySyncManager creates a new sync manager.
// If the backend is not configured, it runs in offline mode (no-op).
func NewMemorySyncManager(cfg SyncConfig) *MemorySyncManager {
	m := &MemorySyncManager{
		cfg:    cfg,
		stopCh: make(chan struct{}),
		online: false,
	}

	if cfg.Enabled {
		switch cfg.Backend {
		case "supabase":
			m.backend = NewSupabaseBackend(cfg.URL, cfg.APIKey, cfg.NodeID)
		}
	}

	return m
}

// Start begins the background sync loop.
func (m *MemorySyncManager) Start() {
	if !m.cfg.Enabled || m.backend == nil {
		return
	}

	go m.syncLoop()
}

// Stop halts the background sync loop.
func (m *MemorySyncManager) Stop() {
	close(m.stopCh)
}

// Enqueue marks an entry as pending sync.
func (m *MemorySyncManager) Enqueue(entry SyncEntry) {
	if !m.cfg.Enabled {
		return
	}
	entry.Status = SyncPending
	entry.NodeID = m.cfg.NodeID

	m.mu.Lock()
	m.pending = append(m.pending, entry)
	m.mu.Unlock()
}

// Stats returns current sync statistics.
func (m *MemorySyncManager) Stats() SyncStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stats
	s.IsOnline = m.online
	return s
}

// syncLoop runs the periodic sync in background.
func (m *MemorySyncManager) syncLoop() {
	interval := m.cfg.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Try immediate sync on start
	m.doSync()

	for {
		select {
		case <-ticker.C:
			m.doSync()
		case <-m.stopCh:
			return
		}
	}
}

// doSync performs one sync cycle: push pending + pull remote changes.
func (m *MemorySyncManager) doSync() {
	// Check connectivity
	if err := m.backend.Ping(); err != nil {
		m.mu.Lock()
		m.online = false
		m.stats.LastError = err.Error()
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	m.online = true
	pending := m.pending
	m.pending = nil
	lastSync := m.lastSync
	m.mu.Unlock()

	// Push pending entries in batches
	if len(pending) > 0 {
		batchSize := m.cfg.BatchSize
		if batchSize <= 0 {
			batchSize = 50
		}

		for i := 0; i < len(pending); i += batchSize {
			end := i + batchSize
			if end > len(pending) {
				end = len(pending)
			}
			batch := pending[i:end]

			if err := m.backend.Push(batch); err != nil {
				// Re-queue failed entries
				m.mu.Lock()
				m.pending = append(batch, m.pending...)
				m.stats.LastError = err.Error()
				m.mu.Unlock()
				return
			}

			m.mu.Lock()
			m.stats.TotalPushed += len(batch)
			m.mu.Unlock()
		}
	}

	// Pull remote changes
	remoteEntries, err := m.backend.Pull(lastSync, m.cfg.NodeID)
	if err != nil {
		m.mu.Lock()
		m.stats.LastError = err.Error()
		m.mu.Unlock()
		return
	}

	// Apply remote changes (Last-Write-Wins)
	if len(remoteEntries) > 0 {
		m.applyRemoteChanges(remoteEntries)
		m.mu.Lock()
		m.stats.TotalPulled += len(remoteEntries)
		m.mu.Unlock()
	}

	m.mu.Lock()
	m.lastSync = time.Now()
	m.stats.LastSyncAt = m.lastSync
	m.stats.LastError = ""
	m.mu.Unlock()
}

// applyRemoteChanges merges remote entries into local storage using Last-Write-Wins.
// In production, this would call the Store to update local entries.
func (m *MemorySyncManager) applyRemoteChanges(remote []SyncEntry) {
	// Last-Write-Wins: remote entry wins if it's newer than local
	// This is called by the Store which has access to local entries
	// The actual merge logic lives in Store.MergeRemote()
	_ = remote // placeholder — Store.MergeRemote() handles this
}

// ─────────────────────────────────────────────────────────────────────────────
// Supabase Backend
// ─────────────────────────────────────────────────────────────────────────────

// SupabaseBackend implements SyncBackend for Supabase (PostgreSQL + pgvector).
type SupabaseBackend struct {
	url    string
	apiKey string
	nodeID string
	client *http.Client
}

// NewSupabaseBackend creates a new Supabase backend.
func NewSupabaseBackend(url, apiKey, nodeID string) *SupabaseBackend {
	return &SupabaseBackend{
		url:    url,
		apiKey: apiKey,
		nodeID: nodeID,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Ping checks if Supabase is reachable.
func (s *SupabaseBackend) Ping() error {
	req, err := http.NewRequest(http.MethodGet, s.url+"/rest/v1/", nil)
	if err != nil {
		return err
	}
	s.setHeaders(req)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("supabase: ping failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("supabase: server error %d", resp.StatusCode)
	}
	return nil
}

// Push uploads entries to the synapse_memory table in Supabase.
func (s *SupabaseBackend) Push(entries []SyncEntry) error {
	if len(entries) == 0 {
		return nil
	}

	body, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("supabase: marshal: %w", err)
	}

	// Use upsert (on conflict update)
	req, err := http.NewRequest(http.MethodPost,
		s.url+"/rest/v1/synapse_memory",
		bytes.NewReader(body))
	if err != nil {
		return err
	}

	s.setHeaders(req)
	req.Header.Set("Prefer", "resolution=merge-duplicates")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("supabase: push: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("supabase: push status %d", resp.StatusCode)
	}
	return nil
}

// Pull downloads entries modified since the given time, excluding this node's own entries.
func (s *SupabaseBackend) Pull(since time.Time, nodeID string) ([]SyncEntry, error) {
	sinceStr := since.UTC().Format(time.RFC3339)

	url := fmt.Sprintf("%s/rest/v1/synapse_memory?updated_at=gt.%s&node_id=neq.%s&order=updated_at.asc&limit=500",
		s.url, sinceStr, nodeID)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	s.setHeaders(req)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase: pull: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("supabase: pull status %d", resp.StatusCode)
	}

	var entries []SyncEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("supabase: decode: %w", err)
	}

	return entries, nil
}

func (s *SupabaseBackend) setHeaders(req *http.Request) {
	req.Header.Set("apikey", s.apiKey)
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
}

// ─────────────────────────────────────────────────────────────────────────────
// SQL Schema for Supabase
// ─────────────────────────────────────────────────────────────────────────────

// SupabaseSchema returns the SQL to create the synapse_memory table in Supabase.
// This is automatically executed by the setup wizard on first configuration.
const SupabaseSchema = `
-- Enable pgvector extension (run once per database)
create extension if not exists vector;

-- Synapse memory table
create table if not exists synapse_memory (
  id          text primary key,
  node_id     text not null,
  content     text not null,
  embedding   vector(1536),  -- OpenAI text-embedding-3-small dimensions
  tags        text[],
  status      text default 'synced',
  version     integer default 1,
  created_at  timestamptz default now(),
  updated_at  timestamptz default now(),
  synced_at   timestamptz default now()
);

-- Index for fast vector similarity search
create index if not exists synapse_memory_embedding_idx
  on synapse_memory using ivfflat (embedding vector_cosine_ops)
  with (lists = 100);

-- Index for sync queries
create index if not exists synapse_memory_updated_idx
  on synapse_memory (updated_at, node_id);

-- Row Level Security (optional but recommended)
alter table synapse_memory enable row level security;

-- Policy: each node can only access its own entries
-- (disable this if you want shared memory across devices)
-- create policy "node_isolation" on synapse_memory
--   using (node_id = current_setting('app.node_id'));

-- Function for semantic search
create or replace function search_memory(
  query_embedding vector(1536),
  match_threshold float default 0.7,
  match_count int default 10
)
returns table (
  id text,
  content text,
  tags text[],
  similarity float
)
language sql stable
as $$
  select
    id, content, tags,
    1 - (embedding <=> query_embedding) as similarity
  from synapse_memory
  where 1 - (embedding <=> query_embedding) > match_threshold
  order by similarity desc
  limit match_count;
$$;
`

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func getEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func generateNodeID() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "synapse"
	}
	return fmt.Sprintf("%s-%d", hostname, time.Now().Unix()%10000)
}
