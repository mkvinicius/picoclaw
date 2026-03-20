// Package homeautomation provides home automation integration for PicoClaw.
// It connects to Home Assistant, MQTT brokers, IP cameras and smart devices,
// allowing the APEX agent to control the physical world via natural language.
package homeautomation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// HAConfig holds the connection parameters for Home Assistant.
type HAConfig struct {
	BaseURL     string        // e.g. "http://homeassistant.local:8123"
	Token       string        // Long-lived access token
	Timeout     time.Duration // HTTP timeout (default 10s)
	VerifyTLS   bool          // Whether to verify TLS certificates
}

// HAEntity represents a Home Assistant entity (device/sensor).
type HAEntity struct {
	EntityID    string                 `json:"entity_id"`
	State       string                 `json:"state"`
	Attributes  map[string]interface{} `json:"attributes"`
	LastChanged time.Time              `json:"last_changed"`
	LastUpdated time.Time              `json:"last_updated"`
	FriendlyName string               `json:"-"` // extracted from attributes
}

// HAServiceCall represents a call to a Home Assistant service.
type HAServiceCall struct {
	Domain    string                 // e.g. "light", "switch", "climate"
	Service   string                 // e.g. "turn_on", "turn_off", "set_temperature"
	EntityID  string                 // target entity
	Data      map[string]interface{} // additional service data
}

// HAAutomation represents a Home Assistant automation rule.
type HAAutomation struct {
	ID          string
	Alias       string
	Description string
	Trigger     []map[string]interface{}
	Condition   []map[string]interface{}
	Action      []map[string]interface{}
}

// DeviceCommand is a high-level natural language command parsed by APEX.
type DeviceCommand struct {
	Intent    string            // "turn_on", "turn_off", "set_value", "get_state", "create_automation"
	Devices   []string          // friendly names or entity IDs
	Value     interface{}       // temperature, brightness, color, etc.
	Schedule  string            // cron expression or "when X" condition
	Condition string            // "if temperature > 28"
}

// ─────────────────────────────────────────────────────────────────────────────
// HomeAssistantClient
// ─────────────────────────────────────────────────────────────────────────────

// HomeAssistantClient manages communication with a Home Assistant instance.
type HomeAssistantClient struct {
	config     HAConfig
	httpClient *http.Client
	entityCache map[string]*HAEntity
	cacheMu    sync.RWMutex
	cacheTime  time.Time
	cacheTTL   time.Duration
}

// NewHomeAssistantClient creates a new Home Assistant client.
func NewHomeAssistantClient(cfg HAConfig) *HomeAssistantClient {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &HomeAssistantClient{
		config:      cfg,
		httpClient:  &http.Client{Timeout: cfg.Timeout},
		entityCache: make(map[string]*HAEntity),
		cacheTTL:    30 * time.Second,
	}
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func (c *HomeAssistantClient) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	url := strings.TrimRight(c.config.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.config.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HA API error %d: %s", resp.StatusCode, string(respData))
	}

	return respData, nil
}

// ─── Core API Methods ─────────────────────────────────────────────────────────

// Ping checks if Home Assistant is reachable and the token is valid.
func (c *HomeAssistantClient) Ping(ctx context.Context) error {
	_, err := c.doRequest(ctx, "GET", "/api/", nil)
	return err
}

// GetAllEntities returns all entities from Home Assistant.
func (c *HomeAssistantClient) GetAllEntities(ctx context.Context) ([]*HAEntity, error) {
	data, err := c.doRequest(ctx, "GET", "/api/states", nil)
	if err != nil {
		return nil, err
	}

	var entities []*HAEntity
	if err := json.Unmarshal(data, &entities); err != nil {
		return nil, fmt.Errorf("parse entities: %w", err)
	}

	// Extract friendly names and update cache
	c.cacheMu.Lock()
	for _, e := range entities {
		if name, ok := e.Attributes["friendly_name"].(string); ok {
			e.FriendlyName = name
		}
		c.entityCache[e.EntityID] = e
		if e.FriendlyName != "" {
			c.entityCache[strings.ToLower(e.FriendlyName)] = e
		}
	}
	c.cacheTime = time.Now()
	c.cacheMu.Unlock()

	return entities, nil
}

// GetEntity returns the state of a single entity.
func (c *HomeAssistantClient) GetEntity(ctx context.Context, entityID string) (*HAEntity, error) {
	// Check cache first
	c.cacheMu.RLock()
	if e, ok := c.entityCache[entityID]; ok && time.Since(c.cacheTime) < c.cacheTTL {
		c.cacheMu.RUnlock()
		return e, nil
	}
	c.cacheMu.RUnlock()

	data, err := c.doRequest(ctx, "GET", "/api/states/"+entityID, nil)
	if err != nil {
		return nil, err
	}

	var entity HAEntity
	if err := json.Unmarshal(data, &entity); err != nil {
		return nil, fmt.Errorf("parse entity: %w", err)
	}
	if name, ok := entity.Attributes["friendly_name"].(string); ok {
		entity.FriendlyName = name
	}

	c.cacheMu.Lock()
	c.entityCache[entity.EntityID] = &entity
	c.cacheMu.Unlock()

	return &entity, nil
}

// CallService calls a Home Assistant service (e.g., turn on a light).
func (c *HomeAssistantClient) CallService(ctx context.Context, call HAServiceCall) error {
	payload := map[string]interface{}{
		"entity_id": call.EntityID,
	}
	for k, v := range call.Data {
		payload[k] = v
	}

	path := fmt.Sprintf("/api/services/%s/%s", call.Domain, call.Service)
	_, err := c.doRequest(ctx, "POST", path, payload)
	return err
}

// ─── High-Level Device Control ────────────────────────────────────────────────

// TurnOn turns on a device by entity ID or friendly name.
func (c *HomeAssistantClient) TurnOn(ctx context.Context, deviceName string, options map[string]interface{}) error {
	entityID, domain, err := c.resolveDevice(ctx, deviceName)
	if err != nil {
		return err
	}
	return c.CallService(ctx, HAServiceCall{
		Domain:   domain,
		Service:  "turn_on",
		EntityID: entityID,
		Data:     options,
	})
}

// TurnOff turns off a device by entity ID or friendly name.
func (c *HomeAssistantClient) TurnOff(ctx context.Context, deviceName string) error {
	entityID, domain, err := c.resolveDevice(ctx, deviceName)
	if err != nil {
		return err
	}
	return c.CallService(ctx, HAServiceCall{
		Domain:   domain,
		Service:  "turn_off",
		EntityID: entityID,
	})
}

// SetTemperature sets the target temperature for a climate device.
func (c *HomeAssistantClient) SetTemperature(ctx context.Context, deviceName string, temp float64) error {
	entityID, _, err := c.resolveDevice(ctx, deviceName)
	if err != nil {
		return err
	}
	return c.CallService(ctx, HAServiceCall{
		Domain:   "climate",
		Service:  "set_temperature",
		EntityID: entityID,
		Data:     map[string]interface{}{"temperature": temp},
	})
}

// SetBrightness sets the brightness of a light (0-255).
func (c *HomeAssistantClient) SetBrightness(ctx context.Context, deviceName string, brightness int) error {
	entityID, _, err := c.resolveDevice(ctx, deviceName)
	if err != nil {
		return err
	}
	return c.CallService(ctx, HAServiceCall{
		Domain:   "light",
		Service:  "turn_on",
		EntityID: entityID,
		Data:     map[string]interface{}{"brightness": brightness},
	})
}

// GetTemperature reads the current temperature from a sensor.
func (c *HomeAssistantClient) GetTemperature(ctx context.Context, sensorName string) (float64, string, error) {
	entityID, _, err := c.resolveDevice(ctx, sensorName)
	if err != nil {
		return 0, "", err
	}

	entity, err := c.GetEntity(ctx, entityID)
	if err != nil {
		return 0, "", err
	}

	var temp float64
	if _, err := fmt.Sscanf(entity.State, "%f", &temp); err != nil {
		return 0, "", fmt.Errorf("could not parse temperature: %s", entity.State)
	}

	unit := "°C"
	if u, ok := entity.Attributes["unit_of_measurement"].(string); ok {
		unit = u
	}

	return temp, unit, nil
}

// GetAllDeviceStates returns a human-readable summary of all devices.
func (c *HomeAssistantClient) GetAllDeviceStates(ctx context.Context) (string, error) {
	entities, err := c.GetAllEntities(ctx)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("📊 Estado atual dos dispositivos:\n\n")

	domains := map[string][]*HAEntity{}
	for _, e := range entities {
		parts := strings.SplitN(e.EntityID, ".", 2)
		if len(parts) == 2 {
			domains[parts[0]] = append(domains[parts[0]], e)
		}
	}

	domainEmojis := map[string]string{
		"light":        "💡",
		"switch":       "🔌",
		"climate":      "🌡️",
		"sensor":       "📡",
		"camera":       "📷",
		"lock":         "🔒",
		"cover":        "🪟",
		"media_player": "📺",
		"alarm_control_panel": "🚨",
	}

	for domain, ents := range domains {
		emoji := domainEmojis[domain]
		if emoji == "" {
			emoji = "⚙️"
		}
		sb.WriteString(fmt.Sprintf("%s %s:\n", emoji, strings.ToUpper(domain)))
		for _, e := range ents {
			name := e.FriendlyName
			if name == "" {
				name = e.EntityID
			}
			sb.WriteString(fmt.Sprintf("  • %s: %s\n", name, e.State))
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// ─── Scene & Automation ───────────────────────────────────────────────────────

// ActivateScene activates a Home Assistant scene (e.g., "cinema", "good night").
func (c *HomeAssistantClient) ActivateScene(ctx context.Context, sceneName string) error {
	entityID, _, err := c.resolveDevice(ctx, sceneName)
	if err != nil {
		// Try with scene. prefix
		entityID = "scene." + strings.ToLower(strings.ReplaceAll(sceneName, " ", "_"))
	}
	return c.CallService(ctx, HAServiceCall{
		Domain:   "scene",
		Service:  "turn_on",
		EntityID: entityID,
	})
}

// RunScript runs a Home Assistant script.
func (c *HomeAssistantClient) RunScript(ctx context.Context, scriptName string) error {
	entityID := "script." + strings.ToLower(strings.ReplaceAll(scriptName, " ", "_"))
	return c.CallService(ctx, HAServiceCall{
		Domain:   "script",
		Service:  "turn_on",
		EntityID: entityID,
	})
}

// ─── Natural Language Command Executor ───────────────────────────────────────

// ExecuteNaturalCommand executes a parsed DeviceCommand from APEX.
// This is the main entry point for APEX-driven home automation.
func (c *HomeAssistantClient) ExecuteNaturalCommand(ctx context.Context, cmd DeviceCommand) (string, error) {
	var results []string

	for _, device := range cmd.Devices {
		var err error
		var result string

		switch cmd.Intent {
		case "turn_on":
			options := map[string]interface{}{}
			if cmd.Value != nil {
				if brightness, ok := cmd.Value.(float64); ok {
					options["brightness"] = int(brightness * 255 / 100)
				}
			}
			err = c.TurnOn(ctx, device, options)
			if err == nil {
				result = fmt.Sprintf("✅ %s ligado", device)
			}

		case "turn_off":
			err = c.TurnOff(ctx, device)
			if err == nil {
				result = fmt.Sprintf("✅ %s desligado", device)
			}

		case "set_temperature":
			if temp, ok := cmd.Value.(float64); ok {
				err = c.SetTemperature(ctx, device, temp)
				if err == nil {
					result = fmt.Sprintf("✅ %s configurado para %.1f°C", device, temp)
				}
			} else {
				err = fmt.Errorf("temperatura inválida: %v", cmd.Value)
			}

		case "set_brightness":
			if brightness, ok := cmd.Value.(float64); ok {
				err = c.SetBrightness(ctx, device, int(brightness*255/100))
				if err == nil {
					result = fmt.Sprintf("✅ Brilho de %s configurado para %.0f%%", device, brightness)
				}
			}

		case "get_state":
			entity, getErr := c.GetEntity(ctx, device)
			if getErr != nil {
				// Try resolving by name
				entityID, _, resolveErr := c.resolveDevice(ctx, device)
				if resolveErr != nil {
					err = getErr
					break
				}
				entity, err = c.GetEntity(ctx, entityID)
				if err != nil {
					break
				}
			}
			name := entity.FriendlyName
			if name == "" {
				name = entity.EntityID
			}
			result = fmt.Sprintf("📊 %s está: **%s**", name, entity.State)

		case "activate_scene":
			err = c.ActivateScene(ctx, device)
			if err == nil {
				result = fmt.Sprintf("✅ Cena '%s' ativada", device)
			}

		case "get_all_states":
			summary, summaryErr := c.GetAllDeviceStates(ctx)
			if summaryErr != nil {
				err = summaryErr
			} else {
				result = summary
			}

		default:
			err = fmt.Errorf("comando não reconhecido: %s", cmd.Intent)
		}

		if err != nil {
			results = append(results, fmt.Sprintf("❌ Erro em '%s': %v", device, err))
		} else if result != "" {
			results = append(results, result)
		}
	}

	return strings.Join(results, "\n"), nil
}

// ─── Device Resolution ────────────────────────────────────────────────────────

// resolveDevice finds the entity ID and domain for a device name.
// It supports friendly names, partial matches and direct entity IDs.
func (c *HomeAssistantClient) resolveDevice(ctx context.Context, name string) (entityID, domain string, err error) {
	// Direct entity ID (contains a dot)
	if strings.Contains(name, ".") {
		parts := strings.SplitN(name, ".", 2)
		return name, parts[0], nil
	}

	// Refresh cache if stale
	if time.Since(c.cacheTime) > c.cacheTTL {
		if _, refreshErr := c.GetAllEntities(ctx); refreshErr != nil {
			// Use stale cache if refresh fails
		}
	}

	// Exact match in cache (case-insensitive)
	nameLower := strings.ToLower(name)
	c.cacheMu.RLock()
	defer c.cacheMu.RUnlock()

	if e, ok := c.entityCache[nameLower]; ok {
		parts := strings.SplitN(e.EntityID, ".", 2)
		return e.EntityID, parts[0], nil
	}

	// Partial match
	for key, e := range c.entityCache {
		if strings.Contains(key, nameLower) || strings.Contains(nameLower, key) {
			parts := strings.SplitN(e.EntityID, ".", 2)
			return e.EntityID, parts[0], nil
		}
	}

	return "", "", fmt.Errorf("dispositivo não encontrado: '%s'", name)
}
