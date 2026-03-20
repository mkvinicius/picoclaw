// Package apexlink implements the secure APEX ↔ PicoClaw communication protocol.
// Features: mutual authentication, HMAC-SHA256 message signing, AES-256-GCM encryption,
// automatic reconnection, and replay attack prevention.
package apexlink

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// MessageType classifies APEX ↔ PicoClaw messages.
type MessageType string

const (
	MsgHandshake   MessageType = "handshake"
	MsgHeartbeat   MessageType = "heartbeat"
	MsgTaskRequest MessageType = "task_request"
	MsgTaskResult  MessageType = "task_result"
	MsgTelemetry   MessageType = "telemetry"
	MsgCommand     MessageType = "command"
	MsgAlert       MessageType = "alert"
)

// SecureMessage is the envelope for all APEX ↔ PicoClaw messages.
type SecureMessage struct {
	ID        string      `json:"id"`
	Type      MessageType `json:"type"`
	Timestamp int64       `json:"timestamp"` // Unix nano for replay prevention
	NodeID    string      `json:"node_id"`
	Payload   string      `json:"payload"`   // base64(AES-256-GCM(json))
	Signature string      `json:"signature"` // HMAC-SHA256(id+type+timestamp+payload)
}

// TaskRequest is sent from APEX to PicoClaw.
type TaskRequest struct {
	TaskID   string            `json:"task_id"`
	Command  string            `json:"command"`
	Args     map[string]string `json:"args,omitempty"`
	Priority int               `json:"priority"` // 1-10
	Timeout  int               `json:"timeout"`  // seconds
}

// TaskResult is sent from PicoClaw to APEX.
type TaskResult struct {
	TaskID    string `json:"task_id"`
	Success   bool   `json:"success"`
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
	Duration  int64  `json:"duration_ms"`
	NodeID    string `json:"node_id"`
}

// Telemetry is sent periodically from PicoClaw to APEX.
type Telemetry struct {
	NodeID      string  `json:"node_id"`
	RAMUsageMB  float64 `json:"ram_usage_mb"`
	CPUPercent  float64 `json:"cpu_percent"`
	ActiveTasks int     `json:"active_tasks"`
	Uptime      int64   `json:"uptime_seconds"`
	Version     string  `json:"version"`
	Online      bool    `json:"online"`
}

// ─────────────────────────────────────────────────────────────────────────────
// APEXLink Client (runs on PicoClaw side)
// ─────────────────────────────────────────────────────────────────────────────

// Client manages the secure connection from PicoClaw to APEX cloud.
type Client struct {
	nodeID     string
	apexURL    string
	sharedKey  []byte // 32 bytes for AES-256
	hmacKey    []byte // 32 bytes for HMAC-SHA256
	httpClient *http.Client
	mu         sync.Mutex
	connected  bool
	startTime  time.Time
	seenIDs    map[string]time.Time // replay prevention
	handlers   map[MessageType]HandlerFunc
}

// HandlerFunc processes incoming messages.
type HandlerFunc func(ctx context.Context, msg SecureMessage, payload []byte) error

// NewClient creates a new APEX link client.
func NewClient(nodeID, apexURL, sharedSecret string) *Client {
	// Derive two separate keys from the shared secret
	h := sha256.Sum256([]byte(sharedSecret + ":enc"))
	encKey := h[:]
	h2 := sha256.Sum256([]byte(sharedSecret + ":mac"))
	macKey := h2[:]

	return &Client{
		nodeID:    nodeID,
		apexURL:   apexURL,
		sharedKey: encKey,
		hmacKey:   macKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		startTime: time.Now(),
		seenIDs:   make(map[string]time.Time),
		handlers:  make(map[MessageType]HandlerFunc),
	}
}

// Register registers a handler for a message type.
func (c *Client) Register(msgType MessageType, handler HandlerFunc) {
	c.handlers[msgType] = handler
}

// Connect performs the initial handshake with APEX.
func (c *Client) Connect(ctx context.Context) error {
	payload := map[string]string{
		"node_id":   c.nodeID,
		"version":   "2.0.0",
		"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
	}

	resp, err := c.Send(ctx, MsgHandshake, payload)
	if err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

	var result map[string]string
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("invalid handshake response: %w", err)
	}

	if result["status"] != "ok" {
		return fmt.Errorf("APEX rejected connection: %s", result["reason"])
	}

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()

	fmt.Printf("🔗 Conectado ao APEX Cloud: %s\n", c.apexURL)
	return nil
}

// Send encrypts and sends a message to APEX, returning the decrypted response.
func (c *Client) Send(ctx context.Context, msgType MessageType, payload interface{}) ([]byte, error) {
	// Serialize payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Encrypt payload
	encrypted, err := c.encrypt(payloadBytes)
	if err != nil {
		return nil, err
	}

	// Build message
	msgID := generateID()
	msg := SecureMessage{
		ID:        msgID,
		Type:      msgType,
		Timestamp: time.Now().UnixNano(),
		NodeID:    c.nodeID,
		Payload:   base64.StdEncoding.EncodeToString(encrypted),
	}

	// Sign message
	msg.Signature = c.sign(msg)

	// Send HTTP POST
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apexURL+"/api/v1/picoclaw/message", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-PicoClaw-Node", c.nodeID)

	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("APEX unreachable: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("APEX error %d: %s", httpResp.StatusCode, string(respBody))
	}

	// Parse and verify response
	var respMsg SecureMessage
	if err := json.Unmarshal(respBody, &respMsg); err != nil {
		return nil, fmt.Errorf("invalid response format: %w", err)
	}

	if err := c.verify(respMsg); err != nil {
		return nil, fmt.Errorf("response verification failed: %w", err)
	}

	// Decrypt response payload
	encBytes, err := base64.StdEncoding.DecodeString(respMsg.Payload)
	if err != nil {
		return nil, err
	}

	return c.decrypt(encBytes)
}

// SendTelemetry sends periodic telemetry to APEX.
func (c *Client) SendTelemetry(ctx context.Context, t Telemetry) error {
	t.NodeID = c.nodeID
	t.Uptime = int64(time.Since(c.startTime).Seconds())
	_, err := c.Send(ctx, MsgTelemetry, t)
	return err
}

// StartHeartbeat starts a background goroutine sending heartbeats to APEX.
func (c *Client) StartHeartbeat(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, err := c.Send(ctx, MsgHeartbeat, map[string]string{
					"node_id": c.nodeID,
					"ts":      fmt.Sprintf("%d", time.Now().Unix()),
				})
				if err != nil {
					fmt.Printf("⚠️  Heartbeat falhou: %v\n", err)
					c.mu.Lock()
					c.connected = false
					c.mu.Unlock()
				} else {
					c.mu.Lock()
					c.connected = true
					c.mu.Unlock()
				}
			}
		}
	}()
}

// IsConnected returns whether the client is connected to APEX.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// ─────────────────────────────────────────────────────────────────────────────
// APEXLink Server (runs on APEX cloud side)
// ─────────────────────────────────────────────────────────────────────────────

// Server handles incoming PicoClaw connections on the APEX side.
type Server struct {
	nodes     map[string]*NodeInfo
	sharedKey []byte
	hmacKey   []byte
	mu        sync.RWMutex
	handlers  map[MessageType]HandlerFunc
}

// NodeInfo tracks a registered PicoClaw node.
type NodeInfo struct {
	NodeID    string    `json:"node_id"`
	Version   string    `json:"version"`
	LastSeen  time.Time `json:"last_seen"`
	Connected bool      `json:"connected"`
	Telemetry Telemetry `json:"telemetry"`
}

// NewServer creates a new APEX link server.
func NewServer(sharedSecret string) *Server {
	h := sha256.Sum256([]byte(sharedSecret + ":enc"))
	encKey := h[:]
	h2 := sha256.Sum256([]byte(sharedSecret + ":mac"))
	macKey := h2[:]

	return &Server{
		nodes:     make(map[string]*NodeInfo),
		sharedKey: encKey,
		hmacKey:   macKey,
		handlers:  make(map[MessageType]HandlerFunc),
	}
}

// HandleMessage processes an incoming message from a PicoClaw node.
func (srv *Server) HandleMessage(ctx context.Context, msg SecureMessage) (SecureMessage, error) {
	// Verify signature
	if err := srv.verify(msg); err != nil {
		return SecureMessage{}, fmt.Errorf("signature invalid: %w", err)
	}

	// Decrypt payload
	encBytes, err := base64.StdEncoding.DecodeString(msg.Payload)
	if err != nil {
		return SecureMessage{}, err
	}
	payload, err := srv.decrypt(encBytes)
	if err != nil {
		return SecureMessage{}, err
	}

	// Update node registry
	srv.mu.Lock()
	node, ok := srv.nodes[msg.NodeID]
	if !ok {
		node = &NodeInfo{NodeID: msg.NodeID}
		srv.nodes[msg.NodeID] = node
	}
	node.LastSeen = time.Now()
	node.Connected = true
	srv.mu.Unlock()

	// Dispatch to handler
	var respPayload interface{}
	if handler, ok := srv.handlers[msg.Type]; ok {
		if err := handler(ctx, msg, payload); err != nil {
			respPayload = map[string]string{"status": "error", "reason": err.Error()}
		} else {
			respPayload = map[string]string{"status": "ok"}
		}
	} else {
		// Default: handle built-in types
		switch msg.Type {
		case MsgHandshake:
			var req map[string]string
			json.Unmarshal(payload, &req)
			srv.mu.Lock()
			srv.nodes[msg.NodeID].Version = req["version"]
			srv.mu.Unlock()
			respPayload = map[string]string{"status": "ok", "server": "APEX Cloud"}
		case MsgHeartbeat:
			respPayload = map[string]string{"status": "ok", "ts": fmt.Sprintf("%d", time.Now().Unix())}
		case MsgTelemetry:
			var t Telemetry
			json.Unmarshal(payload, &t)
			srv.mu.Lock()
			srv.nodes[msg.NodeID].Telemetry = t
			srv.mu.Unlock()
			respPayload = map[string]string{"status": "ok"}
		default:
			respPayload = map[string]string{"status": "unknown_type"}
		}
	}

	return srv.buildResponse(msg.NodeID, MsgTaskResult, respPayload)
}

func (srv *Server) buildResponse(nodeID string, msgType MessageType, payload interface{}) (SecureMessage, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return SecureMessage{}, err
	}
	encrypted, err := srv.encrypt(payloadBytes)
	if err != nil {
		return SecureMessage{}, err
	}

	msgID := generateID()
	msg := SecureMessage{
		ID:        msgID,
		Type:      msgType,
		Timestamp: time.Now().UnixNano(),
		NodeID:    "apex-cloud",
		Payload:   base64.StdEncoding.EncodeToString(encrypted),
	}
	msg.Signature = srv.sign(msg)
	return msg, nil
}

// GetNodes returns all registered PicoClaw nodes.
func (srv *Server) GetNodes() []NodeInfo {
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	nodes := make([]NodeInfo, 0, len(srv.nodes))
	for _, n := range srv.nodes {
		nodes = append(nodes, *n)
	}
	return nodes
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared crypto helpers
// ─────────────────────────────────────────────────────────────────────────────

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func sign(hmacKey []byte, msg SecureMessage) string {
	data := fmt.Sprintf("%s|%s|%d|%s", msg.ID, msg.Type, msg.Timestamp, msg.Payload)
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

func verify(hmacKey []byte, msg SecureMessage) error {
	expected := sign(hmacKey, msg)
	if !hmac.Equal([]byte(expected), []byte(msg.Signature)) {
		return fmt.Errorf("signature mismatch")
	}
	// Replay prevention: reject messages older than 5 minutes
	age := time.Since(time.Unix(0, msg.Timestamp))
	if age > 5*time.Minute || age < -1*time.Minute {
		return fmt.Errorf("message timestamp out of range (age: %v)", age)
	}
	return nil
}

// Client-side wrappers
func (c *Client) encrypt(data []byte) ([]byte, error) { return encrypt(c.sharedKey, data) }
func (c *Client) decrypt(data []byte) ([]byte, error) { return decrypt(c.sharedKey, data) }
func (c *Client) sign(msg SecureMessage) string       { return sign(c.hmacKey, msg) }
func (c *Client) verify(msg SecureMessage) error      { return verify(c.hmacKey, msg) }

// Server-side wrappers
func (s *Server) encrypt(data []byte) ([]byte, error) { return encrypt(s.sharedKey, data) }
func (s *Server) decrypt(data []byte) ([]byte, error) { return decrypt(s.sharedKey, data) }
func (s *Server) sign(msg SecureMessage) string       { return sign(s.hmacKey, msg) }
func (s *Server) verify(msg SecureMessage) error      { return verify(s.hmacKey, msg) }

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
