package homeautomation

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// MQTT Client (pure stdlib, no external dependency)
// ─────────────────────────────────────────────────────────────────────────────
// Implements a minimal MQTT 3.1.1 client sufficient for home automation.
// Uses only Go stdlib (net package) to keep the binary small.

// MQTTConfig holds MQTT broker connection parameters.
type MQTTConfig struct {
	BrokerHost string        // e.g. "192.168.1.100"
	BrokerPort int           // default 1883
	ClientID   string        // unique client identifier
	Username   string        // optional
	Password   string        // optional
	KeepAlive  time.Duration // default 60s
	Timeout    time.Duration // connection timeout
}

// MQTTMessage represents a received MQTT message.
type MQTTMessage struct {
	Topic   string
	Payload []byte
	QoS     byte
	Retain  bool
}

// MQTTHandler is a callback function for received messages.
type MQTTHandler func(msg MQTTMessage)

// MQTTClient is a minimal MQTT 3.1.1 client.
type MQTTClient struct {
	config      MQTTConfig
	conn        net.Conn
	mu          sync.Mutex
	subscriptions map[string]MQTTHandler
	packetID    uint16
	connected   bool
	done        chan struct{}
}

// NewMQTTClient creates a new MQTT client.
func NewMQTTClient(cfg MQTTConfig) *MQTTClient {
	if cfg.BrokerPort == 0 {
		cfg.BrokerPort = 1883
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 60 * time.Second
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "picoclaw-homeauto"
	}
	return &MQTTClient{
		config:        cfg,
		subscriptions: make(map[string]MQTTHandler),
		done:          make(chan struct{}),
	}
}

// Connect establishes a connection to the MQTT broker.
func (c *MQTTClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	addr := fmt.Sprintf("%s:%d", c.config.BrokerHost, c.config.BrokerPort)
	conn, err := net.DialTimeout("tcp", addr, c.config.Timeout)
	if err != nil {
		return fmt.Errorf("MQTT connect to %s: %w", addr, err)
	}
	c.conn = conn

	// Send CONNECT packet
	if err := c.sendConnect(); err != nil {
		conn.Close()
		return fmt.Errorf("MQTT CONNECT packet: %w", err)
	}

	// Read CONNACK
	if err := c.readConnack(); err != nil {
		conn.Close()
		return fmt.Errorf("MQTT CONNACK: %w", err)
	}

	c.connected = true

	// Start read loop
	go c.readLoop()

	// Start keepalive
	go c.keepAliveLoop()

	return nil
}

// Disconnect closes the MQTT connection gracefully.
func (c *MQTTClient) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected {
		close(c.done)
		c.sendDisconnect()
		c.conn.Close()
		c.connected = false
	}
}

// Publish sends a message to an MQTT topic.
func (c *MQTTClient) Publish(topic string, payload []byte, qos byte, retain bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return fmt.Errorf("MQTT not connected")
	}
	return c.sendPublish(topic, payload, qos, retain)
}

// Subscribe registers a handler for messages on a topic pattern.
func (c *MQTTClient) Subscribe(topic string, qos byte, handler MQTTHandler) error {
	c.mu.Lock()
	c.subscriptions[topic] = handler
	c.mu.Unlock()
	if !c.connected {
		return fmt.Errorf("MQTT not connected")
	}
	return c.sendSubscribe(topic, qos)
}

// ─── MQTT Packet Builders (MQTT 3.1.1) ───────────────────────────────────────

func encodeString(s string) []byte {
	b := []byte(s)
	return append([]byte{byte(len(b) >> 8), byte(len(b))}, b...)
}

func encodeRemainingLength(length int) []byte {
	var result []byte
	for {
		digit := length % 128
		length /= 128
		if length > 0 {
			digit |= 0x80
		}
		result = append(result, byte(digit))
		if length == 0 {
			break
		}
	}
	return result
}

func (c *MQTTClient) sendConnect() error {
	clientID := encodeString(c.config.ClientID)
	protocolName := encodeString("MQTT")
	protocolLevel := byte(4) // MQTT 3.1.1

	connectFlags := byte(0x02) // Clean session
	if c.config.Username != "" {
		connectFlags |= 0x80
	}
	if c.config.Password != "" {
		connectFlags |= 0x40
	}

	keepAlive := uint16(c.config.KeepAlive.Seconds())

	var payload []byte
	payload = append(payload, protocolName...)
	payload = append(payload, protocolLevel)
	payload = append(payload, connectFlags)
	payload = append(payload, byte(keepAlive>>8), byte(keepAlive))
	payload = append(payload, clientID...)

	if c.config.Username != "" {
		payload = append(payload, encodeString(c.config.Username)...)
	}
	if c.config.Password != "" {
		payload = append(payload, encodeString(c.config.Password)...)
	}

	packet := append([]byte{0x10}, encodeRemainingLength(len(payload))...)
	packet = append(packet, payload...)
	_, err := c.conn.Write(packet)
	return err
}

func (c *MQTTClient) readConnack() error {
	buf := make([]byte, 4)
	c.conn.SetReadDeadline(time.Now().Add(c.config.Timeout))
	defer c.conn.SetReadDeadline(time.Time{})

	if _, err := c.conn.Read(buf); err != nil {
		return err
	}
	if buf[0] != 0x20 {
		return fmt.Errorf("expected CONNACK (0x20), got 0x%02x", buf[0])
	}
	if buf[3] != 0x00 {
		codes := map[byte]string{
			0x01: "unacceptable protocol version",
			0x02: "identifier rejected",
			0x03: "server unavailable",
			0x04: "bad username or password",
			0x05: "not authorized",
		}
		reason := codes[buf[3]]
		if reason == "" {
			reason = fmt.Sprintf("code 0x%02x", buf[3])
		}
		return fmt.Errorf("CONNACK refused: %s", reason)
	}
	return nil
}

func (c *MQTTClient) sendPublish(topic string, payload []byte, qos byte, retain bool) error {
	topicBytes := encodeString(topic)
	var header byte = 0x30
	if retain {
		header |= 0x01
	}
	if qos > 0 {
		header |= (qos << 1)
	}

	var body []byte
	body = append(body, topicBytes...)
	if qos > 0 {
		c.packetID++
		body = append(body, byte(c.packetID>>8), byte(c.packetID))
	}
	body = append(body, payload...)

	packet := append([]byte{header}, encodeRemainingLength(len(body))...)
	packet = append(packet, body...)
	_, err := c.conn.Write(packet)
	return err
}

func (c *MQTTClient) sendSubscribe(topic string, qos byte) error {
	c.packetID++
	topicBytes := encodeString(topic)

	var body []byte
	body = append(body, byte(c.packetID>>8), byte(c.packetID))
	body = append(body, topicBytes...)
	body = append(body, qos)

	packet := append([]byte{0x82}, encodeRemainingLength(len(body))...)
	packet = append(packet, body...)
	_, err := c.conn.Write(packet)
	return err
}

func (c *MQTTClient) sendDisconnect() {
	c.conn.Write([]byte{0xE0, 0x00})
}

func (c *MQTTClient) sendPingReq() error {
	_, err := c.conn.Write([]byte{0xC0, 0x00})
	return err
}

func (c *MQTTClient) keepAliveLoop() {
	ticker := time.NewTicker(c.config.KeepAlive / 2)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.connected {
				c.sendPingReq()
			}
			c.mu.Unlock()
		}
	}
}

func (c *MQTTClient) readLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		header := make([]byte, 1)
		if _, err := c.conn.Read(header); err != nil {
			continue
		}

		// Read remaining length
		var remainingLength int
		multiplier := 1
		for {
			b := make([]byte, 1)
			if _, err := c.conn.Read(b); err != nil {
				break
			}
			remainingLength += int(b[0]&0x7F) * multiplier
			multiplier *= 128
			if b[0]&0x80 == 0 {
				break
			}
		}

		if remainingLength == 0 {
			continue
		}

		payload := make([]byte, remainingLength)
		if _, err := c.conn.Read(payload); err != nil {
			continue
		}

		packetType := header[0] & 0xF0
		switch packetType {
		case 0x30: // PUBLISH
			c.handlePublish(header[0], payload)
		case 0xD0: // PINGRESP
			// keepalive response, ignore
		}
	}
}

func (c *MQTTClient) handlePublish(header byte, payload []byte) {
	// Parse topic length
	if len(payload) < 2 {
		return
	}
	topicLen := int(payload[0])<<8 | int(payload[1])
	if len(payload) < 2+topicLen {
		return
	}
	topic := string(payload[2 : 2+topicLen])
	msgPayload := payload[2+topicLen:]

	// QoS 1/2: skip packet ID
	qos := (header >> 1) & 0x03
	if qos > 0 && len(msgPayload) >= 2 {
		msgPayload = msgPayload[2:]
	}

	msg := MQTTMessage{
		Topic:   topic,
		Payload: msgPayload,
		QoS:     qos,
		Retain:  header&0x01 != 0,
	}

	// Find matching handler
	c.mu.Lock()
	handlers := make(map[string]MQTTHandler)
	for pattern, h := range c.subscriptions {
		handlers[pattern] = h
	}
	c.mu.Unlock()

	for pattern, handler := range handlers {
		if mqttTopicMatch(pattern, topic) {
			go handler(msg)
		}
	}
}

// mqttTopicMatch checks if a topic matches an MQTT subscription pattern.
func mqttTopicMatch(pattern, topic string) bool {
	if pattern == topic {
		return true
	}
	if pattern == "#" {
		return true
	}
	patParts := strings.Split(pattern, "/")
	topParts := strings.Split(topic, "/")

	for i, p := range patParts {
		if p == "#" {
			return true
		}
		if i >= len(topParts) {
			return false
		}
		if p != "+" && p != topParts[i] {
			return false
		}
	}
	return len(patParts) == len(topParts)
}

// ─────────────────────────────────────────────────────────────────────────────
// Zigbee2MQTT Integration
// ─────────────────────────────────────────────────────────────────────────────

// Zigbee2MQTTConfig holds configuration for Zigbee2MQTT.
type Zigbee2MQTTConfig struct {
	MQTTConfig
	BaseTopic string // default "zigbee2mqtt"
}

// ZigbeeDevice represents a Zigbee device managed by Zigbee2MQTT.
type ZigbeeDevice struct {
	FriendlyName string                 `json:"friendly_name"`
	IEEEAddress  string                 `json:"ieee_address"`
	Type         string                 `json:"type"`
	Vendor       string                 `json:"vendor"`
	Model        string                 `json:"model"`
	State        map[string]interface{} `json:"-"`
}

// Zigbee2MQTTClient manages Zigbee devices via Zigbee2MQTT.
type Zigbee2MQTTClient struct {
	config  Zigbee2MQTTConfig
	mqtt    *MQTTClient
	devices map[string]*ZigbeeDevice
	mu      sync.RWMutex
}

// NewZigbee2MQTTClient creates a new Zigbee2MQTT client.
func NewZigbee2MQTTClient(cfg Zigbee2MQTTConfig) *Zigbee2MQTTClient {
	if cfg.BaseTopic == "" {
		cfg.BaseTopic = "zigbee2mqtt"
	}
	return &Zigbee2MQTTClient{
		config:  cfg,
		mqtt:    NewMQTTClient(cfg.MQTTConfig),
		devices: make(map[string]*ZigbeeDevice),
	}
}

// Connect connects to the MQTT broker and subscribes to Zigbee2MQTT topics.
func (z *Zigbee2MQTTClient) Connect(ctx context.Context) error {
	if err := z.mqtt.Connect(ctx); err != nil {
		return err
	}

	// Subscribe to all device state updates
	z.mqtt.Subscribe(z.config.BaseTopic+"/+", 0, z.handleDeviceState)

	// Subscribe to bridge device list
	z.mqtt.Subscribe(z.config.BaseTopic+"/bridge/devices", 0, z.handleDeviceList)

	// Request device list
	z.mqtt.Publish(z.config.BaseTopic+"/bridge/request/devices", []byte(""), 0, false)

	return nil
}

// Disconnect closes the Zigbee2MQTT connection.
func (z *Zigbee2MQTTClient) Disconnect() {
	z.mqtt.Disconnect()
}

func (z *Zigbee2MQTTClient) handleDeviceList(msg MQTTMessage) {
	var devices []*ZigbeeDevice
	if err := json.Unmarshal(msg.Payload, &devices); err != nil {
		return
	}
	z.mu.Lock()
	for _, d := range devices {
		z.devices[d.FriendlyName] = d
	}
	z.mu.Unlock()
}

func (z *Zigbee2MQTTClient) handleDeviceState(msg MQTTMessage) {
	// Extract device name from topic: zigbee2mqtt/<device_name>
	parts := strings.SplitN(msg.Topic, "/", 2)
	if len(parts) < 2 {
		return
	}
	deviceName := parts[1]

	var state map[string]interface{}
	if err := json.Unmarshal(msg.Payload, &state); err != nil {
		return
	}

	z.mu.Lock()
	if d, ok := z.devices[deviceName]; ok {
		d.State = state
	} else {
		z.devices[deviceName] = &ZigbeeDevice{
			FriendlyName: deviceName,
			State:        state,
		}
	}
	z.mu.Unlock()
}

// SetState sends a state command to a Zigbee device.
func (z *Zigbee2MQTTClient) SetState(deviceName string, state map[string]interface{}) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	topic := fmt.Sprintf("%s/%s/set", z.config.BaseTopic, deviceName)
	return z.mqtt.Publish(topic, payload, 0, false)
}

// TurnOn turns on a Zigbee device.
func (z *Zigbee2MQTTClient) TurnOn(deviceName string) error {
	return z.SetState(deviceName, map[string]interface{}{"state": "ON"})
}

// TurnOff turns off a Zigbee device.
func (z *Zigbee2MQTTClient) TurnOff(deviceName string) error {
	return z.SetState(deviceName, map[string]interface{}{"state": "OFF"})
}

// SetBrightness sets the brightness of a Zigbee light (0-254).
func (z *Zigbee2MQTTClient) SetBrightness(deviceName string, brightness int) error {
	return z.SetState(deviceName, map[string]interface{}{
		"state":      "ON",
		"brightness": brightness,
	})
}

// SetColorTemp sets the color temperature of a Zigbee light (153=warm, 500=cool).
func (z *Zigbee2MQTTClient) SetColorTemp(deviceName string, colorTemp int) error {
	return z.SetState(deviceName, map[string]interface{}{
		"color_temp": colorTemp,
	})
}

// GetDevices returns all known Zigbee devices.
func (z *Zigbee2MQTTClient) GetDevices() map[string]*ZigbeeDevice {
	z.mu.RLock()
	defer z.mu.RUnlock()
	result := make(map[string]*ZigbeeDevice, len(z.devices))
	for k, v := range z.devices {
		result[k] = v
	}
	return result
}

// GetDeviceSummary returns a human-readable summary of all Zigbee devices.
func (z *Zigbee2MQTTClient) GetDeviceSummary() string {
	devices := z.GetDevices()
	if len(devices) == 0 {
		return "Nenhum dispositivo Zigbee encontrado."
	}

	var sb strings.Builder
	sb.WriteString("📡 Dispositivos Zigbee:\n")
	for name, d := range devices {
		sb.WriteString(fmt.Sprintf("  • %s", name))
		if d.Model != "" {
			sb.WriteString(fmt.Sprintf(" (%s %s)", d.Vendor, d.Model))
		}
		if d.State != nil {
			if state, ok := d.State["state"].(string); ok {
				sb.WriteString(fmt.Sprintf(": %s", state))
			}
			if temp, ok := d.State["temperature"].(float64); ok {
				sb.WriteString(fmt.Sprintf(" | Temp: %.1f°C", temp))
			}
			if humidity, ok := d.State["humidity"].(float64); ok {
				sb.WriteString(fmt.Sprintf(" | Umidade: %.0f%%", humidity))
			}
			if battery, ok := d.State["battery"].(float64); ok {
				sb.WriteString(fmt.Sprintf(" | Bateria: %.0f%%", battery))
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
