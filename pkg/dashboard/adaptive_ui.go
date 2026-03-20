// Package dashboard — adaptive_ui.go
//
// Interface Adaptativa: Modo Foco vs. Modo Avançado.
//
// O dashboard web do Synapse oferece dois modos de visualização:
//
//   Modo Foco (padrão):
//   - Design minimalista, estilo Apple
//   - Cards flutuantes com sombras suaves
//   - Cores claras, tipografia limpa
//   - Apenas o chat central e squads ativos visíveis
//   - Sem jargão técnico, sem logs, sem métricas
//
//   Modo Avançado:
//   - Revela painel de logs do Shield (segurança)
//   - Gráficos de uso de memória e tokens em tempo real
//   - Árvore de execução do Oracle
//   - Debug de roteamento semântico
//   - Métricas de cache e latência
//   - Configurações avançadas de agentes
//
// A preferência é salva localmente e persiste entre sessões.
package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// UI Mode
// ─────────────────────────────────────────────────────────────────────────────

// UIMode represents the dashboard display mode.
type UIMode string

const (
	UIModeFocus    UIMode = "focus"    // clean, minimal, user-friendly
	UIModeAdvanced UIMode = "advanced" // full power, developer-friendly
)

// UIPreferences holds the user's dashboard preferences.
type UIPreferences struct {
	Mode            UIMode    `json:"mode"`
	Theme           string    `json:"theme"`            // "light", "dark", "auto"
	Language        string    `json:"language"`         // "pt-BR", "en-US"
	SidebarOpen     bool      `json:"sidebar_open"`
	ShowTokenCount  bool      `json:"show_token_count"`
	ShowLatency     bool      `json:"show_latency"`
	ShowCacheHits   bool      `json:"show_cache_hits"`
	CompactMessages bool      `json:"compact_messages"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// DefaultUIPreferences returns the default (focus mode) preferences.
func DefaultUIPreferences() UIPreferences {
	return UIPreferences{
		Mode:            UIModeFocus,
		Theme:           "light",
		Language:        "pt-BR",
		SidebarOpen:     true,
		ShowTokenCount:  false,
		ShowLatency:     false,
		ShowCacheHits:   false,
		CompactMessages: false,
		UpdatedAt:       time.Now(),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UIState — live metrics for advanced mode
// ─────────────────────────────────────────────────────────────────────────────

// UIState holds real-time metrics shown in advanced mode.
type UIState struct {
	// Memory metrics
	MemoryEntriesTotal  int     `json:"memory_entries_total"`
	KnowledgeGraphNodes int     `json:"knowledge_graph_nodes"`
	CacheHitRate        float64 `json:"cache_hit_rate"`

	// Token metrics
	TokensUsedSession   int     `json:"tokens_used_session"`
	TokensUsedTotal     int     `json:"tokens_used_total"`
	EstimatedCostUSD    float64 `json:"estimated_cost_usd"`
	TokensSaved         int     `json:"tokens_saved_by_cache"`

	// Routing metrics
	LastIntent          string  `json:"last_intent"`
	LastModelTier       string  `json:"last_model_tier"`
	LastConfidence      float64 `json:"last_confidence"`
	SelfCritiqueApplied bool    `json:"self_critique_applied"`

	// Security metrics
	ThreatsDetected     int     `json:"threats_detected_session"`
	AgentsQuarantined   int     `json:"agents_quarantined"`
	ShieldStatus        string  `json:"shield_status"` // "active", "learning", "alert"

	// Oracle metrics
	OracleSimulations   int     `json:"oracle_simulations_total"`
	LastSimulationMode  string  `json:"last_simulation_mode"`

	// System metrics
	UptimeSeconds       int64   `json:"uptime_seconds"`
	GoroutinesActive    int     `json:"goroutines_active"`
	MemoryUsageMB       float64 `json:"memory_usage_mb"`

	UpdatedAt           time.Time `json:"updated_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// AdaptiveUIManager
// ─────────────────────────────────────────────────────────────────────────────

// AdaptiveUIManager manages the dashboard UI state and preferences.
type AdaptiveUIManager struct {
	dataDir  string
	prefs    UIPreferences
	state    UIState
	mu       sync.RWMutex
	startAt  time.Time
}

// NewAdaptiveUIManager creates a new manager, loading saved preferences if available.
func NewAdaptiveUIManager(dataDir string) (*AdaptiveUIManager, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("adaptive_ui: mkdir: %w", err)
	}

	m := &AdaptiveUIManager{
		dataDir: dataDir,
		prefs:   DefaultUIPreferences(),
		startAt: time.Now(),
	}

	// Load saved preferences
	if err := m.loadPrefs(); err != nil && !os.IsNotExist(err) {
		// Non-fatal: use defaults
	}

	return m, nil
}

// GetPreferences returns the current UI preferences.
func (m *AdaptiveUIManager) GetPreferences() UIPreferences {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.prefs
}

// SetMode switches the UI mode and saves the preference.
func (m *AdaptiveUIManager) SetMode(mode UIMode) error {
	m.mu.Lock()
	m.prefs.Mode = mode
	m.prefs.UpdatedAt = time.Now()
	m.mu.Unlock()
	return m.savePrefs()
}

// UpdatePreferences updates and saves UI preferences.
func (m *AdaptiveUIManager) UpdatePreferences(prefs UIPreferences) error {
	m.mu.Lock()
	prefs.UpdatedAt = time.Now()
	m.prefs = prefs
	m.mu.Unlock()
	return m.savePrefs()
}

// UpdateState updates the live metrics state (called by other modules).
func (m *AdaptiveUIManager) UpdateState(fn func(*UIState)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.state)
	m.state.UpdatedAt = time.Now()
	m.state.UptimeSeconds = int64(time.Since(m.startAt).Seconds())
}

// GetState returns the current live state.
func (m *AdaptiveUIManager) GetState() UIState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP Handlers
// ─────────────────────────────────────────────────────────────────────────────

// RegisterRoutes registers the adaptive UI API routes on the given mux.
func (m *AdaptiveUIManager) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/ui/preferences", m.handlePreferences)
	mux.HandleFunc("/api/ui/state", m.handleState)
	mux.HandleFunc("/api/ui/mode", m.handleMode)
}

func (m *AdaptiveUIManager) handlePreferences(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:7777")

	switch r.Method {
	case http.MethodGet:
		prefs := m.GetPreferences()
		json.NewEncoder(w).Encode(prefs)

	case http.MethodPut, http.MethodPost:
		var prefs UIPreferences
		if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		if err := m.UpdatePreferences(prefs); err != nil {
			http.Error(w, `{"error":"save failed"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *AdaptiveUIManager) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:7777")
	json.NewEncoder(w).Encode(m.GetState())
}

func (m *AdaptiveUIManager) handleMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	var body struct {
		Mode UIMode `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if body.Mode != UIModeFocus && body.Mode != UIModeAdvanced {
		http.Error(w, `{"error":"invalid mode"}`, http.StatusBadRequest)
		return
	}

	if err := m.SetMode(body.Mode); err != nil {
		http.Error(w, `{"error":"save failed"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "mode": string(body.Mode)})
}

// ─────────────────────────────────────────────────────────────────────────────
// Dashboard HTML — Adaptive Layout
// ─────────────────────────────────────────────────────────────────────────────

// AdaptiveDashboardHTML returns the HTML for the adaptive dashboard.
// The JavaScript reads the user's mode preference and renders accordingly.
func AdaptiveDashboardHTML() string {
	return `<!DOCTYPE html>
<html lang="pt-BR" data-theme="light">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Synapse</title>
  <style>
    /* ── Reset & Base ─────────────────────────────────────────────── */
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

    :root {
      --bg:          #F5F5F7;
      --surface:     #FFFFFF;
      --surface-2:   #F0F0F2;
      --border:      rgba(0,0,0,0.08);
      --text:        #1D1D1F;
      --text-2:      #6E6E73;
      --accent:      #0071E3;
      --accent-soft: #E8F0FE;
      --success:     #34C759;
      --warning:     #FF9F0A;
      --danger:      #FF3B30;
      --radius:      14px;
      --radius-sm:   8px;
      --shadow:      0 2px 20px rgba(0,0,0,0.08);
      --shadow-hover:0 4px 30px rgba(0,0,0,0.12);
      --font:        -apple-system, BlinkMacSystemFont, 'SF Pro Display', 'Segoe UI', sans-serif;
      --transition:  0.2s cubic-bezier(0.4, 0, 0.2, 1);
    }

    [data-theme="dark"] {
      --bg:          #000000;
      --surface:     #1C1C1E;
      --surface-2:   #2C2C2E;
      --border:      rgba(255,255,255,0.08);
      --text:        #F5F5F7;
      --text-2:      #98989F;
      --accent-soft: #1A2A3A;
    }

    body {
      font-family: var(--font);
      background: var(--bg);
      color: var(--text);
      min-height: 100vh;
      -webkit-font-smoothing: antialiased;
    }

    /* ── Layout ───────────────────────────────────────────────────── */
    .app {
      display: grid;
      grid-template-columns: 260px 1fr;
      grid-template-rows: 56px 1fr;
      height: 100vh;
      overflow: hidden;
    }

    .app.sidebar-closed {
      grid-template-columns: 0 1fr;
    }

    /* ── Topbar ───────────────────────────────────────────────────── */
    .topbar {
      grid-column: 1 / -1;
      display: flex;
      align-items: center;
      padding: 0 20px;
      background: rgba(255,255,255,0.8);
      backdrop-filter: blur(20px);
      -webkit-backdrop-filter: blur(20px);
      border-bottom: 1px solid var(--border);
      gap: 12px;
      z-index: 100;
    }

    [data-theme="dark"] .topbar {
      background: rgba(28,28,30,0.8);
    }

    .topbar-logo {
      font-size: 17px;
      font-weight: 600;
      letter-spacing: -0.3px;
      color: var(--text);
    }

    .topbar-logo span { color: var(--accent); }

    .topbar-spacer { flex: 1; }

    .mode-toggle {
      display: flex;
      background: var(--surface-2);
      border-radius: 20px;
      padding: 3px;
      gap: 2px;
    }

    .mode-btn {
      padding: 5px 14px;
      border-radius: 17px;
      border: none;
      background: transparent;
      color: var(--text-2);
      font-size: 13px;
      font-weight: 500;
      cursor: pointer;
      transition: var(--transition);
    }

    .mode-btn.active {
      background: var(--surface);
      color: var(--text);
      box-shadow: 0 1px 4px rgba(0,0,0,0.12);
    }

    .topbar-btn {
      width: 32px; height: 32px;
      border-radius: 50%;
      border: none;
      background: var(--surface-2);
      color: var(--text-2);
      cursor: pointer;
      display: flex; align-items: center; justify-content: center;
      font-size: 15px;
      transition: var(--transition);
    }

    .topbar-btn:hover { background: var(--border); color: var(--text); }

    /* ── Sidebar ──────────────────────────────────────────────────── */
    .sidebar {
      background: var(--surface);
      border-right: 1px solid var(--border);
      overflow-y: auto;
      padding: 16px 12px;
      display: flex;
      flex-direction: column;
      gap: 8px;
    }

    .sidebar-section-title {
      font-size: 11px;
      font-weight: 600;
      color: var(--text-2);
      text-transform: uppercase;
      letter-spacing: 0.5px;
      padding: 8px 8px 4px;
    }

    .squad-card {
      display: flex;
      align-items: center;
      gap: 10px;
      padding: 10px 12px;
      border-radius: var(--radius-sm);
      cursor: pointer;
      transition: var(--transition);
      border: 1px solid transparent;
    }

    .squad-card:hover {
      background: var(--accent-soft);
      border-color: var(--accent);
    }

    .squad-card.active {
      background: var(--accent-soft);
      border-color: var(--accent);
    }

    .squad-icon {
      width: 32px; height: 32px;
      border-radius: var(--radius-sm);
      background: var(--accent);
      display: flex; align-items: center; justify-content: center;
      font-size: 16px;
      flex-shrink: 0;
    }

    .squad-name {
      font-size: 14px;
      font-weight: 500;
      color: var(--text);
    }

    .squad-status {
      font-size: 11px;
      color: var(--text-2);
    }

    /* ── Main Area ────────────────────────────────────────────────── */
    .main {
      display: flex;
      flex-direction: column;
      overflow: hidden;
      background: var(--bg);
    }

    /* ── Chat Area ────────────────────────────────────────────────── */
    .chat-area {
      flex: 1;
      overflow-y: auto;
      padding: 24px;
      display: flex;
      flex-direction: column;
      gap: 16px;
    }

    .message {
      max-width: 680px;
      animation: fadeUp 0.3s ease;
    }

    .message.user { align-self: flex-end; }
    .message.assistant { align-self: flex-start; }

    @keyframes fadeUp {
      from { opacity: 0; transform: translateY(8px); }
      to   { opacity: 1; transform: translateY(0); }
    }

    .message-bubble {
      padding: 14px 18px;
      border-radius: var(--radius);
      font-size: 15px;
      line-height: 1.6;
      box-shadow: var(--shadow);
    }

    .message.user .message-bubble {
      background: var(--accent);
      color: #fff;
      border-bottom-right-radius: 4px;
    }

    .message.assistant .message-bubble {
      background: var(--surface);
      color: var(--text);
      border-bottom-left-radius: 4px;
    }

    .message-meta {
      font-size: 11px;
      color: var(--text-2);
      margin-top: 4px;
      padding: 0 4px;
    }

    .message.user .message-meta { text-align: right; }

    /* ── Oracle Card ──────────────────────────────────────────────── */
    .oracle-card {
      background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
      border-radius: var(--radius);
      padding: 16px 20px;
      color: white;
      display: flex;
      align-items: center;
      gap: 12px;
      cursor: pointer;
      box-shadow: var(--shadow);
      transition: var(--transition);
      max-width: 680px;
      align-self: flex-start;
    }

    .oracle-card:hover { box-shadow: var(--shadow-hover); transform: translateY(-1px); }

    .oracle-pulse {
      width: 10px; height: 10px;
      border-radius: 50%;
      background: rgba(255,255,255,0.8);
      animation: pulse 2s infinite;
    }

    @keyframes pulse {
      0%, 100% { opacity: 1; transform: scale(1); }
      50%       { opacity: 0.5; transform: scale(1.3); }
    }

    /* ── Input Area ───────────────────────────────────────────────── */
    .input-area {
      padding: 16px 24px 20px;
      background: var(--bg);
    }

    .input-wrapper {
      display: flex;
      align-items: flex-end;
      gap: 10px;
      background: var(--surface);
      border: 1.5px solid var(--border);
      border-radius: var(--radius);
      padding: 10px 14px;
      box-shadow: var(--shadow);
      transition: var(--transition);
    }

    .input-wrapper:focus-within {
      border-color: var(--accent);
      box-shadow: 0 0 0 3px rgba(0,113,227,0.12);
    }

    .chat-input {
      flex: 1;
      border: none;
      outline: none;
      background: transparent;
      font-family: var(--font);
      font-size: 15px;
      color: var(--text);
      resize: none;
      max-height: 120px;
      line-height: 1.5;
    }

    .chat-input::placeholder { color: var(--text-2); }

    .send-btn {
      width: 34px; height: 34px;
      border-radius: 50%;
      border: none;
      background: var(--accent);
      color: white;
      cursor: pointer;
      display: flex; align-items: center; justify-content: center;
      font-size: 16px;
      flex-shrink: 0;
      transition: var(--transition);
    }

    .send-btn:hover { background: #0077ED; transform: scale(1.05); }
    .send-btn:disabled { background: var(--surface-2); color: var(--text-2); cursor: not-allowed; transform: none; }

    /* ── Advanced Panel ───────────────────────────────────────────── */
    .advanced-panel {
      display: none;
      background: var(--surface);
      border-top: 1px solid var(--border);
      padding: 16px 24px;
      gap: 16px;
      flex-wrap: wrap;
    }

    .app.mode-advanced .advanced-panel { display: flex; }

    .metric-card {
      background: var(--surface-2);
      border-radius: var(--radius-sm);
      padding: 12px 16px;
      min-width: 140px;
    }

    .metric-label {
      font-size: 11px;
      color: var(--text-2);
      text-transform: uppercase;
      letter-spacing: 0.4px;
      margin-bottom: 4px;
    }

    .metric-value {
      font-size: 22px;
      font-weight: 600;
      color: var(--text);
      letter-spacing: -0.5px;
    }

    .metric-value.accent { color: var(--accent); }
    .metric-value.success { color: var(--success); }
    .metric-value.warning { color: var(--warning); }

    /* ── Scrollbar ────────────────────────────────────────────────── */
    ::-webkit-scrollbar { width: 6px; }
    ::-webkit-scrollbar-track { background: transparent; }
    ::-webkit-scrollbar-thumb { background: var(--border); border-radius: 3px; }
  </style>
</head>
<body>
<div class="app" id="app">
  <!-- Topbar -->
  <header class="topbar">
    <button class="topbar-btn" onclick="toggleSidebar()" title="Menu">☰</button>
    <div class="topbar-logo">Synapse<span>.</span></div>
    <div class="topbar-spacer"></div>

    <!-- Mode Toggle -->
    <div class="mode-toggle">
      <button class="mode-btn active" id="btn-focus" onclick="setMode('focus')">Foco</button>
      <button class="mode-btn" id="btn-advanced" onclick="setMode('advanced')">Avançado</button>
    </div>

    <button class="topbar-btn" onclick="toggleTheme()" title="Tema" id="theme-btn">🌙</button>
    <button class="topbar-btn" title="Configurações">⚙</button>
  </header>

  <!-- Sidebar -->
  <aside class="sidebar" id="sidebar">
    <div class="sidebar-section-title">Squads Ativos</div>

    <div class="squad-card active">
      <div class="squad-icon">💬</div>
      <div>
        <div class="squad-name">Assistente Geral</div>
        <div class="squad-status">● Online</div>
      </div>
    </div>

    <div class="squad-card">
      <div class="squad-icon">📊</div>
      <div>
        <div class="squad-name">Analista de Mercado</div>
        <div class="squad-status">○ Standby</div>
      </div>
    </div>

    <div class="squad-card">
      <div class="squad-icon">🛡️</div>
      <div>
        <div class="squad-name">Shield</div>
        <div class="squad-status">● Monitorando</div>
      </div>
    </div>

    <div class="squad-card" onclick="openOracle()">
      <div class="squad-icon" style="background: linear-gradient(135deg, #667eea, #764ba2)">🔮</div>
      <div>
        <div class="squad-name">Oracle</div>
        <div class="squad-status">○ Standby</div>
      </div>
    </div>

    <div class="sidebar-section-title" style="margin-top:8px">Criar Squad</div>
    <div class="squad-card" onclick="createSquad()">
      <div class="squad-icon" style="background: var(--surface-2); font-size: 20px">+</div>
      <div>
        <div class="squad-name" style="color: var(--accent)">Novo Squad</div>
        <div class="squad-status">Descreva em linguagem natural</div>
      </div>
    </div>
  </aside>

  <!-- Main -->
  <main class="main">
    <!-- Chat Area -->
    <div class="chat-area" id="chat-area">
      <div class="message assistant">
        <div class="message-bubble">
          Olá! Sou o <strong>Synapse</strong> — seu assistente de inteligência coletiva. Como posso ajudar hoje?
        </div>
        <div class="message-meta">Agora</div>
      </div>
    </div>

    <!-- Advanced Metrics Panel (hidden in focus mode) -->
    <div class="advanced-panel" id="advanced-panel">
      <div class="metric-card">
        <div class="metric-label">Tokens Sessão</div>
        <div class="metric-value accent" id="m-tokens">0</div>
      </div>
      <div class="metric-card">
        <div class="metric-label">Cache Hits</div>
        <div class="metric-value success" id="m-cache">0</div>
      </div>
      <div class="metric-card">
        <div class="metric-label">Último Modelo</div>
        <div class="metric-value" id="m-model" style="font-size:14px">—</div>
      </div>
      <div class="metric-card">
        <div class="metric-label">Intenção</div>
        <div class="metric-value" id="m-intent" style="font-size:14px">—</div>
      </div>
      <div class="metric-card">
        <div class="metric-label">Shield</div>
        <div class="metric-value success" id="m-shield">Ativo</div>
      </div>
      <div class="metric-card">
        <div class="metric-label">RAM</div>
        <div class="metric-value" id="m-ram">—</div>
      </div>
    </div>

    <!-- Input Area -->
    <div class="input-area">
      <div class="input-wrapper">
        <textarea
          class="chat-input"
          id="chat-input"
          placeholder="Mensagem para o Synapse..."
          rows="1"
          onkeydown="handleKey(event)"
          oninput="autoResize(this)"
        ></textarea>
        <button class="send-btn" id="send-btn" onclick="sendMessage()">↑</button>
      </div>
    </div>
  </main>
</div>

<script>
  // ── State ──────────────────────────────────────────────────────────
  let currentMode = 'focus';
  let isDark = false;
  let sessionTokens = 0;
  let cacheHits = 0;

  // ── Init ───────────────────────────────────────────────────────────
  (async function init() {
    try {
      const r = await fetch('/api/ui/preferences');
      if (r.ok) {
        const prefs = await r.json();
        applyPreferences(prefs);
      }
    } catch(e) { /* offline: use defaults */ }

    // Start metrics polling in advanced mode
    setInterval(pollMetrics, 3000);
  })();

  function applyPreferences(prefs) {
    if (prefs.mode) setMode(prefs.mode, false);
    if (prefs.theme === 'dark') setDark(true, false);
  }

  // ── Mode Toggle ────────────────────────────────────────────────────
  function setMode(mode, save = true) {
    currentMode = mode;
    const app = document.getElementById('app');
    app.classList.toggle('mode-advanced', mode === 'advanced');

    document.getElementById('btn-focus').classList.toggle('active', mode === 'focus');
    document.getElementById('btn-advanced').classList.toggle('active', mode === 'advanced');

    if (save) {
      fetch('/api/ui/mode', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({mode})
      }).catch(() => {});
    }
  }

  // ── Theme Toggle ───────────────────────────────────────────────────
  function toggleTheme() {
    setDark(!isDark);
  }

  function setDark(dark, save = true) {
    isDark = dark;
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
    document.getElementById('theme-btn').textContent = dark ? '☀' : '🌙';
    if (save) {
      fetch('/api/ui/preferences', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({mode: currentMode, theme: dark ? 'dark' : 'light', language: 'pt-BR'})
      }).catch(() => {});
    }
  }

  // ── Sidebar ────────────────────────────────────────────────────────
  function toggleSidebar() {
    document.getElementById('app').classList.toggle('sidebar-closed');
  }

  // ── Chat ───────────────────────────────────────────────────────────
  function handleKey(e) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendMessage();
    }
  }

  function autoResize(el) {
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 120) + 'px';
  }

  async function sendMessage() {
    const input = document.getElementById('chat-input');
    const text = input.value.trim();
    if (!text) return;

    input.value = '';
    input.style.height = 'auto';
    document.getElementById('send-btn').disabled = true;

    appendMessage('user', text);

    try {
      const r = await fetch('/api/chat', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({message: text})
      });

      if (r.ok) {
        const data = await r.json();
        appendMessage('assistant', data.response, data.meta);

        // Update metrics
        if (data.meta) {
          if (data.meta.tokens) {
            sessionTokens += data.meta.tokens;
            document.getElementById('m-tokens').textContent = sessionTokens;
          }
          if (data.meta.from_cache) {
            cacheHits++;
            document.getElementById('m-cache').textContent = cacheHits;
          }
          if (data.meta.model) document.getElementById('m-model').textContent = data.meta.model;
          if (data.meta.intent) document.getElementById('m-intent').textContent = data.meta.intent;

          // Show Oracle suggestion if detected
          if (data.meta.oracle_suggested) {
            appendOracleSuggestion(data.meta.oracle_context);
          }
        }
      } else {
        appendMessage('assistant', 'Desculpe, ocorreu um erro. Tente novamente.');
      }
    } catch(e) {
      appendMessage('assistant', 'Sem conexão com o servidor. Verifique se o Synapse está rodando.');
    }

    document.getElementById('send-btn').disabled = false;
    scrollToBottom();
  }

  function appendMessage(role, text, meta) {
    const area = document.getElementById('chat-area');
    const div = document.createElement('div');
    div.className = 'message ' + role;

    const time = new Date().toLocaleTimeString('pt-BR', {hour:'2-digit', minute:'2-digit'});
    const metaStr = meta ? buildMetaStr(meta, time) : time;

    div.innerHTML =
      '<div class="message-bubble">' + escapeHtml(text) + '</div>' +
      '<div class="message-meta">' + metaStr + '</div>';

    area.appendChild(div);
    scrollToBottom();
  }

  function buildMetaStr(meta, time) {
    let parts = [time];
    if (meta.from_cache) parts.push('⚡ cache');
    if (meta.self_critique) parts.push('✓ revisado');
    if (meta.model) parts.push(meta.model);
    return parts.join(' · ');
  }

  function appendOracleSuggestion(context) {
    const area = document.getElementById('chat-area');
    const div = document.createElement('div');
    div.className = 'oracle-card';
    div.onclick = () => openOracle(context);
    div.innerHTML =
      '<div class="oracle-pulse"></div>' +
      '<div><strong>Oracle detectou uma oportunidade de previsão</strong><br>' +
      '<small>Clique para simular cenários e antecipar resultados</small></div>' +
      '<div style="margin-left:auto;font-size:20px">🔮</div>';
    area.appendChild(div);
    scrollToBottom();
  }

  function openOracle(context) {
    const msg = context
      ? 'Oracle: simule o cenário — ' + context
      : 'Oracle: quero uma simulação preditiva do cenário atual';
    document.getElementById('chat-input').value = msg;
    document.getElementById('chat-input').focus();
  }

  function createSquad() {
    document.getElementById('chat-input').value = 'Quero criar um novo squad para ';
    document.getElementById('chat-input').focus();
  }

  function scrollToBottom() {
    const area = document.getElementById('chat-area');
    area.scrollTop = area.scrollHeight;
  }

  function escapeHtml(text) {
    return text
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/\n/g, '<br>');
  }

  // ── Metrics Polling (Advanced Mode) ───────────────────────────────
  async function pollMetrics() {
    if (currentMode !== 'advanced') return;
    try {
      const r = await fetch('/api/ui/state');
      if (!r.ok) return;
      const s = await r.json();

      if (s.tokens_used_session) document.getElementById('m-tokens').textContent = s.tokens_used_session;
      if (s.last_model_tier) document.getElementById('m-model').textContent = s.last_model_tier;
      if (s.last_intent) document.getElementById('m-intent').textContent = s.last_intent;
      if (s.memory_usage_mb) document.getElementById('m-ram').textContent = s.memory_usage_mb.toFixed(1) + ' MB';
      if (s.shield_status) {
        const el = document.getElementById('m-shield');
        el.textContent = s.shield_status === 'active' ? 'Ativo' : s.shield_status === 'alert' ? '⚠ Alerta' : s.shield_status;
        el.className = 'metric-value ' + (s.shield_status === 'alert' ? 'warning' : 'success');
      }
    } catch(e) { /* ignore */ }
  }
</script>
</body>
</html>`
}

// ─────────────────────────────────────────────────────────────────────────────
// Persistence
// ─────────────────────────────────────────────────────────────────────────────

const prefsFile = "ui_preferences.json"

func (m *AdaptiveUIManager) savePrefs() error {
	m.mu.RLock()
	data, err := json.MarshalIndent(m.prefs, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return err
	}

	path := filepath.Join(m.dataDir, prefsFile)
	return os.WriteFile(path, data, 0o600)
}

func (m *AdaptiveUIManager) loadPrefs() error {
	path := filepath.Join(m.dataDir, prefsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return json.Unmarshal(data, &m.prefs)
}
