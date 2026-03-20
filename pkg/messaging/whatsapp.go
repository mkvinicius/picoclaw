// Package messaging provides messaging integrations for PicoClaw.
// Supports WhatsApp (via Evolution API), Telegram, and other channels.
package messaging

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
// WhatsApp via Evolution API (open-source, self-hosted)
// https://github.com/EvolutionAPI/evolution-api
// ─────────────────────────────────────────────────────────────────────────────

// WhatsAppConfig holds the configuration for the WhatsApp integration.
type WhatsAppConfig struct {
	// EvolutionAPI settings
	ServerURL   string `json:"server_url"`   // ex: http://localhost:8080
	APIKey      string `json:"api_key"`
	Instance    string `json:"instance"`     // ex: picoclaw

	// Bot settings
	BotName     string `json:"bot_name"`
	OwnerNumber string `json:"owner_number"` // ex: 5511999999999
	AllowedNums []string `json:"allowed_numbers"` // empty = allow all

	// Behavior
	AutoReply   bool   `json:"auto_reply"`
	Prefix      string `json:"prefix"` // command prefix, ex: "/"
}

// DefaultWhatsAppConfig returns a default configuration.
func DefaultWhatsAppConfig() WhatsAppConfig {
	return WhatsAppConfig{
		ServerURL:  "http://localhost:8080",
		Instance:   "picoclaw",
		BotName:    "PicoClaw",
		AutoReply:  true,
		Prefix:     "/",
	}
}

// WhatsAppMessage represents an incoming or outgoing WhatsApp message.
type WhatsAppMessage struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Body      string    `json:"body"`
	Type      string    `json:"type"` // text, image, audio, document
	Timestamp time.Time `json:"timestamp"`
	IsGroup   bool      `json:"is_group"`
	GroupID   string    `json:"group_id"`
	MediaURL  string    `json:"media_url,omitempty"`
}

// MessageHandler is a function that handles incoming messages.
type MessageHandler func(msg WhatsAppMessage) (string, error)

// WhatsAppClient manages the WhatsApp connection via Evolution API.
type WhatsAppClient struct {
	config   WhatsAppConfig
	client   *http.Client
	handler  MessageHandler
	mu       sync.RWMutex
	running  bool
	webhookCh chan WhatsAppMessage
}

// NewWhatsAppClient creates a new WhatsApp client.
func NewWhatsAppClient(config WhatsAppConfig) *WhatsAppClient {
	return &WhatsAppClient{
		config:    config,
		client:    &http.Client{Timeout: 30 * time.Second},
		webhookCh: make(chan WhatsAppMessage, 100),
	}
}

// SetHandler sets the message handler function.
func (w *WhatsAppClient) SetHandler(handler MessageHandler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handler = handler
}

// Connect initializes the WhatsApp instance via Evolution API.
func (w *WhatsAppClient) Connect(ctx context.Context) error {
	// Check if instance already exists
	status, err := w.getInstanceStatus()
	if err != nil {
		// Try to create the instance
		if createErr := w.createInstance(); createErr != nil {
			return fmt.Errorf("failed to connect to Evolution API: %w (is it running at %s?)", createErr, w.config.ServerURL)
		}
	}

	if status == "open" {
		fmt.Printf("✅ WhatsApp já conectado (instância: %s)\n", w.config.Instance)
		return nil
	}

	// Get QR code for pairing
	qr, err := w.getQRCode()
	if err != nil {
		return fmt.Errorf("failed to get QR code: %w", err)
	}

	fmt.Printf("\n📱 WHATSAPP — ESCANEIE O QR CODE:\n")
	fmt.Printf("   Abra o WhatsApp → Dispositivos Conectados → Conectar Dispositivo\n")
	fmt.Printf("   QR Code URL: %s/instance/qrcode/%s\n\n", w.config.ServerURL, w.config.Instance)
	_ = qr

	// Start webhook listener
	go w.startWebhookListener(ctx)

	// Poll for connection status
	go w.pollConnectionStatus(ctx)

	return nil
}

// SendText sends a text message to a WhatsApp number.
func (w *WhatsAppClient) SendText(to, text string) error {
	payload := map[string]interface{}{
		"number":  to,
		"text":    text,
		"delay":   0,
	}
	return w.apiPost(fmt.Sprintf("/message/sendText/%s", w.config.Instance), payload)
}

// SendImage sends an image with optional caption.
func (w *WhatsAppClient) SendImage(to, imageURL, caption string) error {
	payload := map[string]interface{}{
		"number":  to,
		"mediatype": "image",
		"mimetype": "image/jpeg",
		"caption": caption,
		"media":   imageURL,
	}
	return w.apiPost(fmt.Sprintf("/message/sendMedia/%s", w.config.Instance), payload)
}

// SendDocument sends a document file.
func (w *WhatsAppClient) SendDocument(to, docURL, filename string) error {
	payload := map[string]interface{}{
		"number":   to,
		"mediatype": "document",
		"caption":  filename,
		"media":    docURL,
		"fileName": filename,
	}
	return w.apiPost(fmt.Sprintf("/message/sendMedia/%s", w.config.Instance), payload)
}

// SendReaction sends a reaction emoji to a message.
func (w *WhatsAppClient) SendReaction(to, messageID, emoji string) error {
	payload := map[string]interface{}{
		"number":    to,
		"key":       map[string]string{"id": messageID},
		"reaction":  emoji,
	}
	return w.apiPost(fmt.Sprintf("/message/sendReaction/%s", w.config.Instance), payload)
}

// HandleWebhook processes an incoming webhook payload from Evolution API.
func (w *WhatsAppClient) HandleWebhook(payload []byte) {
	var event struct {
		Event string `json:"event"`
		Data  struct {
			Key struct {
				RemoteJid string `json:"remoteJid"`
				FromMe    bool   `json:"fromMe"`
				ID        string `json:"id"`
			} `json:"key"`
			Message struct {
				Conversation         string `json:"conversation"`
				ExtendedTextMessage  struct {
					Text string `json:"text"`
				} `json:"extendedTextMessage"`
			} `json:"message"`
			MessageTimestamp int64  `json:"messageTimestamp"`
			PushName         string `json:"pushName"`
		} `json:"data"`
	}

	if err := json.Unmarshal(payload, &event); err != nil {
		return
	}

	if event.Event != "messages.upsert" || event.Data.Key.FromMe {
		return
	}

	body := event.Data.Message.Conversation
	if body == "" {
		body = event.Data.Message.ExtendedTextMessage.Text
	}
	if body == "" {
		return
	}

	from := strings.Replace(event.Data.Key.RemoteJid, "@s.whatsapp.net", "", 1)
	isGroup := strings.Contains(event.Data.Key.RemoteJid, "@g.us")

	msg := WhatsAppMessage{
		ID:        event.Data.Key.ID,
		From:      from,
		Body:      body,
		IsGroup:   isGroup,
		Timestamp: time.Unix(event.Data.MessageTimestamp, 0),
	}

	// Check if sender is allowed
	if !w.isAllowed(from) {
		return
	}

	// Send to channel for processing
	select {
	case w.webhookCh <- msg:
	default:
	}
}

// StartWebhookServer starts an HTTP server to receive Evolution API webhooks.
func (w *WhatsAppClient) StartWebhookServer(ctx context.Context, port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/whatsapp", func(rw http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(rw, "bad request", http.StatusBadRequest)
			return
		}
		w.HandleWebhook(body)
		rw.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf("localhost:%d", port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("WhatsApp webhook server error: %v\n", err)
		}
	}()

	// Register webhook URL with Evolution API
	webhookURL := fmt.Sprintf("http://localhost:%d/webhook/whatsapp", port)
	w.registerWebhook(webhookURL)

	fmt.Printf("📱 WhatsApp webhook ativo em: %s\n", webhookURL)
	return nil
}

// ProcessMessages processes incoming messages using the registered handler.
func (w *WhatsAppClient) ProcessMessages(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-w.webhookCh:
			w.mu.RLock()
			handler := w.handler
			w.mu.RUnlock()

			if handler == nil {
				continue
			}

			// Process in goroutine to avoid blocking
			go func(m WhatsAppMessage) {
				// Send "typing" indicator
				w.sendPresence(m.From, "composing")

				response, err := handler(m)
				if err != nil {
					response = fmt.Sprintf("❌ Erro ao processar: %v", err)
				}

				// Stop typing indicator
				w.sendPresence(m.From, "paused")

				if response != "" {
					if sendErr := w.SendText(m.From+"@s.whatsapp.net", response); sendErr != nil {
						fmt.Printf("WhatsApp send error: %v\n", sendErr)
					}
				}
			}(msg)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────────────────

func (w *WhatsAppClient) getInstanceStatus() (string, error) {
	resp, err := w.apiGet(fmt.Sprintf("/instance/connectionState/%s", w.config.Instance))
	if err != nil {
		return "", err
	}
	var result struct {
		Instance struct {
			State string `json:"state"`
		} `json:"instance"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", err
	}
	return result.Instance.State, nil
}

func (w *WhatsAppClient) createInstance() error {
	payload := map[string]interface{}{
		"instanceName": w.config.Instance,
		"qrcode":       true,
		"integration":  "WHATSAPP-BAILEYS",
	}
	return w.apiPost("/instance/create", payload)
}

func (w *WhatsAppClient) getQRCode() (string, error) {
	resp, err := w.apiGet(fmt.Sprintf("/instance/qrcode/%s", w.config.Instance))
	if err != nil {
		return "", err
	}
	var result struct {
		Base64 string `json:"base64"`
	}
	json.Unmarshal(resp, &result)
	return result.Base64, nil
}

func (w *WhatsAppClient) registerWebhook(url string) error {
	payload := map[string]interface{}{
		"url":     url,
		"enabled": true,
		"events":  []string{"messages.upsert", "connection.update"},
	}
	return w.apiPost(fmt.Sprintf("/webhook/set/%s", w.config.Instance), payload)
}

func (w *WhatsAppClient) sendPresence(to, presence string) {
	payload := map[string]interface{}{
		"number":   to,
		"presence": presence,
		"delay":    500,
	}
	w.apiPost(fmt.Sprintf("/chat/sendPresence/%s", w.config.Instance), payload)
}

func (w *WhatsAppClient) isAllowed(number string) bool {
	if len(w.config.AllowedNums) == 0 {
		return true
	}
	for _, n := range w.config.AllowedNums {
		if n == number {
			return true
		}
	}
	return false
}

func (w *WhatsAppClient) pollConnectionStatus(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status, err := w.getInstanceStatus()
			if err == nil && status == "open" {
				fmt.Printf("✅ WhatsApp conectado com sucesso!\n")
				return
			}
		}
	}
}

func (w *WhatsAppClient) startWebhookListener(ctx context.Context) {
	w.ProcessMessages(ctx)
}

func (w *WhatsAppClient) apiGet(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, w.config.ServerURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", w.config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (w *WhatsAppClient) apiPost(path string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, w.config.ServerURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("apikey", w.config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
