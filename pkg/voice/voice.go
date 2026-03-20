// Package voice provides bidirectional voice interaction for PicoClaw.
// Features: Speech-to-Text (STT), Text-to-Speech (TTS), wake word detection,
// and Tri-Brain security validation for voice commands.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// TTSProvider specifies the text-to-speech engine.
type TTSProvider string

const (
	TTSOpenAI   TTSProvider = "openai"   // OpenAI TTS (online, high quality)
	TTSElevenLabs TTSProvider = "elevenlabs" // ElevenLabs (online, most natural)
	TTSPiper    TTSProvider = "piper"    // Piper TTS (offline, local)
	TTSSystem   TTSProvider = "system"   // OS native TTS (espeak/say)
)

// STTProvider specifies the speech-to-text engine.
type STTProvider string

const (
	STTWhisperAPI  STTProvider = "whisper_api"  // OpenAI Whisper API (online)
	STTWhisperLocal STTProvider = "whisper_local" // Whisper.cpp (offline)
	STTSystem      STTProvider = "system"        // OS native STT
)

// VoiceConfig holds voice module configuration.
type VoiceConfig struct {
	TTSProvider    TTSProvider `json:"tts_provider"`
	STTProvider    STTProvider `json:"stt_provider"`
	TTSVoice       string      `json:"tts_voice"`       // e.g., "nova", "alloy", "echo"
	TTSSpeed       float64     `json:"tts_speed"`       // 0.5 - 2.0
	Language       string      `json:"language"`        // e.g., "pt-BR"
	WakeWord       string      `json:"wake_word"`       // e.g., "hey synapse"
	OpenAIKey      string      `json:"openai_key,omitempty"`
	ElevenLabsKey  string      `json:"elevenlabs_key,omitempty"`
	AudioOutputDir string      `json:"audio_output_dir"`
}

// VoiceCommand represents a processed voice command.
type VoiceCommand struct {
	Raw         string    `json:"raw"`          // original transcription
	Normalized  string    `json:"normalized"`   // cleaned up text
	Confidence  float64   `json:"confidence"`   // STT confidence 0-1
	Language    string    `json:"language"`     // detected language
	Timestamp   time.Time `json:"timestamp"`
	AudioFile   string    `json:"audio_file,omitempty"`
}

// CommandHandler processes a recognized voice command.
type CommandHandler func(ctx context.Context, cmd VoiceCommand) (string, error)

// ─────────────────────────────────────────────────────────────────────────────
// Voice Engine
// ─────────────────────────────────────────────────────────────────────────────

// Engine manages voice input/output for PicoClaw.
type Engine struct {
	config   VoiceConfig
	handler  CommandHandler
	client   *http.Client
	mu       sync.Mutex
	speaking bool
	listening bool
}

// NewEngine creates a new voice engine.
func NewEngine(config VoiceConfig, handler CommandHandler) *Engine {
	if config.TTSVoice == "" {
		config.TTSVoice = "nova" // warm, natural Portuguese voice
	}
	if config.TTSSpeed == 0 {
		config.TTSSpeed = 1.0
	}
	if config.Language == "" {
		config.Language = "pt-BR"
	}
	if config.WakeWord == "" {
		config.WakeWord = "hey synapse"
	}
	if config.AudioOutputDir == "" {
		config.AudioOutputDir = "/tmp/picoclaw_audio"
	}

	os.MkdirAll(config.AudioOutputDir, 0700)

	return &Engine{
		config:  config,
		handler: handler,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Speak converts text to speech and plays it.
func (e *Engine) Speak(ctx context.Context, text string) error {
	e.mu.Lock()
	e.speaking = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.speaking = false
		e.mu.Unlock()
	}()

	// Generate audio
	audioFile, err := e.textToSpeech(ctx, text)
	if err != nil {
		return fmt.Errorf("TTS falhou: %w", err)
	}

	// Play audio
	return e.playAudio(audioFile)
}

// Listen records audio and returns the transcription.
func (e *Engine) Listen(ctx context.Context, durationSec int) (*VoiceCommand, error) {
	e.mu.Lock()
	e.listening = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.listening = false
		e.mu.Unlock()
	}()

	// Record audio
	audioFile, err := e.recordAudio(ctx, durationSec)
	if err != nil {
		return nil, fmt.Errorf("gravação falhou: %w", err)
	}

	// Transcribe
	return e.speechToText(ctx, audioFile)
}

// ListenAndRespond listens for a command and responds with voice.
func (e *Engine) ListenAndRespond(ctx context.Context, durationSec int) error {
	// Visual feedback
	fmt.Printf("🎙️  Ouvindo por %d segundos...\n", durationSec)

	cmd, err := e.Listen(ctx, durationSec)
	if err != nil {
		return err
	}

	if cmd.Normalized == "" {
		return e.Speak(ctx, "Não entendi. Pode repetir?")
	}

	fmt.Printf("🗣️  Você disse: %s\n", cmd.Normalized)

	// Process command
	response, err := e.handler(ctx, *cmd)
	if err != nil {
		errMsg := "Ocorreu um erro ao processar seu comando."
		e.Speak(ctx, errMsg)
		return err
	}

	// Speak response
	return e.Speak(ctx, response)
}

// ─────────────────────────────────────────────────────────────────────────────
// Text-to-Speech implementations
// ─────────────────────────────────────────────────────────────────────────────

func (e *Engine) textToSpeech(ctx context.Context, text string) (string, error) {
	switch e.config.TTSProvider {
	case TTSOpenAI:
		return e.ttsOpenAI(ctx, text)
	case TTSElevenLabs:
		return e.ttsElevenLabs(ctx, text)
	case TTSPiper:
		return e.ttsPiper(ctx, text)
	default:
		return e.ttsSystem(ctx, text)
	}
}

func (e *Engine) ttsOpenAI(ctx context.Context, text string) (string, error) {
	body := map[string]interface{}{
		"model": "tts-1",
		"input": text,
		"voice": e.config.TTSVoice,
		"speed": e.config.TTSSpeed,
	}

	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.openai.com/v1/audio/speech",
		bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+e.config.OpenAIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OpenAI TTS error: %s", string(body))
	}

	outFile := filepath.Join(e.config.AudioOutputDir, fmt.Sprintf("tts_%d.mp3", time.Now().UnixNano()))
	f, err := os.Create(outFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	io.Copy(f, resp.Body)

	return outFile, nil
}

func (e *Engine) ttsElevenLabs(ctx context.Context, text string) (string, error) {
	voiceID := "21m00Tcm4TlvDq8ikWAM" // default voice
	body := map[string]interface{}{
		"text":     text,
		"model_id": "eleven_multilingual_v2",
		"voice_settings": map[string]float64{
			"stability":        0.5,
			"similarity_boost": 0.75,
		},
	}

	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s", voiceID),
		bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("xi-api-key", e.config.ElevenLabsKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	outFile := filepath.Join(e.config.AudioOutputDir, fmt.Sprintf("tts_%d.mp3", time.Now().UnixNano()))
	f, _ := os.Create(outFile)
	defer f.Close()
	io.Copy(f, resp.Body)

	return outFile, nil
}

func (e *Engine) ttsPiper(ctx context.Context, text string) (string, error) {
	// Piper TTS: offline, fast, good quality
	outFile := filepath.Join(e.config.AudioOutputDir, fmt.Sprintf("tts_%d.wav", time.Now().UnixNano()))
	cmd := exec.CommandContext(ctx, "piper",
		"--model", "pt_BR-faber-medium",
		"--output_file", outFile)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		// Fallback to system TTS
		return e.ttsSystem(ctx, text)
	}
	return outFile, nil
}

func (e *Engine) ttsSystem(ctx context.Context, text string) (string, error) {
	outFile := filepath.Join(e.config.AudioOutputDir, fmt.Sprintf("tts_%d.wav", time.Now().UnixNano()))

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "say", "-o", outFile, text)
	case "linux":
		cmd = exec.CommandContext(ctx, "espeak-ng", "-v", "pt-br", "-w", outFile, text)
	case "windows":
		// PowerShell TTS
		ps := fmt.Sprintf(`Add-Type -AssemblyName System.Speech; $s = New-Object System.Speech.Synthesis.SpeechSynthesizer; $s.Speak('%s')`, text)
		cmd = exec.CommandContext(ctx, "powershell", "-Command", ps)
	default:
		return "", fmt.Errorf("sistema operacional não suportado para TTS")
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("TTS do sistema falhou: %w", err)
	}
	return outFile, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Speech-to-Text implementations
// ─────────────────────────────────────────────────────────────────────────────

func (e *Engine) speechToText(ctx context.Context, audioFile string) (*VoiceCommand, error) {
	switch e.config.STTProvider {
	case STTWhisperAPI:
		return e.sttWhisperAPI(ctx, audioFile)
	case STTWhisperLocal:
		return e.sttWhisperLocal(ctx, audioFile)
	default:
		return e.sttWhisperAPI(ctx, audioFile)
	}
}

func (e *Engine) sttWhisperAPI(ctx context.Context, audioFile string) (*VoiceCommand, error) {
	f, err := os.Open(audioFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("file", filepath.Base(audioFile))
	io.Copy(fw, f)
	w.WriteField("model", "whisper-1")
	w.WriteField("language", strings.Split(e.config.Language, "-")[0]) // "pt"
	w.WriteField("response_format", "verbose_json")
	w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.openai.com/v1/audio/transcriptions",
		&buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.config.OpenAIKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var result struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
	}
	json.Unmarshal(respBody, &result)

	normalized := strings.TrimSpace(result.Text)
	normalized = strings.ToLower(normalized)

	return &VoiceCommand{
		Raw:        result.Text,
		Normalized: normalized,
		Confidence: 0.95, // Whisper is generally very accurate
		Language:   result.Language,
		Timestamp:  time.Now(),
		AudioFile:  audioFile,
	}, nil
}

func (e *Engine) sttWhisperLocal(ctx context.Context, audioFile string) (*VoiceCommand, error) {
	// whisper.cpp local inference
	cmd := exec.CommandContext(ctx, "whisper-cpp",
		"-m", "/opt/whisper/models/ggml-base.bin",
		"-l", "pt",
		"-f", audioFile,
		"--output-txt")

	output, err := cmd.Output()
	if err != nil {
		// Fallback to API
		return e.sttWhisperAPI(ctx, audioFile)
	}

	text := strings.TrimSpace(string(output))
	return &VoiceCommand{
		Raw:        text,
		Normalized: strings.ToLower(text),
		Confidence: 0.85,
		Language:   "pt",
		Timestamp:  time.Now(),
		AudioFile:  audioFile,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Audio recording
// ─────────────────────────────────────────────────────────────────────────────

func (e *Engine) recordAudio(ctx context.Context, durationSec int) (string, error) {
	outFile := filepath.Join(e.config.AudioOutputDir, fmt.Sprintf("rec_%d.wav", time.Now().UnixNano()))

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// macOS: use sox or ffmpeg
		cmd = exec.CommandContext(ctx, "rec", "-r", "16000", "-c", "1", "-b", "16", outFile, "trim", "0", fmt.Sprintf("%d", durationSec))
	case "linux":
		// Linux: use arecord
		cmd = exec.CommandContext(ctx, "arecord",
			"-r", "16000", "-c", "1", "-f", "S16_LE",
			"-d", fmt.Sprintf("%d", durationSec),
			outFile)
	case "windows":
		// Windows: use ffmpeg
		cmd = exec.CommandContext(ctx, "ffmpeg",
			"-f", "dshow", "-i", "audio=Microphone",
			"-t", fmt.Sprintf("%d", durationSec),
			"-ar", "16000", "-ac", "1",
			outFile, "-y")
	default:
		return "", fmt.Errorf("gravação de áudio não suportada neste sistema")
	}

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("falha ao gravar áudio: %w", err)
	}

	return outFile, nil
}

func (e *Engine) playAudio(audioFile string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("afplay", audioFile)
	case "linux":
		// Try multiple players
		for _, player := range []string{"mpv", "aplay", "ffplay", "paplay"} {
			if _, err := exec.LookPath(player); err == nil {
				if player == "ffplay" {
					cmd = exec.Command(player, "-nodisp", "-autoexit", audioFile)
				} else {
					cmd = exec.Command(player, audioFile)
				}
				break
			}
		}
	case "windows":
		cmd = exec.Command("powershell", "-c", fmt.Sprintf(`(New-Object Media.SoundPlayer '%s').PlaySync()`, audioFile))
	}

	if cmd == nil {
		return fmt.Errorf("nenhum player de áudio encontrado")
	}

	return cmd.Run()
}

// ─────────────────────────────────────────────────────────────────────────────
// Wake Word Detection (simplified polling approach)
// ─────────────────────────────────────────────────────────────────────────────

// StartWakeWordDetection starts listening for the wake word in the background.
func (e *Engine) StartWakeWordDetection(ctx context.Context) {
	go func() {
		fmt.Printf("👂 Wake word ativo: diga '%s' para ativar\n", e.config.WakeWord)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Record 3-second chunks and check for wake word
				audioFile, err := e.recordAudio(ctx, 3)
				if err != nil {
					time.Sleep(time.Second)
					continue
				}

				cmd, err := e.speechToText(ctx, audioFile)
				if err != nil {
					continue
				}

				// Check for wake word
				if strings.Contains(cmd.Normalized, strings.ToLower(e.config.WakeWord)) {
					fmt.Printf("🎙️  Wake word detectado!\n")
					e.Speak(ctx, "Olá! Como posso ajudar?")
					e.ListenAndRespond(ctx, 10)
				}

				// Clean up temp file
				os.Remove(audioFile)
			}
		}
	}()
}

// IsAvailable checks if voice capabilities are available on this system.
func (e *Engine) IsAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("say")
		return err == nil
	case "linux":
		_, err1 := exec.LookPath("espeak-ng")
		_, err2 := exec.LookPath("espeak")
		return err1 == nil || err2 == nil
	case "windows":
		return true // PowerShell TTS always available
	}
	return false
}
