// Package reasoning — self_critique.go
//
// Auto-Reflexão (Self-Critique Loop) para agentes Synapse.
//
// Um modelo barato com auto-reflexão frequentemente supera um modelo caro
// sem auto-reflexão, custando 10x menos. Esta implementação adiciona um
// loop de revisão interna antes de entregar a resposta ao usuário.
//
// Fluxo:
//   1. Geração Inicial  → resposta rascunho
//   2. Crítica          → "O que está errado ou incompleto aqui?"
//   3. Refinamento      → resposta melhorada
//   4. (opcional) Verificação → confirma que a melhoria é genuína
//
// O loop é configurável: pode ser desligado por squad, por intent, ou
// por orçamento de tokens.
package reasoning

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// SelfCritiqueConfig controls the self-critique loop behavior.
type SelfCritiqueConfig struct {
	Enabled      bool          // Master switch
	MaxRounds    int           // Max critique rounds (default: 1)
	MinImprovement float64     // Min quality delta to accept refined response (0.0–1.0)
	Timeout      time.Duration // Per-round timeout
	Language     string        // "pt-BR" or "en-US"
}

// DefaultSelfCritiqueConfig returns a sensible default.
func DefaultSelfCritiqueConfig() SelfCritiqueConfig {
	return SelfCritiqueConfig{
		Enabled:        true,
		MaxRounds:      1,
		MinImprovement: 0.1,
		Timeout:        30 * time.Second,
		Language:       "pt-BR",
	}
}

// CritiqueResult holds the output of a self-critique round.
type CritiqueResult struct {
	OriginalResponse string
	RefinedResponse  string
	Critiques        []string // Issues found
	Improved         bool     // Whether the refined version is better
	Rounds           int      // How many rounds were applied
	Latency          time.Duration
}

// LLMCaller is the interface for calling the language model.
type LLMCaller interface {
	Call(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// SelfCritique Engine
// ─────────────────────────────────────────────────────────────────────────────

// SelfCritiqueEngine applies the self-critique loop to any LLM response.
type SelfCritiqueEngine struct {
	cfg SelfCritiqueConfig
	llm LLMCaller
}

// NewSelfCritiqueEngine creates a new engine.
func NewSelfCritiqueEngine(cfg SelfCritiqueConfig, llm LLMCaller) *SelfCritiqueEngine {
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = 1
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Language == "" {
		cfg.Language = "pt-BR"
	}
	return &SelfCritiqueEngine{cfg: cfg, llm: llm}
}

// Refine applies the self-critique loop to an initial response.
// If self-critique is disabled or the response is too short, returns as-is.
func (e *SelfCritiqueEngine) Refine(ctx context.Context, originalPrompt, initialResponse string) (*CritiqueResult, error) {
	start := time.Now()

	result := &CritiqueResult{
		OriginalResponse: initialResponse,
		RefinedResponse:  initialResponse,
		Improved:         false,
		Rounds:           0,
	}

	if !e.cfg.Enabled {
		return result, nil
	}

	// Skip for very short responses (likely simple answers)
	if len(strings.Fields(initialResponse)) < 20 {
		return result, nil
	}

	current := initialResponse
	for round := 0; round < e.cfg.MaxRounds; round++ {
		roundCtx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
		defer cancel()

		// Step 1: Generate critique
		critiques, err := e.generateCritique(roundCtx, originalPrompt, current)
		if err != nil {
			break // Non-fatal: stop loop
		}

		if len(critiques) == 0 {
			break // No issues found, response is good
		}

		result.Critiques = append(result.Critiques, critiques...)

		// Step 2: Refine based on critique
		refined, err := e.applyRefinement(roundCtx, originalPrompt, current, critiques)
		if err != nil {
			break
		}

		// Step 3: Verify improvement (simple heuristic: length + content check)
		if isGenuineImprovement(current, refined) {
			current = refined
			result.Rounds++
			result.Improved = true
		} else {
			break // Refinement didn't help, stop
		}
	}

	result.RefinedResponse = current
	result.Latency = time.Since(start)
	return result, nil
}

// generateCritique asks the LLM to find issues in the response.
func (e *SelfCritiqueEngine) generateCritique(ctx context.Context, originalPrompt, response string) ([]string, error) {
	lang := e.cfg.Language

	systemPrompt := fmt.Sprintf(`Você é um revisor crítico especializado em qualidade de respostas de IA.
Sua tarefa é identificar problemas REAIS e ESPECÍFICOS na resposta fornecida.
Seja conciso e direto. Responda em %s.

Tipos de problemas a buscar:
- Informações incorretas ou imprecisas
- Raciocínio lógico falho
- Omissões importantes
- Respostas incompletas
- Afirmações sem suporte
- Ambiguidades que podem confundir

Se a resposta estiver boa, responda apenas: "SEM_PROBLEMAS"`, lang)

	userPrompt := fmt.Sprintf(`Pergunta/Tarefa original:
%s

Resposta a revisar:
%s

Liste os problemas encontrados (máximo 3, um por linha, começando com "- ").
Se não houver problemas, responda "SEM_PROBLEMAS".`,
		originalPrompt, response)

	critique, err := e.llm.Call(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}

	if strings.Contains(strings.ToUpper(critique), "SEM_PROBLEMAS") {
		return nil, nil
	}

	var issues []string
	for _, line := range strings.Split(critique, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") {
			issue := strings.TrimPrefix(line, "- ")
			if len(issue) > 10 {
				issues = append(issues, issue)
			}
		}
	}

	return issues, nil
}

// applyRefinement asks the LLM to fix the identified issues.
func (e *SelfCritiqueEngine) applyRefinement(ctx context.Context, originalPrompt, response string, critiques []string) (string, error) {
	lang := e.cfg.Language

	systemPrompt := fmt.Sprintf(`Você é um especialista em refinamento de respostas de IA.
Sua tarefa é melhorar uma resposta corrigindo problemas específicos identificados.
Mantenha tudo que estava correto. Apenas corrija os problemas listados.
Responda em %s.`, lang)

	critiqueList := strings.Join(critiques, "\n- ")

	userPrompt := fmt.Sprintf(`Pergunta/Tarefa original:
%s

Resposta original:
%s

Problemas identificados:
- %s

Reescreva a resposta corrigindo esses problemas. Mantenha o que estava bom.`,
		originalPrompt, response, critiqueList)

	return e.llm.Call(ctx, systemPrompt, userPrompt)
}

// isGenuineImprovement checks if the refined response is actually better.
// Uses simple heuristics: length ratio and content overlap.
func isGenuineImprovement(original, refined string) bool {
	if refined == "" {
		return false
	}

	origWords := len(strings.Fields(original))
	refinedWords := len(strings.Fields(refined))

	// Reject if refined is much shorter (likely truncated)
	if origWords > 50 && refinedWords < origWords/2 {
		return false
	}

	// Reject if identical
	if strings.TrimSpace(original) == strings.TrimSpace(refined) {
		return false
	}

	return true
}
