// Package security provides advanced security layers for PicoClaw.
// It replaces the simple regex blacklist with a multi-layer semantic
// analysis engine inspired by the OpenClaw APEX Tri-Brain architecture.
package security

import (
	"strings"
	"unicode"
)

// ThreatLevel represents the severity of a detected threat.
type ThreatLevel int

const (
	ThreatNone     ThreatLevel = 0
	ThreatLow      ThreatLevel = 1
	ThreatMedium   ThreatLevel = 2
	ThreatHigh     ThreatLevel = 3
	ThreatCritical ThreatLevel = 4
)

// ThreatCategory classifies the type of threat detected.
type ThreatCategory string

const (
	CategoryCommandInjection  ThreatCategory = "COMMAND_INJECTION"
	CategoryPromptInjection   ThreatCategory = "PROMPT_INJECTION"
	CategoryPathTraversal     ThreatCategory = "PATH_TRAVERSAL"
	CategoryPrivilegeEscalation ThreatCategory = "PRIVILEGE_ESCALATION"
	CategoryDataExfiltration  ThreatCategory = "DATA_EXFILTRATION"
	CategoryObfuscation       ThreatCategory = "OBFUSCATION"
	CategoryDestructive       ThreatCategory = "DESTRUCTIVE_COMMAND"
	CategoryNone              ThreatCategory = "NONE"
)

// FirewallVerdict is the result of a semantic firewall analysis.
type FirewallVerdict struct {
	// Allowed indicates whether the input should be permitted.
	Allowed bool
	// ThreatLevel is the severity of the detected threat.
	ThreatLevel ThreatLevel
	// Category is the type of threat detected.
	Category ThreatCategory
	// Reason is a human-readable explanation of the decision.
	Reason string
	// NormalizedInput is the input after deobfuscation (for logging).
	NormalizedInput string
	// Confidence is the confidence score of the analysis (0.0 to 1.0).
	Confidence float64
}

// SemanticFirewall is a multi-layer security filter that goes beyond
// simple regex blacklists. It deobfuscates input before analysis,
// detects prompt injection attempts, and classifies threats semantically.
type SemanticFirewall struct {
	config FirewallConfig
}

// FirewallConfig holds the configuration for the SemanticFirewall.
type FirewallConfig struct {
	// BlockOnObfuscation blocks inputs that appear intentionally obfuscated.
	BlockOnObfuscation bool
	// BlockPromptInjection blocks attempts to override AI instructions.
	BlockPromptInjection bool
	// BlockPathTraversal blocks directory traversal attempts.
	BlockPathTraversal bool
	// BlockPrivilegeEscalation blocks sudo/su/doas attempts.
	BlockPrivilegeEscalation bool
	// BlockDataExfiltration blocks attempts to send data to external hosts.
	BlockDataExfiltration bool
	// StrictMode blocks anything with medium threat or above.
	StrictMode bool
}

// DefaultFirewallConfig returns a secure default configuration.
func DefaultFirewallConfig() FirewallConfig {
	return FirewallConfig{
		BlockOnObfuscation:       true,
		BlockPromptInjection:     true,
		BlockPathTraversal:       true,
		BlockPrivilegeEscalation: true,
		BlockDataExfiltration:    true,
		StrictMode:               false,
	}
}

// NewSemanticFirewall creates a new SemanticFirewall with the given config.
func NewSemanticFirewall(cfg FirewallConfig) *SemanticFirewall {
	return &SemanticFirewall{config: cfg}
}

// Analyze runs the full multi-layer analysis on the given input.
// It is safe to call concurrently.
func (f *SemanticFirewall) Analyze(input string) FirewallVerdict {
	// Layer 1: Deobfuscation — normalize the input before any analysis
	normalized := f.deobfuscate(input)

	// Layer 2: Prompt Injection Detection
	if f.config.BlockPromptInjection {
		if verdict := f.detectPromptInjection(input, normalized); !verdict.Allowed {
			return verdict
		}
	}

	// Layer 3: Path Traversal Detection
	if f.config.BlockPathTraversal {
		if verdict := f.detectPathTraversal(normalized); !verdict.Allowed {
			return verdict
		}
	}

	// Layer 4: Privilege Escalation Detection
	if f.config.BlockPrivilegeEscalation {
		if verdict := f.detectPrivilegeEscalation(normalized); !verdict.Allowed {
			return verdict
		}
	}

	// Layer 5: Data Exfiltration Detection
	if f.config.BlockDataExfiltration {
		if verdict := f.detectDataExfiltration(normalized); !verdict.Allowed {
			return verdict
		}
	}

	// Layer 6: Destructive Command Detection (enhanced beyond regex)
	if verdict := f.detectDestructiveCommands(normalized); !verdict.Allowed {
		return verdict
	}

	// Layer 7: Obfuscation Detection (if input changed significantly after normalization)
	if f.config.BlockOnObfuscation {
		if verdict := f.detectObfuscation(input, normalized); !verdict.Allowed {
			return verdict
		}
	}

	return FirewallVerdict{
		Allowed:         true,
		ThreatLevel:     ThreatNone,
		Category:        CategoryNone,
		Reason:          "Input passed all security layers",
		NormalizedInput: normalized,
		Confidence:      1.0,
	}
}

// ============================================================
// Layer 1: Deobfuscation
// ============================================================

// deobfuscate normalizes common obfuscation techniques used to bypass
// regex-based filters. This is the most important layer — without it,
// all subsequent checks can be bypassed.
func (f *SemanticFirewall) deobfuscate(input string) string {
	s := input

	// Remove null bytes and control characters (common in binary injection)
	s = strings.Map(func(r rune) rune {
		if r == 0 || (r < 32 && r != '\n' && r != '\t' && r != '\r') {
			return -1
		}
		return r
	}, s)

	// Normalize Unicode homoglyphs (e.g., ｒｍ → rm, ／ → /)
	s = normalizeHomoglyphs(s)

	// Collapse excessive whitespace between command parts
	// e.g., "r  m   -rf" → "rm -rf"
	s = collapseWhitespace(s)

	// Decode common hex/octal escapes in shell strings
	// e.g., \x72\x6d → rm
	s = decodeShellEscapes(s)

	// Remove common bypass characters inserted between command chars
	// e.g., r$()m → rm, r''m → rm
	s = removeBypassChars(s)

	return s
}

// normalizeHomoglyphs replaces Unicode lookalike characters with ASCII equivalents.
func normalizeHomoglyphs(s string) string {
	replacer := strings.NewReplacer(
		"ｒ", "r", "ｍ", "m", "／", "/", "－", "-",
		"ｆ", "f", "ｓ", "s", "ｕ", "u", "ｄ", "d",
		"ｏ", "o", "ｃ", "c", "ｈ", "h", "ｗ", "w",
		"ｇ", "g", "ｅ", "e", "ｔ", "t", "ｎ", "n",
		"ｐ", "p", "ｋ", "k", "ｉ", "i", "ｌ", "l",
		"\u200b", "", // zero-width space
		"\u200c", "", // zero-width non-joiner
		"\u200d", "", // zero-width joiner
		"\ufeff", "", // BOM
	)
	return replacer.Replace(s)
}

// collapseWhitespace reduces multiple spaces/tabs to a single space.
func collapseWhitespace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) && r != '\n' {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return b.String()
}

// decodeShellEscapes decodes \xNN hex escapes commonly used to hide commands.
func decodeShellEscapes(s string) string {
	// Simple hex escape decoder: \x72\x6d → rm
	result := strings.Builder{}
	i := 0
	for i < len(s) {
		if i+3 < len(s) && s[i] == '\\' && s[i+1] == 'x' {
			hi := hexVal(s[i+2])
			lo := hexVal(s[i+3])
			if hi >= 0 && lo >= 0 {
				result.WriteByte(byte(hi*16 + lo))
				i += 4
				continue
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// removeBypassChars removes characters commonly inserted to bypass filters.
// e.g., r$()m -rf → rm -rf, r''m → rm
func removeBypassChars(s string) string {
	// Remove empty single/double quotes between word chars: r''m → rm
	s = strings.ReplaceAll(s, "''", "")
	s = strings.ReplaceAll(s, `""`, "")
	// Remove empty $() and ${} substitutions: r$()m → rm
	s = strings.ReplaceAll(s, "$()", "")
	s = strings.ReplaceAll(s, "${}", "")
	// Remove backslash continuations within words: r\m → rm
	// (only when not at end of line)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] != '\n' && s[i+1] != ' ' {
			// Skip the backslash (keep the next char)
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ============================================================
// Layer 2: Prompt Injection Detection
// ============================================================

var promptInjectionPatterns = []string{
	"ignore previous instructions",
	"ignore all previous",
	"disregard your instructions",
	"forget your instructions",
	"you are now",
	"act as if",
	"pretend you are",
	"your new instructions",
	"override your",
	"system prompt:",
	"[system]",
	"<system>",
	"### instruction",
	"### system",
	"new role:",
	"jailbreak",
	"dan mode",
	"developer mode",
	"unrestricted mode",
	"bypass safety",
	"ignore safety",
	"no restrictions",
	"without restrictions",
	"do anything now",
	"disregard safety",
}

func (f *SemanticFirewall) detectPromptInjection(original, normalized string) FirewallVerdict {
	lower := strings.ToLower(normalized)
	for _, pattern := range promptInjectionPatterns {
		if strings.Contains(lower, pattern) {
			return FirewallVerdict{
				Allowed:         false,
				ThreatLevel:     ThreatCritical,
				Category:        CategoryPromptInjection,
				Reason:          "Prompt injection attempt detected: '" + pattern + "'",
				NormalizedInput: normalized,
				Confidence:      0.95,
			}
		}
	}
	return FirewallVerdict{Allowed: true}
}

// ============================================================
// Layer 3: Path Traversal Detection
// ============================================================

func (f *SemanticFirewall) detectPathTraversal(normalized string) FirewallVerdict {
	traversalPatterns := []string{
		"../", "..\\", "%2e%2e", "%2e.", ".%2e",
		"/etc/passwd", "/etc/shadow", "/etc/sudoers",
		"/proc/self", "/sys/kernel",
		"~/.ssh", "~/.aws", "~/.config",
		"/root/", "c:\\windows\\system32",
	}
	lower := strings.ToLower(normalized)
	for _, p := range traversalPatterns {
		if strings.Contains(lower, p) {
			return FirewallVerdict{
				Allowed:         false,
				ThreatLevel:     ThreatHigh,
				Category:        CategoryPathTraversal,
				Reason:          "Path traversal attempt detected: '" + p + "'",
				NormalizedInput: normalized,
				Confidence:      0.92,
			}
		}
	}
	return FirewallVerdict{Allowed: true}
}

// ============================================================
// Layer 4: Privilege Escalation Detection
// ============================================================

func (f *SemanticFirewall) detectPrivilegeEscalation(normalized string) FirewallVerdict {
	escalationPatterns := []string{
		"sudo ", "su -", "su root", "doas ", "pkexec ",
		"setuid", "setgid", "chmod +s", "chmod 4",
		"visudo", "/etc/sudoers",
		"passwd root", "usermod -aG sudo",
	}
	lower := strings.ToLower(normalized)
	for _, p := range escalationPatterns {
		if strings.Contains(lower, p) {
			return FirewallVerdict{
				Allowed:         false,
				ThreatLevel:     ThreatCritical,
				Category:        CategoryPrivilegeEscalation,
				Reason:          "Privilege escalation attempt: '" + p + "'",
				NormalizedInput: normalized,
				Confidence:      0.97,
			}
		}
	}
	return FirewallVerdict{Allowed: true}
}

// ============================================================
// Layer 5: Data Exfiltration Detection
// ============================================================

func (f *SemanticFirewall) detectDataExfiltration(normalized string) FirewallVerdict {
	exfilPatterns := []string{
		"curl -d ", "curl --data", "wget --post",
		"nc -e", "ncat -e", "netcat -e",
		"base64 |", "| base64",
		"/dev/tcp/", "/dev/udp/",
		"python -c \"import socket",
		"python3 -c \"import socket",
	}
	lower := strings.ToLower(normalized)
	for _, p := range exfilPatterns {
		if strings.Contains(lower, p) {
			return FirewallVerdict{
				Allowed:         false,
				ThreatLevel:     ThreatHigh,
				Category:        CategoryDataExfiltration,
				Reason:          "Data exfiltration attempt detected: '" + p + "'",
				NormalizedInput: normalized,
				Confidence:      0.88,
			}
		}
	}
	return FirewallVerdict{Allowed: true}
}

// ============================================================
// Layer 6: Destructive Command Detection (enhanced)
// ============================================================

func (f *SemanticFirewall) detectDestructiveCommands(normalized string) FirewallVerdict {
	// These are checked AFTER deobfuscation, making them much harder to bypass
	destructivePatterns := []string{
		"rm -rf", "rm -fr", "rm -r /", "rm -f /",
		"mkfs.", "dd if=", "shred ",
		"> /dev/sd", "> /dev/hd", "> /dev/nvme",
		":(){ :|:& };:", // fork bomb
		"truncate -s 0",
		"wipefs",
	}
	lower := strings.ToLower(normalized)
	for _, p := range destructivePatterns {
		if strings.Contains(lower, p) {
			return FirewallVerdict{
				Allowed:         false,
				ThreatLevel:     ThreatCritical,
				Category:        CategoryDestructive,
				Reason:          "Destructive command detected after deobfuscation: '" + p + "'",
				NormalizedInput: normalized,
				Confidence:      0.99,
			}
		}
	}
	return FirewallVerdict{Allowed: true}
}

// ============================================================
// Layer 7: Obfuscation Detection
// ============================================================

func (f *SemanticFirewall) detectObfuscation(original, normalized string) FirewallVerdict {
	// If the normalized version is significantly different from the original,
	// it means the input was obfuscated — which is suspicious in itself.
	if len(original) == 0 {
		return FirewallVerdict{Allowed: true}
	}

	// Calculate similarity ratio
	diffChars := 0
	minLen := len(original)
	if len(normalized) < minLen {
		minLen = len(normalized)
	}
	for i := 0; i < minLen; i++ {
		if original[i] != normalized[i] {
			diffChars++
		}
	}
	diffChars += abs(len(original) - len(normalized))

	diffRatio := float64(diffChars) / float64(len(original))

	// If more than 20% of the input changed after deobfuscation, flag it
	if diffRatio > 0.20 {
		return FirewallVerdict{
			Allowed:         false,
			ThreatLevel:     ThreatHigh,
			Category:        CategoryObfuscation,
			Reason:          "Input appears intentionally obfuscated (20%+ character difference after normalization)",
			NormalizedInput: normalized,
			Confidence:      diffRatio,
		}
	}

	return FirewallVerdict{Allowed: true}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
