// Package dashboard provides a local web dashboard for PicoClaw.
// It serves a React-based UI at http://localhost:7474 that shows:
// - Real-time agent status and activity
// - Squad builder and manager
// - Security events (MIL-SPEC)
// - Resource usage (RAM, CPU, API cost)
// - Task history and scheduler
// - Home automation controls
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"
)

const DefaultPort = 7474

// DashboardServer serves the local web dashboard.
type DashboardServer struct {
	port      int
	server    *http.Server
	hub       *WebSocketHub
	state     *DashboardState
	mu        sync.RWMutex
	startTime time.Time
}

// DashboardState holds the current state of the system.
type DashboardState struct {
	Agents        []AgentStatus   `json:"agents"`
	Squads        []SquadStatus   `json:"squads"`
	SecurityEvents []SecurityEvent `json:"security_events"`
	ScheduledTasks []ScheduledTask `json:"scheduled_tasks"`
	SystemStats   SystemStats     `json:"system_stats"`
	APEXConnected bool            `json:"apex_connected"`
	Mode          string          `json:"mode"` // online, offline, auto
}

// AgentStatus represents the status of a single agent.
type AgentStatus struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Status    string    `json:"status"` // idle, running, paused, error
	Task      string    `json:"task"`
	Progress  int       `json:"progress"` // 0-100
	StartedAt time.Time `json:"started_at"`
	Squad     string    `json:"squad"`
}

// SquadStatus represents a squad of agents.
type SquadStatus struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Agents  []AgentStatus `json:"agents"`
	Status  string        `json:"status"`
	Created time.Time     `json:"created"`
}

// SecurityEvent represents a security event detected by MIL-SPEC.
type SecurityEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Severity  string    `json:"severity"` // info, warning, critical
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Blocked   bool      `json:"blocked"`
}

// ScheduledTask represents a scheduled automation task.
type ScheduledTask struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Schedule    string    `json:"schedule"` // cron expression or natural language
	NextRun     time.Time `json:"next_run"`
	LastRun     time.Time `json:"last_run"`
	Status      string    `json:"status"` // active, paused, completed
	Description string    `json:"description"`
}

// SystemStats holds real-time system resource usage.
type SystemStats struct {
	RAMUsedMB   uint64  `json:"ram_used_mb"`
	RAMTotalMB  uint64  `json:"ram_total_mb"`
	CPUPercent  float64 `json:"cpu_percent"`
	APICallsToday int   `json:"api_calls_today"`
	APICostToday  float64 `json:"api_cost_today"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	ActiveAgents  int    `json:"active_agents"`
}

// NewDashboardServer creates a new dashboard server.
func NewDashboardServer(port int) *DashboardServer {
	if port == 0 {
		port = DefaultPort
	}
	hub := newWebSocketHub()
	return &DashboardServer{
		port:      port,
		hub:       hub,
		startTime: time.Now(),
		state: &DashboardState{
			Agents:        []AgentStatus{},
			Squads:        []SquadStatus{},
			SecurityEvents: []SecurityEvent{},
			ScheduledTasks: []ScheduledTask{},
			Mode:          "auto",
		},
	}
}

// Start starts the dashboard server.
func (d *DashboardServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// WebSocket endpoint for real-time updates
	mux.HandleFunc("/ws", d.hub.handleWebSocket)

	// REST API endpoints
	mux.HandleFunc("/api/state", d.handleGetState)
	mux.HandleFunc("/api/agents", d.handleAgents)
	mux.HandleFunc("/api/squads", d.handleSquads)
	mux.HandleFunc("/api/security", d.handleSecurity)
	mux.HandleFunc("/api/schedule", d.handleSchedule)
	mux.HandleFunc("/api/stats", d.handleStats)
	mux.HandleFunc("/api/command", d.handleCommand)

	// Serve the embedded HTML dashboard
	mux.HandleFunc("/", d.handleDashboardHTML)

	d.server = &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", d.port),
		Handler: mux,
	}

	// Start WebSocket hub
	go d.hub.run()

	// Start stats collector
	go d.collectStats(ctx)

	// Start broadcaster
	go d.broadcastUpdates(ctx)

	fmt.Printf("🖥️  Dashboard disponível em: http://localhost:%d\n", d.port)

	go func() {
		if err := d.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Dashboard error: %v\n", err)
		}
	}()

	return nil
}

// Stop stops the dashboard server.
func (d *DashboardServer) Stop(ctx context.Context) error {
	if d.server != nil {
		return d.server.Shutdown(ctx)
	}
	return nil
}

// UpdateAgent updates the status of an agent and broadcasts to all clients.
func (d *DashboardServer) UpdateAgent(agent AgentStatus) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i, a := range d.state.Agents {
		if a.ID == agent.ID {
			d.state.Agents[i] = agent
			d.broadcastEvent("agent_update", agent)
			return
		}
	}
	d.state.Agents = append(d.state.Agents, agent)
	d.broadcastEvent("agent_added", agent)
}

// AddSecurityEvent adds a security event and broadcasts it.
func (d *DashboardServer) AddSecurityEvent(event SecurityEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()

	event.ID = fmt.Sprintf("sec_%d", time.Now().UnixNano())
	event.Timestamp = time.Now()

	// Keep last 100 events
	d.state.SecurityEvents = append([]SecurityEvent{event}, d.state.SecurityEvents...)
	if len(d.state.SecurityEvents) > 100 {
		d.state.SecurityEvents = d.state.SecurityEvents[:100]
	}

	d.broadcastEvent("security_event", event)
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP Handlers
// ─────────────────────────────────────────────────────────────────────────────

func (d *DashboardServer) handleGetState(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(d.state)
}

func (d *DashboardServer) handleAgents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	d.mu.RLock()
	defer d.mu.RUnlock()
	json.NewEncoder(w).Encode(d.state.Agents)
}

func (d *DashboardServer) handleSquads(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == http.MethodPost {
		var squad SquadStatus
		if err := json.NewDecoder(r.Body).Decode(&squad); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		squad.ID = fmt.Sprintf("squad_%d", time.Now().UnixNano())
		squad.Created = time.Now()
		d.mu.Lock()
		d.state.Squads = append(d.state.Squads, squad)
		d.mu.Unlock()
		d.broadcastEvent("squad_created", squad)
		json.NewEncoder(w).Encode(squad)
		return
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	json.NewEncoder(w).Encode(d.state.Squads)
}

func (d *DashboardServer) handleSecurity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	d.mu.RLock()
	defer d.mu.RUnlock()
	json.NewEncoder(w).Encode(d.state.SecurityEvents)
}

func (d *DashboardServer) handleSchedule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == http.MethodPost {
		var task ScheduledTask
		if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		task.ID = fmt.Sprintf("task_%d", time.Now().UnixNano())
		task.Status = "active"
		d.mu.Lock()
		d.state.ScheduledTasks = append(d.state.ScheduledTasks, task)
		d.mu.Unlock()
		d.broadcastEvent("task_scheduled", task)
		json.NewEncoder(w).Encode(task)
		return
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	json.NewEncoder(w).Encode(d.state.ScheduledTasks)
}

func (d *DashboardServer) handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	d.mu.RLock()
	defer d.mu.RUnlock()
	json.NewEncoder(w).Encode(d.state.SystemStats)
}

func (d *DashboardServer) handleCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var cmd struct {
		Command string `json:"command"`
		AgentID string `json:"agent_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Broadcast command to all connected clients (including APEX bridge)
	d.broadcastEvent("command", cmd)
	json.NewEncoder(w).Encode(map[string]string{"status": "dispatched"})
}

func (d *DashboardServer) handleDashboardHTML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

func (d *DashboardServer) broadcastEvent(eventType string, data interface{}) {
	payload, _ := json.Marshal(map[string]interface{}{
		"type": eventType,
		"data": data,
	})
	d.hub.broadcast(payload)
}

func (d *DashboardServer) collectStats(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var memStats runtime.MemStats
			runtime.ReadMemStats(&memStats)

			d.mu.Lock()
			d.state.SystemStats.RAMUsedMB = memStats.Alloc / 1024 / 1024
			d.state.SystemStats.RAMTotalMB = memStats.Sys / 1024 / 1024
			d.state.SystemStats.UptimeSeconds = int64(time.Since(d.startTime).Seconds())
			activeCount := 0
			for _, a := range d.state.Agents {
				if a.Status == "running" {
					activeCount++
				}
			}
			d.state.SystemStats.ActiveAgents = activeCount
			d.mu.Unlock()
		}
	}
}

func (d *DashboardServer) broadcastUpdates(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.mu.RLock()
			stats := d.state.SystemStats
			d.mu.RUnlock()
			d.broadcastEvent("stats_update", stats)
		}
	}
}
