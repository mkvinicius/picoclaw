package homeautomation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// HomeOrchestrator — Central hub for all home automation
// ─────────────────────────────────────────────────────────────────────────────
// This is the main entry point that APEX calls to control the physical world.
// It integrates:
//   - Home Assistant (lights, climate, locks, scenes)
//   - Zigbee2MQTT (Zigbee devices directly)
//   - IP Cameras (snapshots, RTSP streams)
//   - Energy Monitor (consumption tracking)
//   - Presence Detection (who is home)
//   - Automation Rules (if/then/when logic)
//   - Security Integration (MIL-SPEC alerts for physical intrusion)

// OrchestratorConfig holds the full configuration for home automation.
type OrchestratorConfig struct {
	// Home Assistant
	HAEnabled bool
	HA        HAConfig

	// MQTT / Zigbee2MQTT
	MQTTEnabled bool
	MQTT        MQTTConfig
	Zigbee      Zigbee2MQTTConfig

	// Cameras
	Cameras []CameraConfig

	// Presence detection
	People map[string]string // name → IP/MAC

	// Snapshot storage
	SnapshotDir string

	// APEX communication (for sending alerts back)
	APEXWebhookURL string
	APEXAuthToken  string
}

// AutomationRule defines an if/then/when automation.
type AutomationRule struct {
	ID          string
	Name        string
	Trigger     RuleTrigger
	Conditions  []RuleCondition
	Actions     []RuleAction
	Enabled     bool
	LastFired   time.Time
	CooldownSec int // minimum seconds between firings
}

// RuleTrigger defines what starts an automation.
type RuleTrigger struct {
	Type     string // "time", "presence_arrive", "presence_leave", "state_change", "sensor_threshold"
	Value    string // cron expression, person name, entity ID, etc.
	Operator string // ">", "<", "==", "!="
	Threshold float64
}

// RuleCondition defines a condition that must be true for the rule to fire.
type RuleCondition struct {
	Type     string // "time_range", "presence", "state"
	Value    string
	Operator string
	Threshold float64
}

// RuleAction defines what happens when a rule fires.
type RuleAction struct {
	Type     string // "turn_on", "turn_off", "set_temperature", "capture_snapshot", "notify_telegram", "notify_apex"
	Device   string
	Value    interface{}
	Message  string
}

// HomeOrchestrator manages all home automation.
type HomeOrchestrator struct {
	config    OrchestratorConfig
	ha        *HomeAssistantClient
	zigbee    *Zigbee2MQTTClient
	cameras   *CameraClient
	energy    *EnergyMonitor
	presence  *PresenceDetector
	rules     map[string]*AutomationRule
	rulesMu   sync.RWMutex
	mqtt      *MQTTClient
	connected bool
	done      chan struct{}
	alertCh   chan Alert
}

// Alert represents a security or automation alert to be sent to APEX/MIL-SPEC.
type Alert struct {
	Level     string    // "info", "warning", "critical"
	Source    string    // "camera", "presence", "sensor", "energy"
	Message   string
	Data      map[string]interface{}
	Timestamp time.Time
	FilePath  string // for snapshot alerts
}

// NewHomeOrchestrator creates a new home automation orchestrator.
func NewHomeOrchestrator(cfg OrchestratorConfig) *HomeOrchestrator {
	if cfg.SnapshotDir == "" {
		cfg.SnapshotDir = "/tmp/picoclaw-snapshots"
	}

	o := &HomeOrchestrator{
		config:  cfg,
		cameras: NewCameraClient(cfg.SnapshotDir),
		rules:   make(map[string]*AutomationRule),
		done:    make(chan struct{}),
		alertCh: make(chan Alert, 100),
	}

	// Register cameras
	for _, cam := range cfg.Cameras {
		o.cameras.AddCamera(cam)
	}

	// Initialize Home Assistant
	if cfg.HAEnabled {
		o.ha = NewHomeAssistantClient(cfg.HA)
	}

	// Initialize MQTT / Zigbee
	if cfg.MQTTEnabled {
		o.mqtt = NewMQTTClient(cfg.MQTT)
		o.zigbee = NewZigbee2MQTTClient(cfg.Zigbee)
	}

	// Initialize presence detection
	if len(cfg.People) > 0 {
		o.presence = NewPresenceDetector(30 * time.Second)
		for name, ip := range cfg.People {
			o.presence.AddPerson(name, ip)
		}
		o.presence.OnArrive(o.onPersonArrived)
		o.presence.OnLeave(o.onPersonLeft)
	}

	return o
}

// Connect initializes all connections.
func (o *HomeOrchestrator) Connect(ctx context.Context) error {
	var errs []string

	if o.ha != nil {
		if err := o.ha.Ping(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("Home Assistant: %v", err))
		} else {
			// Pre-load entity cache
			o.ha.GetAllEntities(ctx)
		}
	}

	if o.mqtt != nil {
		if err := o.mqtt.Connect(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("MQTT: %v", err))
		} else {
			if o.zigbee != nil {
				o.energy = NewEnergyMonitor(o.mqtt, o.config.Zigbee.BaseTopic)
			}
		}
	}

	if o.zigbee != nil {
		if err := o.zigbee.Connect(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("Zigbee2MQTT: %v", err))
		}
	}

	if o.presence != nil {
		o.presence.Start()
	}

	// Start alert dispatcher
	go o.dispatchAlerts()

	o.connected = true

	if len(errs) > 0 {
		return fmt.Errorf("partial connection errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Disconnect closes all connections gracefully.
func (o *HomeOrchestrator) Disconnect() {
	close(o.done)
	if o.mqtt != nil {
		o.mqtt.Disconnect()
	}
	if o.zigbee != nil {
		o.zigbee.Disconnect()
	}
	if o.presence != nil {
		o.presence.Stop()
	}
}

// ─── APEX Command Interface ───────────────────────────────────────────────────

// APEXCommand is the structure APEX sends to control home automation.
type APEXCommand struct {
	SessionID string          `json:"session_id"`
	Command   string          `json:"command"`   // natural language command
	Intent    string          `json:"intent"`    // parsed intent from APEX
	Devices   []string        `json:"devices"`
	Value     interface{}     `json:"value"`
	Schedule  string          `json:"schedule"`
}

// APEXResponse is the structure returned to APEX after executing a command.
type APEXResponse struct {
	Success   bool              `json:"success"`
	Message   string            `json:"message"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Snapshots []string          `json:"snapshots,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// ExecuteAPEXCommand processes a command from APEX and returns a response.
// This is the main integration point between PicoClaw and APEX.
func (o *HomeOrchestrator) ExecuteAPEXCommand(ctx context.Context, cmd APEXCommand) APEXResponse {
	resp := APEXResponse{Timestamp: time.Now()}

	// Route to appropriate handler based on intent
	switch cmd.Intent {

	// ── Light & Switch Control ──────────────────────────────────────────────
	case "turn_on", "ligar":
		result, err := o.controlDevices(ctx, cmd.Devices, "turn_on", cmd.Value)
		resp.Success = err == nil
		resp.Message = result
		if err != nil {
			resp.Message = err.Error()
		}

	case "turn_off", "desligar":
		result, err := o.controlDevices(ctx, cmd.Devices, "turn_off", nil)
		resp.Success = err == nil
		resp.Message = result
		if err != nil {
			resp.Message = err.Error()
		}

	// ── Climate Control ─────────────────────────────────────────────────────
	case "set_temperature", "temperatura":
		result, err := o.controlDevices(ctx, cmd.Devices, "set_temperature", cmd.Value)
		resp.Success = err == nil
		resp.Message = result
		if err != nil {
			resp.Message = err.Error()
		}

	// ── Scene Activation ────────────────────────────────────────────────────
	case "activate_scene", "cena", "modo":
		for _, scene := range cmd.Devices {
			if o.ha != nil {
				if err := o.ha.ActivateScene(ctx, scene); err != nil {
					resp.Message += fmt.Sprintf("❌ Cena '%s': %v\n", scene, err)
				} else {
					resp.Message += fmt.Sprintf("✅ Cena '%s' ativada\n", scene)
					resp.Success = true
				}
			}
		}

	// ── Status Queries ──────────────────────────────────────────────────────
	case "get_status", "status", "estado":
		var sb strings.Builder
		if o.ha != nil {
			summary, err := o.ha.GetAllDeviceStates(ctx)
			if err == nil {
				sb.WriteString(summary)
			}
		}
		if o.zigbee != nil {
			sb.WriteString("\n")
			sb.WriteString(o.zigbee.GetDeviceSummary())
		}
		if o.presence != nil {
			sb.WriteString("\n")
			sb.WriteString(o.presence.GetPresenceSummary())
		}
		if o.energy != nil {
			sb.WriteString("\n")
			sb.WriteString(o.energy.GetEnergySummary())
		}
		resp.Success = true
		resp.Message = sb.String()

	// ── Camera Control ──────────────────────────────────────────────────────
	case "capture_snapshot", "foto", "câmera":
		var snapPaths []string
		if len(cmd.Devices) == 0 {
			// Capture all cameras
			snaps, _ := o.cameras.CaptureAllSnapshots(ctx)
			for _, s := range snaps {
				snapPaths = append(snapPaths, s.FilePath)
			}
		} else {
			for _, cam := range cmd.Devices {
				snap, err := o.cameras.CaptureSnapshot(ctx, cam)
				if err != nil {
					resp.Message += fmt.Sprintf("❌ Câmera '%s': %v\n", cam, err)
				} else {
					snapPaths = append(snapPaths, snap.FilePath)
					resp.Message += fmt.Sprintf("📸 Foto capturada: %s\n", snap.CameraName)
				}
			}
		}
		resp.Success = len(snapPaths) > 0
		resp.Snapshots = snapPaths

	// ── Presence ────────────────────────────────────────────────────────────
	case "who_is_home", "quem_em_casa", "presença":
		if o.presence != nil {
			resp.Message = o.presence.GetPresenceSummary()
			resp.Success = true
		} else {
			resp.Message = "Detecção de presença não configurada."
		}

	// ── Energy ──────────────────────────────────────────────────────────────
	case "energy", "energia", "consumo":
		if o.energy != nil {
			resp.Message = o.energy.GetEnergySummary()
			resp.Success = true
		} else {
			resp.Message = "Monitor de energia não configurado."
		}

	// ── Automation Rules ────────────────────────────────────────────────────
	case "create_rule", "criar_automação":
		rule := o.parseRuleFromCommand(cmd)
		o.AddRule(rule)
		resp.Success = true
		resp.Message = fmt.Sprintf("✅ Automação '%s' criada com sucesso!", rule.Name)

	// ── Security Mode ───────────────────────────────────────────────────────
	case "security_mode", "modo_segurança", "alarme":
		result := o.activateSecurityMode(ctx)
		resp.Success = true
		resp.Message = result

	// ── Cinema/Night/Away Modes ─────────────────────────────────────────────
	case "cinema_mode", "modo_cinema":
		result := o.activateCinemaMode(ctx)
		resp.Success = true
		resp.Message = result

	case "good_night", "boa_noite":
		result := o.activateGoodNightMode(ctx)
		resp.Success = true
		resp.Message = result

	case "away_mode", "saindo", "modo_ausente":
		result := o.activateAwayMode(ctx)
		resp.Success = true
		resp.Message = result

	default:
		// Try to execute as a generic Home Assistant command
		if o.ha != nil {
			haCmd := DeviceCommand{
				Intent:  cmd.Intent,
				Devices: cmd.Devices,
				Value:   cmd.Value,
			}
			result, err := o.ha.ExecuteNaturalCommand(ctx, haCmd)
			resp.Success = err == nil
			resp.Message = result
			if err != nil {
				resp.Message = fmt.Sprintf("Comando não reconhecido: %s. Erro: %v", cmd.Intent, err)
			}
		} else {
			resp.Message = fmt.Sprintf("Comando '%s' não reconhecido e Home Assistant não está configurado.", cmd.Intent)
		}
	}

	return resp
}

// ─── Preset Modes ─────────────────────────────────────────────────────────────

func (o *HomeOrchestrator) activateSecurityMode(ctx context.Context) string {
	var actions []string

	// Capture snapshot from all cameras
	snaps, _ := o.cameras.CaptureAllSnapshots(ctx)
	for _, s := range snaps {
		actions = append(actions, fmt.Sprintf("📸 Foto capturada: %s", s.CameraName))
		// Send alert to APEX/MIL-SPEC
		o.alertCh <- Alert{
			Level:    "warning",
			Source:   "camera",
			Message:  fmt.Sprintf("Modo segurança ativado - foto capturada: %s", s.CameraName),
			FilePath: s.FilePath,
			Timestamp: time.Now(),
		}
	}

	// Turn on all lights (deter intruders)
	if o.ha != nil {
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "light",
			Service:  "turn_on",
			EntityID: "all",
			Data:     map[string]interface{}{"brightness": 255},
		})
		actions = append(actions, "💡 Todas as luzes acesas")

		// Activate alarm if available
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "alarm_control_panel",
			Service:  "alarm_arm_away",
			EntityID: "alarm_control_panel.home",
		})
		actions = append(actions, "🚨 Alarme ativado")
	}

	return "🛡️ MODO SEGURANÇA ATIVADO:\n" + strings.Join(actions, "\n")
}

func (o *HomeOrchestrator) activateCinemaMode(ctx context.Context) string {
	var actions []string
	if o.ha != nil {
		// Dim lights to 10%
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "light",
			Service:  "turn_on",
			EntityID: "all",
			Data:     map[string]interface{}{"brightness": 25},
		})
		actions = append(actions, "💡 Luzes reduzidas a 10%")

		// Close covers/blinds
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "cover",
			Service:  "close_cover",
			EntityID: "all",
		})
		actions = append(actions, "🪟 Cortinas fechadas")

		// Turn on TV
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "media_player",
			Service:  "turn_on",
			EntityID: "media_player.tv",
		})
		actions = append(actions, "📺 TV ligada")
	}
	return "🎬 MODO CINEMA ATIVADO:\n" + strings.Join(actions, "\n")
}

func (o *HomeOrchestrator) activateGoodNightMode(ctx context.Context) string {
	var actions []string
	if o.ha != nil {
		// Turn off all lights
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "light",
			Service:  "turn_off",
			EntityID: "all",
		})
		actions = append(actions, "💡 Todas as luzes apagadas")

		// Turn off all non-essential switches
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "switch",
			Service:  "turn_off",
			EntityID: "all",
		})
		actions = append(actions, "🔌 Tomadas desligadas")

		// Lock doors
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "lock",
			Service:  "lock",
			EntityID: "all",
		})
		actions = append(actions, "🔒 Portas trancadas")

		// Set climate to sleep mode
		o.ha.CallService(ctx, HAServiceCall{
			Domain:   "climate",
			Service:  "set_temperature",
			EntityID: "all",
			Data:     map[string]interface{}{"temperature": 22},
		})
		actions = append(actions, "🌡️ Temperatura configurada para 22°C")
	}
	return "🌙 BOA NOITE! MODO DORMIR ATIVADO:\n" + strings.Join(actions, "\n")
}

func (o *HomeOrchestrator) activateAwayMode(ctx context.Context) string {
	var actions []string
	if o.ha != nil {
		// Turn off everything
		o.ha.CallService(ctx, HAServiceCall{Domain: "light", Service: "turn_off", EntityID: "all"})
		actions = append(actions, "💡 Luzes apagadas")

		o.ha.CallService(ctx, HAServiceCall{Domain: "switch", Service: "turn_off", EntityID: "all"})
		actions = append(actions, "🔌 Tomadas desligadas")

		o.ha.CallService(ctx, HAServiceCall{Domain: "lock", Service: "lock", EntityID: "all"})
		actions = append(actions, "🔒 Portas trancadas")

		o.ha.CallService(ctx, HAServiceCall{Domain: "alarm_control_panel", Service: "alarm_arm_away", EntityID: "alarm_control_panel.home"})
		actions = append(actions, "🚨 Alarme ativado")
	}

	// Capture snapshot before leaving
	snaps, _ := o.cameras.CaptureAllSnapshots(ctx)
	for _, s := range snaps {
		actions = append(actions, fmt.Sprintf("📸 Foto registrada: %s", s.CameraName))
	}

	return "🚗 MODO AUSENTE ATIVADO:\n" + strings.Join(actions, "\n")
}

// ─── Device Control Helper ────────────────────────────────────────────────────

func (o *HomeOrchestrator) controlDevices(ctx context.Context, devices []string, action string, value interface{}) (string, error) {
	var results []string
	var lastErr error

	for _, device := range devices {
		var err error

		// Try Home Assistant first
		if o.ha != nil {
			haCmd := DeviceCommand{
				Intent:  action,
				Devices: []string{device},
				Value:   value,
			}
			result, haErr := o.ha.ExecuteNaturalCommand(ctx, haCmd)
			if haErr == nil {
				results = append(results, result)
				continue
			}
			err = haErr
		}

		// Try Zigbee2MQTT
		if o.zigbee != nil {
			switch action {
			case "turn_on":
				err = o.zigbee.TurnOn(device)
				if err == nil {
					results = append(results, fmt.Sprintf("✅ %s ligado (Zigbee)", device))
					continue
				}
			case "turn_off":
				err = o.zigbee.TurnOff(device)
				if err == nil {
					results = append(results, fmt.Sprintf("✅ %s desligado (Zigbee)", device))
					continue
				}
			}
		}

		if err != nil {
			results = append(results, fmt.Sprintf("❌ %s: %v", device, err))
			lastErr = err
		}
	}

	return strings.Join(results, "\n"), lastErr
}

// ─── Automation Rules ─────────────────────────────────────────────────────────

// AddRule adds an automation rule.
func (o *HomeOrchestrator) AddRule(rule *AutomationRule) {
	o.rulesMu.Lock()
	o.rules[rule.ID] = rule
	o.rulesMu.Unlock()
}

// RemoveRule removes an automation rule.
func (o *HomeOrchestrator) RemoveRule(id string) {
	o.rulesMu.Lock()
	delete(o.rules, id)
	o.rulesMu.Unlock()
}

func (o *HomeOrchestrator) parseRuleFromCommand(cmd APEXCommand) *AutomationRule {
	rule := &AutomationRule{
		ID:      fmt.Sprintf("rule_%d", time.Now().UnixNano()),
		Name:    cmd.Command,
		Enabled: true,
	}
	if cmd.Schedule != "" {
		rule.Trigger = RuleTrigger{
			Type:  "time",
			Value: cmd.Schedule,
		}
	}
	for _, device := range cmd.Devices {
		rule.Actions = append(rule.Actions, RuleAction{
			Type:   cmd.Intent,
			Device: device,
			Value:  cmd.Value,
		})
	}
	return rule
}

// ─── Presence Callbacks ───────────────────────────────────────────────────────

func (o *HomeOrchestrator) onPersonArrived(name string) {
	o.alertCh <- Alert{
		Level:     "info",
		Source:    "presence",
		Message:   fmt.Sprintf("🏠 %s chegou em casa", name),
		Timestamp: time.Now(),
		Data:      map[string]interface{}{"person": name, "event": "arrived"},
	}
}

func (o *HomeOrchestrator) onPersonLeft(name string) {
	o.alertCh <- Alert{
		Level:     "info",
		Source:    "presence",
		Message:   fmt.Sprintf("🚗 %s saiu de casa", name),
		Timestamp: time.Now(),
		Data:      map[string]interface{}{"person": name, "event": "left"},
	}
}

// ─── Alert Dispatcher (sends to APEX/MIL-SPEC) ───────────────────────────────

func (o *HomeOrchestrator) dispatchAlerts() {
	for {
		select {
		case <-o.done:
			return
		case alert := <-o.alertCh:
			o.sendAlertToAPEX(alert)
		}
	}
}

func (o *HomeOrchestrator) sendAlertToAPEX(alert Alert) {
	if o.config.APEXWebhookURL == "" {
		return
	}

	payload, err := json.Marshal(map[string]interface{}{
		"source":    "picoclaw_homeauto",
		"level":     alert.Level,
		"component": alert.Source,
		"message":   alert.Message,
		"data":      alert.Data,
		"file":      alert.FilePath,
		"timestamp": alert.Timestamp,
	})
	if err != nil {
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("POST", o.config.APEXWebhookURL+"/api/homeauto/alert", strings.NewReader(string(payload)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-APEX-Token", o.config.APEXAuthToken)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// ─── Status Report ────────────────────────────────────────────────────────────

// GetSystemStatus returns a complete status report of all home automation systems.
func (o *HomeOrchestrator) GetSystemStatus() string {
	var sb strings.Builder
	sb.WriteString("🏠 SYNAPSE HOME AUTOMATION — STATUS\n")
	sb.WriteString(strings.Repeat("─", 40) + "\n\n")

	// Connection status
	sb.WriteString("🔌 Conexões:\n")
	if o.ha != nil {
		sb.WriteString("  ✅ Home Assistant: conectado\n")
	} else {
		sb.WriteString("  ⚫ Home Assistant: não configurado\n")
	}
	if o.mqtt != nil {
		sb.WriteString("  ✅ MQTT Broker: conectado\n")
	} else {
		sb.WriteString("  ⚫ MQTT: não configurado\n")
	}
	if o.zigbee != nil {
		devices := o.zigbee.GetDevices()
		sb.WriteString(fmt.Sprintf("  ✅ Zigbee2MQTT: %d dispositivos\n", len(devices)))
	}

	// Camera status
	sb.WriteString(fmt.Sprintf("\n📷 Câmeras: %d registradas\n", len(o.config.Cameras)))

	// Rules
	o.rulesMu.RLock()
	activeRules := 0
	for _, r := range o.rules {
		if r.Enabled {
			activeRules++
		}
	}
	o.rulesMu.RUnlock()
	sb.WriteString(fmt.Sprintf("\n⚡ Automações: %d ativas\n", activeRules))

	// Presence
	if o.presence != nil {
		sb.WriteString("\n")
		sb.WriteString(o.presence.GetPresenceSummary())
	}

	return sb.String()
}
