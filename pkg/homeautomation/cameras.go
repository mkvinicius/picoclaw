package homeautomation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// IP Camera Integration (RTSP / HTTP Snapshot / ONVIF)
// ─────────────────────────────────────────────────────────────────────────────

// CameraConfig holds configuration for an IP camera.
type CameraConfig struct {
	Name         string // friendly name
	Host         string // IP address or hostname
	Port         int    // HTTP port (default 80)
	RTSPPort     int    // RTSP port (default 554)
	Username     string
	Password     string
	SnapshotPath string // HTTP path for snapshot (e.g. "/snapshot.jpg")
	StreamPath   string // RTSP path (e.g. "/stream1")
}

// CameraSnapshot holds a captured snapshot.
type CameraSnapshot struct {
	CameraName string
	Timestamp  time.Time
	FilePath   string
	Width      int
	Height     int
	SizeBytes  int64
}

// CameraClient manages IP cameras.
type CameraClient struct {
	cameras    map[string]*CameraConfig
	httpClient *http.Client
	saveDir    string // directory to save snapshots
}

// NewCameraClient creates a new camera client.
func NewCameraClient(saveDir string) *CameraClient {
	if saveDir == "" {
		saveDir = os.TempDir()
	}
	return &CameraClient{
		cameras: make(map[string]*CameraConfig),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		saveDir: saveDir,
	}
}

// AddCamera registers a camera.
func (c *CameraClient) AddCamera(cfg CameraConfig) {
	if cfg.Port == 0 {
		cfg.Port = 80
	}
	if cfg.RTSPPort == 0 {
		cfg.RTSPPort = 554
	}
	if cfg.SnapshotPath == "" {
		cfg.SnapshotPath = "/snapshot.jpg"
	}
	if cfg.StreamPath == "" {
		cfg.StreamPath = "/stream1"
	}
	c.cameras[strings.ToLower(cfg.Name)] = &cfg
}

// GetRTSPURL returns the RTSP stream URL for a camera.
func (c *CameraClient) GetRTSPURL(cameraName string) (string, error) {
	cam, err := c.resolveCamera(cameraName)
	if err != nil {
		return "", err
	}
	if cam.Username != "" {
		return fmt.Sprintf("rtsp://%s:%s@%s:%d%s",
			cam.Username, cam.Password, cam.Host, cam.RTSPPort, cam.StreamPath), nil
	}
	return fmt.Sprintf("rtsp://%s:%d%s", cam.Host, cam.RTSPPort, cam.StreamPath), nil
}

// CaptureSnapshot captures a snapshot from a camera and saves it to disk.
func (c *CameraClient) CaptureSnapshot(ctx context.Context, cameraName string) (*CameraSnapshot, error) {
	cam, err := c.resolveCamera(cameraName)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("http://%s:%d%s", cam.Host, cam.Port, cam.SnapshotPath)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create snapshot request: %w", err)
	}
	if cam.Username != "" {
		req.SetBasicAuth(cam.Username, cam.Password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("capture snapshot from %s: %w", cam.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("camera %s returned HTTP %d", cam.Name, resp.StatusCode)
	}

	// Save to file
	timestamp := time.Now()
	filename := fmt.Sprintf("%s_%s.jpg",
		strings.ReplaceAll(strings.ToLower(cam.Name), " ", "_"),
		timestamp.Format("20060102_150405"))
	filePath := filepath.Join(c.saveDir, filename)

	f, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("create snapshot file: %w", err)
	}
	defer f.Close()

	size, err := io.Copy(f, resp.Body)
	if err != nil {
		return nil, fmt.Errorf("save snapshot: %w", err)
	}

	return &CameraSnapshot{
		CameraName: cam.Name,
		Timestamp:  timestamp,
		FilePath:   filePath,
		SizeBytes:  size,
	}, nil
}

// CaptureAllSnapshots captures snapshots from all registered cameras.
func (c *CameraClient) CaptureAllSnapshots(ctx context.Context) ([]*CameraSnapshot, []error) {
	var snapshots []*CameraSnapshot
	var errors []error

	for name := range c.cameras {
		snap, err := c.CaptureSnapshot(ctx, name)
		if err != nil {
			errors = append(errors, fmt.Errorf("camera %s: %w", name, err))
		} else {
			snapshots = append(snapshots, snap)
		}
	}
	return snapshots, errors
}

// GetCameraList returns a summary of all registered cameras.
func (c *CameraClient) GetCameraList() string {
	if len(c.cameras) == 0 {
		return "Nenhuma câmera registrada."
	}
	var sb strings.Builder
	sb.WriteString("📷 Câmeras registradas:\n")
	for _, cam := range c.cameras {
		rtspURL, _ := c.GetRTSPURL(cam.Name)
		sb.WriteString(fmt.Sprintf("  • %s (%s:%d)\n", cam.Name, cam.Host, cam.Port))
		sb.WriteString(fmt.Sprintf("    Stream: %s\n", rtspURL))
	}
	return sb.String()
}

func (c *CameraClient) resolveCamera(name string) (*CameraConfig, error) {
	key := strings.ToLower(name)
	if cam, ok := c.cameras[key]; ok {
		return cam, nil
	}
	// Partial match
	for k, cam := range c.cameras {
		if strings.Contains(k, key) || strings.Contains(key, k) {
			return cam, nil
		}
	}
	return nil, fmt.Errorf("câmera não encontrada: '%s'", name)
}

// ─────────────────────────────────────────────────────────────────────────────
// Energy Monitor Integration
// ─────────────────────────────────────────────────────────────────────────────

// EnergyReading holds an energy consumption reading.
type EnergyReading struct {
	DeviceName  string
	PowerWatts  float64
	EnergyKWh   float64
	Voltage     float64
	Current     float64
	Timestamp   time.Time
}

// EnergyMonitor tracks energy consumption from smart plugs via MQTT.
type EnergyMonitor struct {
	mqtt     *MQTTClient
	readings map[string]*EnergyReading
	mu       sync.RWMutex
	baseTopic string
}

// NewEnergyMonitor creates a new energy monitor.
func NewEnergyMonitor(mqttClient *MQTTClient, baseTopic string) *EnergyMonitor {
	if baseTopic == "" {
		baseTopic = "zigbee2mqtt"
	}
	em := &EnergyMonitor{
		mqtt:      mqttClient,
		readings:  make(map[string]*EnergyReading),
		baseTopic: baseTopic,
	}
	// Subscribe to all device updates for energy data
	mqttClient.Subscribe(baseTopic+"/+", 0, em.handleMessage)
	return em
}

func (em *EnergyMonitor) handleMessage(msg MQTTMessage) {
	parts := strings.SplitN(msg.Topic, "/", 2)
	if len(parts) < 2 {
		return
	}
	deviceName := parts[1]

	var data map[string]interface{}
	if err := json.Unmarshal(msg.Payload, &data); err != nil {
		return
	}

	reading := &EnergyReading{
		DeviceName: deviceName,
		Timestamp:  time.Now(),
	}

	if v, ok := data["power"].(float64); ok {
		reading.PowerWatts = v
	}
	if v, ok := data["energy"].(float64); ok {
		reading.EnergyKWh = v
	}
	if v, ok := data["voltage"].(float64); ok {
		reading.Voltage = v
	}
	if v, ok := data["current"].(float64); ok {
		reading.Current = v
	}

	// Only store if it has energy data
	if reading.PowerWatts > 0 || reading.EnergyKWh > 0 {
		em.mu.Lock()
		em.readings[deviceName] = reading
		em.mu.Unlock()
	}
}

// GetEnergySummary returns a human-readable energy consumption summary.
func (em *EnergyMonitor) GetEnergySummary() string {
	em.mu.RLock()
	defer em.mu.RUnlock()

	if len(em.readings) == 0 {
		return "Nenhum dado de consumo de energia disponível."
	}

	var sb strings.Builder
	sb.WriteString("⚡ Consumo de Energia:\n\n")

	var totalPower float64
	for _, r := range em.readings {
		sb.WriteString(fmt.Sprintf("  • %s:\n", r.DeviceName))
		if r.PowerWatts > 0 {
			sb.WriteString(fmt.Sprintf("    Potência atual: %.1f W\n", r.PowerWatts))
			totalPower += r.PowerWatts
		}
		if r.EnergyKWh > 0 {
			sb.WriteString(fmt.Sprintf("    Consumo total: %.3f kWh\n", r.EnergyKWh))
			// Estimate cost (R$ 0.80/kWh average Brazil 2026)
			cost := r.EnergyKWh * 0.80
			sb.WriteString(fmt.Sprintf("    Custo estimado: R$ %.2f\n", cost))
		}
		if r.Voltage > 0 {
			sb.WriteString(fmt.Sprintf("    Tensão: %.1f V\n", r.Voltage))
		}
	}

	sb.WriteString(fmt.Sprintf("\n💡 Potência total agora: %.1f W\n", totalPower))
	sb.WriteString(fmt.Sprintf("💰 Custo estimado/mês: R$ %.2f\n", totalPower/1000*24*30*0.80))

	return sb.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// Presence Detection
// ─────────────────────────────────────────────────────────────────────────────

// PresenceDetector tracks who is home via network device detection.
type PresenceDetector struct {
	devices    map[string]string // name → MAC/IP
	present    map[string]bool
	mu         sync.RWMutex
	onArrive   func(name string)
	onLeave    func(name string)
	pollInterval time.Duration
	done       chan struct{}
}

// NewPresenceDetector creates a new presence detector.
func NewPresenceDetector(pollInterval time.Duration) *PresenceDetector {
	if pollInterval == 0 {
		pollInterval = 30 * time.Second
	}
	return &PresenceDetector{
		devices:      make(map[string]string),
		present:      make(map[string]bool),
		pollInterval: pollInterval,
		done:         make(chan struct{}),
	}
}

// AddPerson registers a person with their device's IP or MAC address.
func (p *PresenceDetector) AddPerson(name, ipOrMAC string) {
	p.mu.Lock()
	p.devices[name] = ipOrMAC
	p.mu.Unlock()
}

// OnArrive sets the callback for when someone arrives home.
func (p *PresenceDetector) OnArrive(fn func(name string)) {
	p.onArrive = fn
}

// OnLeave sets the callback for when someone leaves home.
func (p *PresenceDetector) OnLeave(fn func(name string)) {
	p.onLeave = fn
}

// Start begins polling for device presence.
func (p *PresenceDetector) Start() {
	go func() {
		ticker := time.NewTicker(p.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-ticker.C:
				p.poll()
			}
		}
	}()
}

// Stop stops the presence detection.
func (p *PresenceDetector) Stop() {
	close(p.done)
}

func (p *PresenceDetector) poll() {
	p.mu.Lock()
	devices := make(map[string]string, len(p.devices))
	for k, v := range p.devices {
		devices[k] = v
	}
	p.mu.Unlock()

	for name, ip := range devices {
		isPresent := p.pingDevice(ip)

		p.mu.Lock()
		wasPresent := p.present[name]
		p.present[name] = isPresent
		p.mu.Unlock()

		if isPresent && !wasPresent && p.onArrive != nil {
			go p.onArrive(name)
		} else if !isPresent && wasPresent && p.onLeave != nil {
			go p.onLeave(name)
		}
	}
}

func (p *PresenceDetector) pingDevice(ip string) bool {
	conn, err := net.DialTimeout("tcp", ip+":80", 2*time.Second)
	if err != nil {
		// Try port 443
		conn, err = net.DialTimeout("tcp", ip+":443", 2*time.Second)
		if err != nil {
			return false
		}
	}
	conn.Close()
	return true
}

// GetPresenceSummary returns who is currently home.
func (p *PresenceDetector) GetPresenceSummary() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.present) == 0 {
		return "Nenhum dispositivo monitorado."
	}

	var home, away []string
	for name, present := range p.present {
		if present {
			home = append(home, name)
		} else {
			away = append(away, name)
		}
	}

	var sb strings.Builder
	sb.WriteString("👥 Presença em casa:\n")
	if len(home) > 0 {
		sb.WriteString(fmt.Sprintf("  🏠 Em casa: %s\n", strings.Join(home, ", ")))
	}
	if len(away) > 0 {
		sb.WriteString(fmt.Sprintf("  🚗 Fora: %s\n", strings.Join(away, ", ")))
	}
	return sb.String()
}
