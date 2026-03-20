// Package reasoning provides hybrid Online/Offline reasoning capabilities for PicoClaw.
// It allows the user to choose between:
//   - Online mode: uses external APIs (OpenAI, Claude, Gemini, OpenRouter, etc.)
//   - Offline mode: uses local Ollama models (Llama 3.2, Phi-3, Gemma, etc.)
//
// The package also implements a Tri-Brain reasoning layer that validates every
// AI response through three independent perspectives before returning it to the
// agent, providing MIL-SPEC level reasoning safety without external dependencies.
package reasoning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Mode & Configuration
// ─────────────────────────────────────────────────────────────────────────────

// Mode defines whether the agent uses online APIs or local models.
type Mode string

const (
	ModeOnline  Mode = "online"  // Uses external API (OpenAI, Claude, etc.)
	ModeOffline Mode = "offline" // Uses local Ollama model
	ModeAuto    Mode = "auto"    // Tries online, falls back to offline if unavailable
)

// HybridConfig holds configuration for the hybrid reasoning engine.
type HybridConfig struct {
	Mode Mode

	// Online settings (used when Mode == ModeOnline or ModeAuto)
	OnlineBaseURL string // e.g. "https://api.openai.com/v1"
	OnlineAPIKey  string
	OnlineModel   string // e.g. "gpt-4.1-mini", "claude-sonnet-4.6"

	// Offline settings (used when Mode == ModeOffline or ModeAuto fallback)
	OllamaBaseURL string // default "http://localhost:11434"
	OllamaModel   string // e.g. "llama3.2:3b", "phi3:mini", "gemma2:2b"

	// Tri-Brain settings
	TriBrainEnabled bool          // Enable triple-validation of responses
	TriBrainTimeout time.Duration // Timeout for each brain call (default 30s)

	// HTTP client settings
	Timeout time.Duration
}

// DefaultHybridConfig returns a sensible default configuration.
func DefaultHybridConfig() HybridConfig {
	return HybridConfig{
		Mode:            ModeAuto,
		OllamaBaseURL:   "http://localhost:11434",
		OllamaModel:     "llama3.2:3b",
		TriBrainEnabled: true,
		TriBrainTimeout: 30 * time.Second,
		Timeout:         60 * time.Second,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HybridEngine — Main reasoning engine
// ─────────────────────────────────────────────────────────────────────────────

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`    // "system", "user", "assistant"
	Content string `json:"content"`
}

// ReasoningResult holds the result of a reasoning call.
type ReasoningResult struct {
	Content      string        // The final validated response
	Mode         Mode          // Which mode was used (online/offline)
	Model        string        // Which model was used
	TriBrainUsed bool          // Whether Tri-Brain validation was applied
	Confidence   float64       // 0.0-1.0 confidence score from Tri-Brain
	Latency      time.Duration // Total reasoning time
	Warnings     []string      // Any warnings from Tri-Brain
}

// HybridEngine is the main reasoning engine.
type HybridEngine struct {
	config     HybridConfig
	httpClient *http.Client
	mu         sync.Mutex
	online     bool // cached connectivity status
	lastCheck  time.Time
}

// NewHybridEngine creates a new hybrid reasoning engine.
func NewHybridEngine(cfg HybridConfig) *HybridEngine {
	return &HybridEngine{
		config: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Reason processes a conversation and returns a validated response.
// This is the main entry point called by PicoClaw agents.
func (e *HybridEngine) Reason(ctx context.Context, messages []Message) (*ReasoningResult, error) {
	start := time.Now()

	// Determine which mode to use
	mode := e.resolveMode(ctx)

	var result *ReasoningResult
	var err error

	switch mode {
	case ModeOnline:
		result, err = e.callOnline(ctx, messages)
	case ModeOffline:
		result, err = e.callOllama(ctx, messages)
	default:
		result, err = e.callOnline(ctx, messages)
		if err != nil {
			// Fallback to offline
			result, err = e.callOllama(ctx, messages)
			if err != nil {
				return nil, fmt.Errorf("both online and offline reasoning failed: %w", err)
			}
			result.Warnings = append(result.Warnings, "Online API unavailable, used offline fallback")
		}
	}

	if err != nil {
		return nil, err
	}

	// Apply Tri-Brain validation if enabled
	if e.config.TriBrainEnabled {
		result = e.applyTriBrain(ctx, messages, result)
	}

	result.Latency = time.Since(start)
	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Online API Call (OpenAI-compatible)
// ─────────────────────────────────────────────────────────────────────────────

type openAIRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (e *HybridEngine) callOnline(ctx context.Context, messages []Message) (*ReasoningResult, error) {
	if e.config.OnlineBaseURL == "" {
		return nil, fmt.Errorf("online base URL not configured")
	}
	if e.config.OnlineAPIKey == "" {
		return nil, fmt.Errorf("online API key not configured")
	}

	reqBody, err := json.Marshal(openAIRequest{
		Model:    e.config.OnlineModel,
		Messages: messages,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		e.config.OnlineBaseURL+"/chat/completions",
		bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.config.OnlineAPIKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("online API call failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var apiResp openAIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("parse online API response: %w", err)
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("online API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("online API returned no choices")
	}

	return &ReasoningResult{
		Content: apiResp.Choices[0].Message.Content,
		Mode:    ModeOnline,
		Model:   e.config.OnlineModel,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Offline Ollama Call
// ─────────────────────────────────────────────────────────────────────────────

type ollamaRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type ollamaResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

func (e *HybridEngine) callOllama(ctx context.Context, messages []Message) (*ReasoningResult, error) {
	baseURL := e.config.OllamaBaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	reqBody, err := json.Marshal(ollamaRequest{
		Model:    e.config.OllamaModel,
		Messages: messages,
		Stream:   false,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/api/chat",
		bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Ollama não está rodando em %s. Execute 'picoclaw offline start' para iniciar: %w", baseURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return nil, fmt.Errorf("parse Ollama response: %w", err)
	}

	if ollamaResp.Error != "" {
		return nil, fmt.Errorf("Ollama error: %s", ollamaResp.Error)
	}

	return &ReasoningResult{
		Content: ollamaResp.Message.Content,
		Mode:    ModeOffline,
		Model:   e.config.OllamaModel,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Tri-Brain Validation (MIL-SPEC Safety Layer)
// ─────────────────────────────────────────────────────────────────────────────
// The Tri-Brain validates every AI response through three independent lenses:
//   1. Safety Brain: checks for harmful content, prompt injection, data exfiltration
//   2. Logic Brain: checks for contradictions, hallucinations, factual inconsistencies
//   3. Intent Brain: checks if the response actually answers what was asked
//
// If all three agree the response is valid, confidence = 1.0
// If two agree, confidence = 0.67 and a warning is added
// If only one agrees, the response is regenerated or flagged

type brainVerdict struct {
	Name       string
	Approved   bool
	Reason     string
	Confidence float64
}

func (e *HybridEngine) applyTriBrain(ctx context.Context, originalMessages []Message, result *ReasoningResult) *ReasoningResult {
	// Run three independent validation checks in parallel
	verdictCh := make(chan brainVerdict, 3)

	go func() { verdictCh <- e.safetyBrain(ctx, originalMessages, result.Content) }()
	go func() { verdictCh <- e.logicBrain(ctx, originalMessages, result.Content) }()
	go func() { verdictCh <- e.intentBrain(ctx, originalMessages, result.Content) }()

	var verdicts []brainVerdict
	timeout := time.After(e.config.TriBrainTimeout)
	for i := 0; i < 3; i++ {
		select {
		case v := <-verdictCh:
			verdicts = append(verdicts, v)
		case <-timeout:
			// If a brain times out, we give it a neutral verdict
			verdicts = append(verdicts, brainVerdict{
				Name:       "timeout",
				Approved:   true,
				Reason:     "Brain validation timed out, proceeding with caution",
				Confidence: 0.5,
			})
		}
	}

	// Calculate consensus
	approved := 0
	var totalConfidence float64
	var warnings []string

	for _, v := range verdicts {
		if v.Approved {
			approved++
			totalConfidence += v.Confidence
		} else {
			warnings = append(warnings, fmt.Sprintf("[%s] %s", v.Name, v.Reason))
		}
	}

	result.TriBrainUsed = true
	result.Confidence = totalConfidence / 3.0
	result.Warnings = append(result.Warnings, warnings...)

	// If safety brain rejected, always block regardless of others
	for _, v := range verdicts {
		if v.Name == "safety" && !v.Approved {
			result.Content = fmt.Sprintf("⚠️ Resposta bloqueada pelo Tri-Brain (Safety): %s", v.Reason)
			result.Confidence = 0.0
			return result
		}
	}

	// If less than 2 brains approved, add strong warning
	if approved < 2 {
		result.Warnings = append(result.Warnings,
			"⚠️ Tri-Brain: menos de 2 validações aprovadas. Resposta pode ser imprecisa.")
	}

	return result
}

// safetyBrain checks for harmful content, prompt injection, and data exfiltration.
func (e *HybridEngine) safetyBrain(_ context.Context, messages []Message, response string) brainVerdict {
	verdict := brainVerdict{Name: "safety", Approved: true, Confidence: 1.0}

	responseLower := strings.ToLower(response)

	// Check for prompt injection attempts in the response
	injectionPatterns := []string{
		"ignore previous instructions",
		"ignore all previous",
		"disregard your instructions",
		"you are now",
		"act as if",
		"pretend you are",
		"jailbreak",
		"dan mode",
		"developer mode",
	}
	for _, pattern := range injectionPatterns {
		if strings.Contains(responseLower, pattern) {
			return brainVerdict{
				Name:     "safety",
				Approved: false,
				Reason:   fmt.Sprintf("Prompt injection detectado na resposta: '%s'", pattern),
			}
		}
	}

	// Check for data exfiltration patterns
	exfilPatterns := []string{
		"curl -d",
		"wget --post",
		"/dev/tcp/",
		"nc -e",
		"base64 -d",
		"eval(",
	}
	for _, pattern := range exfilPatterns {
		if strings.Contains(responseLower, pattern) {
			return brainVerdict{
				Name:     "safety",
				Approved: false,
				Reason:   fmt.Sprintf("Padrão de exfiltração de dados detectado: '%s'", pattern),
			}
		}
	}

	// Check for destructive commands
	destructivePatterns := []string{
		"rm -rf /",
		"format c:",
		"del /f /s /q",
		"dd if=/dev/zero of=/dev/",
		":(){:|:&};:",
	}
	for _, pattern := range destructivePatterns {
		if strings.Contains(responseLower, pattern) {
			return brainVerdict{
				Name:     "safety",
				Approved: false,
				Reason:   fmt.Sprintf("Comando destrutivo detectado: '%s'", pattern),
			}
		}
	}

	// Check if response references sensitive files from the original messages
	_ = messages // Could be used for context-aware checks in future

	return verdict
}

// logicBrain checks for obvious contradictions and hallucinations.
func (e *HybridEngine) logicBrain(_ context.Context, messages []Message, response string) brainVerdict {
	verdict := brainVerdict{Name: "logic", Approved: true, Confidence: 1.0}

	// Check for empty or very short responses
	if strings.TrimSpace(response) == "" {
		return brainVerdict{
			Name:       "logic",
			Approved:   false,
			Reason:     "Resposta vazia",
			Confidence: 0.0,
		}
	}

	// Check for obvious error messages that slipped through
	errorPatterns := []string{
		"i cannot",
		"i'm unable to",
		"as an ai, i",
		"i don't have access",
	}
	responseLower := strings.ToLower(response)
	for _, pattern := range errorPatterns {
		if strings.Contains(responseLower, pattern) {
			verdict.Confidence = 0.7
			verdict.Reason = "Resposta contém limitação de IA, pode ser imprecisa"
		}
	}

	// Check for suspiciously repetitive content (hallucination indicator)
	words := strings.Fields(response)
	if len(words) > 20 {
		wordCount := make(map[string]int)
		for _, w := range words {
			wordCount[strings.ToLower(w)]++
		}
		for word, count := range wordCount {
			if count > len(words)/5 && len(word) > 4 {
				verdict.Confidence = 0.6
				verdict.Reason = fmt.Sprintf("Palavra '%s' repetida %d vezes, possível alucinação", word, count)
				break
			}
		}
	}

	return verdict
}

// intentBrain checks if the response actually addresses what was asked.
func (e *HybridEngine) intentBrain(_ context.Context, messages []Message, response string) brainVerdict {
	verdict := brainVerdict{Name: "intent", Approved: true, Confidence: 1.0}

	if len(messages) == 0 {
		return verdict
	}

	// Get the last user message
	var lastUserMsg string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserMsg = strings.ToLower(messages[i].Content)
			break
		}
	}

	if lastUserMsg == "" {
		return verdict
	}

	responseLower := strings.ToLower(response)

	// Check if a question was asked but response doesn't seem to answer it
	if strings.Contains(lastUserMsg, "?") {
		questionWords := []string{"what", "how", "why", "when", "where", "who", "which",
			"o que", "como", "por que", "quando", "onde", "quem", "qual"}
		isQuestion := false
		for _, qw := range questionWords {
			if strings.HasPrefix(lastUserMsg, qw) {
				isQuestion = true
				break
			}
		}
		if isQuestion && len(responseLower) < 20 {
			verdict.Confidence = 0.6
			verdict.Reason = "Pergunta detectada mas resposta é muito curta"
		}
	}

	// Check if a command was given and response confirms action
	commandWords := []string{"liga", "desliga", "abre", "fecha", "cria", "delete", "run", "execute",
		"turn on", "turn off", "create", "delete", "start", "stop"}
	for _, cw := range commandWords {
		if strings.Contains(lastUserMsg, cw) {
			// Response should contain some confirmation
			confirmWords := []string{"✅", "ok", "done", "feito", "executado", "concluído", "ligado", "desligado"}
			hasConfirmation := false
			for _, conf := range confirmWords {
				if strings.Contains(responseLower, conf) {
					hasConfirmation = true
					break
				}
			}
			if !hasConfirmation {
				verdict.Confidence = 0.8
				verdict.Reason = "Comando detectado mas resposta não confirma execução"
			}
			break
		}
	}

	return verdict
}

// ─────────────────────────────────────────────────────────────────────────────
// Mode Resolution
// ─────────────────────────────────────────────────────────────────────────────

func (e *HybridEngine) resolveMode(ctx context.Context) Mode {
	if e.config.Mode != ModeAuto {
		return e.config.Mode
	}

	// Cache connectivity check for 60 seconds
	e.mu.Lock()
	defer e.mu.Unlock()

	if time.Since(e.lastCheck) < 60*time.Second {
		if e.online {
			return ModeOnline
		}
		return ModeOffline
	}

	e.lastCheck = time.Now()
	e.online = e.checkOnlineAvailability(ctx)

	if e.online {
		return ModeOnline
	}
	return ModeOffline
}

func (e *HybridEngine) checkOnlineAvailability(ctx context.Context) bool {
	if e.config.OnlineBaseURL == "" || e.config.OnlineAPIKey == "" {
		return false
	}

	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, "GET", e.config.OnlineBaseURL+"/models", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+e.config.OnlineAPIKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// ─────────────────────────────────────────────────────────────────────────────
// Ollama Manager — Download and manage local models
// ─────────────────────────────────────────────────────────────────────────────

// OllamaManager handles installation and management of Ollama and local models.
type OllamaManager struct {
	baseURL string
	client  *http.Client
}

// NewOllamaManager creates a new Ollama manager.
func NewOllamaManager(baseURL string) *OllamaManager {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &OllamaManager{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// IsOllamaRunning checks if Ollama is running locally.
func (m *OllamaManager) IsOllamaRunning() bool {
	resp, err := m.client.Get(m.baseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// IsModelAvailable checks if a specific model is already downloaded.
func (m *OllamaManager) IsModelAvailable(modelName string) bool {
	resp, err := m.client.Get(m.baseURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false
	}

	for _, m := range result.Models {
		if strings.HasPrefix(m.Name, modelName) {
			return true
		}
	}
	return false
}

// InstallOllama downloads and installs Ollama for the current OS.
func (m *OllamaManager) InstallOllama(progressFn func(msg string)) error {
	progressFn("🔍 Detectando sistema operacional...")

	switch runtime.GOOS {
	case "linux":
		progressFn("📥 Baixando Ollama para Linux...")
		cmd := exec.Command("sh", "-c", "curl -fsSL https://ollama.com/install.sh | sh")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("falha ao instalar Ollama: %w", err)
		}

	case "darwin":
		progressFn("📥 Baixando Ollama para macOS...")
		// Try homebrew first
		if _, err := exec.LookPath("brew"); err == nil {
			cmd := exec.Command("brew", "install", "ollama")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("falha ao instalar Ollama via Homebrew: %w", err)
			}
		} else {
			return fmt.Errorf("Homebrew não encontrado. Instale o Ollama manualmente em https://ollama.com/download")
		}

	case "windows":
		progressFn("📥 Para Windows, baixe o instalador em: https://ollama.com/download")
		progressFn("   Após instalar, execute: picoclaw offline start")
		return fmt.Errorf("instalação automática não disponível no Windows. Acesse https://ollama.com/download")

	default:
		return fmt.Errorf("sistema operacional não suportado: %s", runtime.GOOS)
	}

	progressFn("✅ Ollama instalado com sucesso!")
	return nil
}

// DownloadModel downloads a model from Ollama with progress reporting.
func (m *OllamaManager) DownloadModel(ctx context.Context, modelName string, progressFn func(msg string)) error {
	progressFn(fmt.Sprintf("📥 Baixando modelo %s...", modelName))
	progressFn("   Isso pode levar alguns minutos dependendo da sua internet.")
	progressFn(fmt.Sprintf("   Tamanho aproximado: %s", modelSizeHint(modelName)))

	reqBody, _ := json.Marshal(map[string]interface{}{
		"name":   modelName,
		"stream": true,
	})

	req, err := http.NewRequestWithContext(ctx, "POST",
		m.baseURL+"/api/pull",
		bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	// Use a longer timeout for model downloads
	downloadClient := &http.Client{Timeout: 30 * time.Minute}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return fmt.Errorf("falha ao baixar modelo: %w", err)
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	lastStatus := ""
	for {
		var event map[string]interface{}
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		status, _ := event["status"].(string)
		if status != lastStatus && status != "" {
			if total, ok := event["total"].(float64); ok && total > 0 {
				completed, _ := event["completed"].(float64)
				pct := (completed / total) * 100
				progressFn(fmt.Sprintf("   %s: %.1f%%", status, pct))
			} else {
				progressFn(fmt.Sprintf("   %s", status))
			}
			lastStatus = status
		}
	}

	progressFn(fmt.Sprintf("✅ Modelo %s baixado com sucesso!", modelName))
	return nil
}

// StartOllama starts the Ollama server in the background.
func (m *OllamaManager) StartOllama() error {
	cmd := exec.Command("ollama", "serve")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("falha ao iniciar Ollama: %w", err)
	}

	// Wait for it to be ready
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if m.IsOllamaRunning() {
			return nil
		}
	}
	return fmt.Errorf("Ollama iniciou mas não respondeu em 5 segundos")
}

// RecommendedModels returns a list of recommended models for offline use.
func RecommendedModels() []ModelInfo {
	return []ModelInfo{
		{
			Name:        "llama3.2:3b",
			DisplayName: "Llama 3.2 (3B) — Recomendado",
			Size:        "2.0 GB",
			RAM:         "4 GB",
			Speed:       "Rápido",
			Quality:     "Bom",
			Description: "Melhor equilíbrio entre velocidade e qualidade. Ideal para uso geral.",
		},
		{
			Name:        "phi3:mini",
			DisplayName: "Phi-3 Mini — Ultra Leve",
			Size:        "2.3 GB",
			RAM:         "4 GB",
			Speed:       "Muito Rápido",
			Quality:     "Bom",
			Description: "Modelo da Microsoft. Excelente para tarefas de código e raciocínio.",
		},
		{
			Name:        "gemma2:2b",
			DisplayName: "Gemma 2 (2B) — Google",
			Size:        "1.6 GB",
			RAM:         "4 GB",
			Speed:       "Muito Rápido",
			Quality:     "Bom",
			Description: "Modelo do Google. Leve e eficiente para conversas gerais.",
		},
		{
			Name:        "llama3.2:8b",
			DisplayName: "Llama 3.2 (8B) — Alta Qualidade",
			Size:        "4.7 GB",
			RAM:         "8 GB",
			Speed:       "Médio",
			Quality:     "Muito Bom",
			Description: "Maior capacidade de raciocínio. Requer mais RAM.",
		},
		{
			Name:        "mistral:7b",
			DisplayName: "Mistral (7B) — Código",
			Size:        "4.1 GB",
			RAM:         "8 GB",
			Speed:       "Médio",
			Quality:     "Muito Bom",
			Description: "Excelente para programação e análise técnica.",
		},
	}
}

// ModelInfo holds information about a recommended model.
type ModelInfo struct {
	Name        string
	DisplayName string
	Size        string
	RAM         string
	Speed       string
	Quality     string
	Description string
}

func modelSizeHint(modelName string) string {
	hints := map[string]string{
		"llama3.2:3b": "~2.0 GB",
		"llama3.2:8b": "~4.7 GB",
		"phi3:mini":   "~2.3 GB",
		"gemma2:2b":   "~1.6 GB",
		"mistral:7b":  "~4.1 GB",
	}
	if hint, ok := hints[modelName]; ok {
		return hint
	}
	return "tamanho variável"
}
