// Package fleet provides fleet management for PicoClaw: registration,
// heartbeat tracking, health probing, and command dispatch across N nodes.
//
// # Architecture
//
// Each running PicoClaw instance is a "node" in the fleet. Nodes self-register
// and emit periodic heartbeats. The FleetManager tracks status, marks stale
// nodes offline, and persists the registry to disk (JSONL) so it survives
// restarts.
//
// # HTTP Endpoints (mount via RegisterOnMux)
//
//	GET  /api/fleet/nodes          – list all nodes
//	GET  /api/fleet/nodes/:id      – get a single node
//	POST /api/fleet/nodes/register – register or refresh a node
//	POST /api/fleet/nodes/:id/heartbeat – send heartbeat
//	GET  /api/fleet/summary        – aggregate stats
package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// NodeStatus represents the liveness state of a fleet node.
type NodeStatus string

const (
	NodeOnline   NodeStatus = "online"
	NodeOffline  NodeStatus = "offline"
	NodeDegraded NodeStatus = "degraded"
	NodeUnknown  NodeStatus = "unknown"
)

// defaultOfflineThreshold is how long without a heartbeat before a node is
// considered offline.
const defaultOfflineThreshold = 2 * time.Minute

// NodeInfo holds all metadata for a single fleet node.
type NodeInfo struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Status       NodeStatus        `json:"status"`
	Version      string            `json:"version,omitempty"`
	Arch         string            `json:"arch,omitempty"`
	OS           string            `json:"os,omitempty"`
	HealthURL    string            `json:"health_url,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	RegisteredAt time.Time         `json:"registered_at"`
	LastSeenAt   time.Time         `json:"last_seen_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// IsAlive returns true if the node has sent a heartbeat within the threshold.
func (n *NodeInfo) IsAlive(threshold time.Duration) bool {
	return time.Since(n.LastSeenAt) < threshold
}

// FleetConfig holds configuration for the FleetManager.
type FleetConfig struct {
	// WorkspaceDir is where the fleet registry is persisted (fleet.jsonl).
	WorkspaceDir string
	// OfflineThreshold is how long without a heartbeat before marking offline.
	OfflineThreshold time.Duration
	// ProbeInterval is how often the manager probes nodes' health endpoints.
	// Zero disables active probing (rely on heartbeats only).
	ProbeInterval time.Duration
	// SweepInterval is how often the manager sweeps for stale nodes.
	SweepInterval time.Duration
}

// DefaultFleetConfig returns sensible defaults for a given workspace.
func DefaultFleetConfig(workspaceDir string) FleetConfig {
	return FleetConfig{
		WorkspaceDir:     workspaceDir,
		OfflineThreshold: defaultOfflineThreshold,
		ProbeInterval:    30 * time.Second,
		SweepInterval:    30 * time.Second,
	}
}

// FleetManager tracks all nodes in the fleet, persists registry to disk,
// and runs background sweeps to detect stale nodes.
type FleetManager struct {
	cfg      FleetConfig
	nodes    map[string]*NodeInfo
	mu       sync.RWMutex
	dataFile string
	stopCh   chan struct{}
	once     sync.Once
}

// NewFleetManager creates a new FleetManager and loads any persisted registry.
func NewFleetManager(cfg FleetConfig) (*FleetManager, error) {
	if cfg.OfflineThreshold == 0 {
		cfg.OfflineThreshold = defaultOfflineThreshold
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = 30 * time.Second
	}

	dataDir := filepath.Join(cfg.WorkspaceDir, "fleet")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("fleet: create data dir: %w", err)
	}

	fm := &FleetManager{
		cfg:      cfg,
		nodes:    make(map[string]*NodeInfo),
		dataFile: filepath.Join(dataDir, "nodes.json"),
		stopCh:   make(chan struct{}),
	}

	// Best-effort load persisted nodes.
	_ = fm.load()
	return fm, nil
}

// Start launches the background sweep goroutine.
func (fm *FleetManager) Start(ctx context.Context) {
	fm.once.Do(func() {
		go fm.sweepLoop(ctx)
		if fm.cfg.ProbeInterval > 0 {
			go fm.probeLoop(ctx)
		}
	})
}

// Stop signals the background goroutines to exit.
func (fm *FleetManager) Stop() {
	select {
	case <-fm.stopCh:
	default:
		close(fm.stopCh)
	}
}

// Register adds or updates a node in the fleet.
func (fm *FleetManager) Register(node NodeInfo) {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	now := time.Now()
	if existing, ok := fm.nodes[node.ID]; ok {
		// Preserve registration timestamp; update everything else.
		node.RegisteredAt = existing.RegisteredAt
	} else {
		node.RegisteredAt = now
	}
	node.LastSeenAt = now
	node.UpdatedAt = now
	if node.Status == "" {
		node.Status = NodeOnline
	}
	fm.nodes[node.ID] = &node
	_ = fm.save()
}

// Heartbeat updates the LastSeenAt for a node and marks it online.
func (fm *FleetManager) Heartbeat(nodeID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	node, ok := fm.nodes[nodeID]
	if !ok {
		return fmt.Errorf("fleet: unknown node %q", nodeID)
	}
	node.LastSeenAt = time.Now()
	node.UpdatedAt = time.Now()
	node.Status = NodeOnline
	_ = fm.save()
	return nil
}

// GetNode returns a copy of the node with the given ID.
func (fm *FleetManager) GetNode(id string) (NodeInfo, bool) {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	n, ok := fm.nodes[id]
	if !ok {
		return NodeInfo{}, false
	}
	return *n, true
}

// ListNodes returns a snapshot of all nodes, optionally filtered by status.
func (fm *FleetManager) ListNodes(statusFilter ...NodeStatus) []NodeInfo {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	filter := make(map[NodeStatus]bool, len(statusFilter))
	for _, s := range statusFilter {
		filter[s] = true
	}

	out := make([]NodeInfo, 0, len(fm.nodes))
	for _, n := range fm.nodes {
		if len(filter) > 0 && !filter[n.Status] {
			continue
		}
		out = append(out, *n)
	}
	return out
}

// Summary returns aggregate statistics about the fleet.
func (fm *FleetManager) Summary() map[string]any {
	fm.mu.RLock()
	defer fm.mu.RUnlock()

	counts := map[NodeStatus]int{}
	for _, n := range fm.nodes {
		counts[n.Status]++
	}
	return map[string]any{
		"total":    len(fm.nodes),
		"online":   counts[NodeOnline],
		"offline":  counts[NodeOffline],
		"degraded": counts[NodeDegraded],
		"unknown":  counts[NodeUnknown],
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP Handlers
// ─────────────────────────────────────────────────────────────────────────────

// RegisterOnMux mounts all fleet HTTP endpoints on the given ServeMux.
func (fm *FleetManager) RegisterOnMux(mux *http.ServeMux) {
	mux.HandleFunc("/api/fleet/nodes", fm.handleNodes)
	mux.HandleFunc("/api/fleet/nodes/register", fm.handleRegister)
	mux.HandleFunc("/api/fleet/summary", fm.handleSummary)
	// Handle /api/fleet/nodes/{id} and /api/fleet/nodes/{id}/heartbeat
	mux.HandleFunc("/api/fleet/nodes/", fm.handleNodeID)
}

func (fm *FleetManager) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodes := fm.ListNodes()
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "count": len(nodes)})
}

func (fm *FleetManager) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var node NodeInfo
	if err := json.NewDecoder(r.Body).Decode(&node); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(node.ID) == "" {
		http.Error(w, "node id is required", http.StatusBadRequest)
		return
	}
	fm.Register(node)
	registered, _ := fm.GetNode(node.ID)
	writeJSON(w, http.StatusOK, registered)
}

func (fm *FleetManager) handleNodeID(w http.ResponseWriter, r *http.Request) {
	// Path: /api/fleet/nodes/{id}  or  /api/fleet/nodes/{id}/heartbeat
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/fleet/nodes/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	nodeID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "heartbeat" && r.Method == http.MethodPost:
		if err := fm.Heartbeat(nodeID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})

	case action == "" && r.Method == http.MethodGet:
		node, ok := fm.GetNode(nodeID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, node)

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (fm *FleetManager) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, fm.Summary())
}

// ─────────────────────────────────────────────────────────────────────────────
// Background goroutines
// ─────────────────────────────────────────────────────────────────────────────

// sweepLoop periodically marks nodes as offline if they haven't sent a
// heartbeat within OfflineThreshold.
func (fm *FleetManager) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(fm.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			fm.sweep()
		case <-ctx.Done():
			return
		case <-fm.stopCh:
			return
		}
	}
}

func (fm *FleetManager) sweep() {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	changed := false
	for _, n := range fm.nodes {
		if n.Status == NodeOnline && !n.IsAlive(fm.cfg.OfflineThreshold) {
			n.Status = NodeOffline
			n.UpdatedAt = time.Now()
			changed = true
		}
	}
	if changed {
		_ = fm.save()
	}
}

// probeLoop actively probes nodes that have a HealthURL configured.
func (fm *FleetManager) probeLoop(ctx context.Context) {
	ticker := time.NewTicker(fm.cfg.ProbeInterval)
	defer ticker.Stop()
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		select {
		case <-ticker.C:
			fm.probeAll(ctx, client)
		case <-ctx.Done():
			return
		case <-fm.stopCh:
			return
		}
	}
}

func (fm *FleetManager) probeAll(ctx context.Context, client *http.Client) {
	fm.mu.RLock()
	targets := make([]struct {
		id  string
		url string
	}, 0)
	for id, n := range fm.nodes {
		if n.HealthURL != "" {
			targets = append(targets, struct {
				id  string
				url string
			}{id, n.HealthURL})
		}
	}
	fm.mu.RUnlock()

	for _, t := range targets {
		status := NodeOnline
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
		if err != nil {
			status = NodeDegraded
		} else if resp, err := client.Do(req); err != nil {
			status = NodeOffline
		} else {
			resp.Body.Close()
			if resp.StatusCode >= 500 {
				status = NodeDegraded
			}
		}

		fm.mu.Lock()
		if n, ok := fm.nodes[t.id]; ok {
			if status == NodeOnline {
				n.LastSeenAt = time.Now()
			}
			if n.Status != status {
				n.Status = status
				n.UpdatedAt = time.Now()
			}
		}
		fm.mu.Unlock()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Persistence
// ─────────────────────────────────────────────────────────────────────────────

func (fm *FleetManager) save() error {
	data, err := json.MarshalIndent(fm.nodes, "", "  ")
	if err != nil {
		return err
	}
	tmp := fm.dataFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fm.dataFile)
}

func (fm *FleetManager) load() error {
	data, err := os.ReadFile(fm.dataFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &fm.nodes)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
