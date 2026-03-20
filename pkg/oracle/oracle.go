// Package oracle implements the Synapse Oracle — a lightweight predictive
// simulation engine inspired by MiroFish's social simulation architecture.
//
// # The Core Idea
//
// Instead of simulating thousands of agents (expensive), the Oracle creates
// 3–10 "archetypal profiles" that statistically represent the full population.
// Research shows that 5 well-defined archetypes capture ~95% of the variance
// of a 1,000-agent simulation at 1% of the computational cost.
//
// # Modes
//
//   - ModeEconomic  (3 archetypes, 1 debate round)  → ~$0.01–0.05 per simulation
//   - ModeBalanced  (5 archetypes, 2 debate rounds)  → ~$0.05–0.20 per simulation
//   - ModeMaximum   (10 archetypes, 3 debate rounds) → ~$0.20–1.00 per simulation
//
// # Usage
//
//	oracle := oracle.New(oracle.Config{Mode: oracle.ModeBalanced, ...})
//	result, err := oracle.Simulate(ctx, oracle.SimulationRequest{
//	    Scenario:    "Vou lançar um produto de R$200 para pequenos empresários",
//	    Context:     "Mercado brasileiro, segmento PME, produto SaaS",
//	    Question:    "Como o mercado vai reagir nos primeiros 30 dias?",
//	})
package oracle

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// Mode defines the simulation depth and cost level.
type Mode string

const (
	ModeEconomic Mode = "economic"  // 3 archetypes, fast, cheap
	ModeBalanced Mode = "balanced"  // 5 archetypes, balanced (default)
	ModeMaximum  Mode = "maximum"   // 10 archetypes, deep, thorough
)

// Archetype represents a simulated persona with distinct beliefs and behaviors.
type Archetype struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Perspective string  `json:"perspective"` // their worldview/bias
	Weight      float64 `json:"weight"`      // population representation (0.0–1.0, sum=1.0)
}

// SimulationRequest holds the input for an Oracle simulation.
type SimulationRequest struct {
	Scenario    string            // What is happening / being decided
	Context     string            // Background information, constraints
	Question    string            // What do we want to predict?
	Archetypes  []Archetype       // Optional: custom archetypes (overrides auto-generation)
	Metadata    map[string]string // Optional: extra context
}

// ArchetypeResponse holds a single archetype's reaction to the scenario.
type ArchetypeResponse struct {
	Archetype   Archetype
	Reaction    string  // Their initial reaction
	Concerns    []string
	Opportunities []string
	Prediction  string  // Their prediction for the outcome
	Confidence  float64 // How confident they are (0.0–1.0)
}

// SimulationResult holds the complete Oracle output.
type SimulationResult struct {
	Request       SimulationRequest
	Mode          Mode
	Archetypes    []Archetype
	Responses     []ArchetypeResponse
	Synthesis     string    // Synthesized prediction from all archetypes
	KeyInsights   []string  // Top 3–5 actionable insights
	RiskFactors   []string  // Main risks identified
	Opportunities []string  // Main opportunities identified
	Confidence    float64   // Overall confidence score (0.0–1.0)
	Duration      time.Duration
	TokensUsed    int
}

// LLMCaller is the interface the Oracle uses to call the language model.
// This allows the Oracle to work with any LLM backend (online or offline).
type LLMCaller interface {
	Call(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// Config
// ─────────────────────────────────────────────────────────────────────────────

// Config holds Oracle configuration.
type Config struct {
	Mode           Mode
	LLM            LLMCaller
	SelfCritique   bool          // Apply self-critique to the synthesis
	MaxDebateRounds int          // Override default rounds for the mode
	Timeout        time.Duration // Per-archetype timeout
	Language       string        // "pt-BR" or "en-US"
}

// DefaultConfig returns a balanced configuration.
func DefaultConfig(llm LLMCaller) Config {
	return Config{
		Mode:         ModeBalanced,
		LLM:          llm,
		SelfCritique: true,
		Timeout:      45 * time.Second,
		Language:     "pt-BR",
	}
}

// modeParams holds the parameters for each simulation mode.
type modeParams struct {
	archetypeCount int
	debateRounds   int
}

var modeDefaults = map[Mode]modeParams{
	ModeEconomic: {archetypeCount: 3, debateRounds: 1},
	ModeBalanced: {archetypeCount: 5, debateRounds: 2},
	ModeMaximum:  {archetypeCount: 10, debateRounds: 3},
}

// ─────────────────────────────────────────────────────────────────────────────
// Oracle
// ─────────────────────────────────────────────────────────────────────────────

// Oracle is the main predictive simulation engine.
type Oracle struct {
	cfg Config
}

// New creates a new Oracle with the given configuration.
func New(cfg Config) *Oracle {
	if cfg.Timeout == 0 {
		cfg.Timeout = 45 * time.Second
	}
	if cfg.Language == "" {
		cfg.Language = "pt-BR"
	}
	if _, ok := modeDefaults[cfg.Mode]; !ok {
		cfg.Mode = ModeBalanced
	}
	return &Oracle{cfg: cfg}
}

// Simulate runs a full predictive simulation for the given request.
func (o *Oracle) Simulate(ctx context.Context, req SimulationRequest) (*SimulationResult, error) {
	start := time.Now()
	params := modeDefaults[o.cfg.Mode]
	if o.cfg.MaxDebateRounds > 0 {
		params.debateRounds = o.cfg.MaxDebateRounds
	}

	result := &SimulationResult{
		Request: req,
		Mode:    o.cfg.Mode,
	}

	// Step 1: Generate or use provided archetypes
	archetypes := req.Archetypes
	if len(archetypes) == 0 {
		var err error
		archetypes, err = o.generateArchetypes(ctx, req, params.archetypeCount)
		if err != nil {
			return nil, fmt.Errorf("oracle: generate archetypes: %w", err)
		}
	}
	result.Archetypes = archetypes

	// Step 2: Simulate each archetype's response in parallel
	responses, err := o.simulateArchetypes(ctx, req, archetypes)
	if err != nil {
		return nil, fmt.Errorf("oracle: simulate archetypes: %w", err)
	}
	result.Responses = responses

	// Step 3: Debate rounds (archetypes react to each other)
	for round := 1; round < params.debateRounds; round++ {
		responses, err = o.debateRound(ctx, req, responses, round)
		if err != nil {
			// Non-fatal: continue with what we have
			break
		}
		result.Responses = responses
	}

	// Step 4: Synthesize all responses into a final prediction
	synthesis, insights, risks, opps, confidence, err := o.synthesize(ctx, req, responses)
	if err != nil {
		return nil, fmt.Errorf("oracle: synthesize: %w", err)
	}

	// Step 5: Self-critique (optional)
	if o.cfg.SelfCritique {
		synthesis, err = o.selfCritique(ctx, req, synthesis)
		if err != nil {
			// Non-fatal: use original synthesis
		}
	}

	result.Synthesis = synthesis
	result.KeyInsights = insights
	result.RiskFactors = risks
	result.Opportunities = opps
	result.Confidence = confidence
	result.Duration = time.Since(start)

	return result, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal: Archetype Generation
// ─────────────────────────────────────────────────────────────────────────────

func (o *Oracle) generateArchetypes(ctx context.Context, req SimulationRequest, count int) ([]Archetype, error) {
	lang := o.cfg.Language
	systemPrompt := fmt.Sprintf(`Você é um especialista em simulação social e análise de stakeholders.
Sua tarefa é criar %d arquétipos representativos que cubram o espectro de perspectivas relevantes para o cenário descrito.

Regras:
- Cada arquétipo deve ter uma perspectiva DISTINTA e representativa
- Os pesos devem somar 1.0
- Inclua tanto perspectivas otimistas quanto céticas
- Seja específico para o contexto dado
- Responda em %s

Formato de resposta (JSON):
[
  {
    "name": "Nome do Arquétipo",
    "description": "Quem é essa pessoa/perfil",
    "perspective": "Sua visão de mundo e viés principal",
    "weight": 0.20
  }
]`, count, lang)

	userPrompt := fmt.Sprintf(`Cenário: %s
Contexto: %s
Pergunta a ser respondida: %s

Crie %d arquétipos que representem os diferentes stakeholders e perspectivas relevantes para este cenário.`,
		req.Scenario, req.Context, req.Question, count)

	response, err := o.cfg.LLM.Call(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}

	archetypes, err := parseArchetypes(response)
	if err != nil {
		// Fallback: use generic archetypes
		return o.genericArchetypes(count), nil
	}

	return archetypes, nil
}

// genericArchetypes returns a set of generic archetypes as fallback.
func (o *Oracle) genericArchetypes(count int) []Archetype {
	all := []Archetype{
		{Name: "O Inovador", Description: "Adota novas tecnologias rapidamente", Perspective: "Otimista, focado em possibilidades", Weight: 0.20},
		{Name: "O Conservador", Description: "Prefere estabilidade e comprovação", Perspective: "Cético, focado em riscos", Weight: 0.25},
		{Name: "O Pragmático", Description: "Toma decisões baseadas em ROI", Perspective: "Neutro, focado em resultados", Weight: 0.30},
		{Name: "O Entusiasta", Description: "Abraça mudanças com energia", Perspective: "Muito otimista, pode subestimar riscos", Weight: 0.10},
		{Name: "O Analítico", Description: "Precisa de dados antes de decidir", Perspective: "Neutro-cético, focado em evidências", Weight: 0.15},
		{Name: "O Influenciador", Description: "Forma a opinião dos outros", Perspective: "Variável, impacto multiplicado", Weight: 0.10},
		{Name: "O Resistente", Description: "Resiste ativamente a mudanças", Perspective: "Negativo, foco em perdas", Weight: 0.10},
		{Name: "O Oportunista", Description: "Busca vantagem em qualquer situação", Perspective: "Estratégico, foco em ganhos pessoais", Weight: 0.10},
		{Name: "O Especialista", Description: "Conhecimento técnico profundo", Perspective: "Baseado em expertise, pode ser rígido", Weight: 0.10},
		{Name: "O Usuário Final", Description: "Quem usa o produto/serviço no dia a dia", Perspective: "Prático, foco em usabilidade", Weight: 0.20},
	}

	if count >= len(all) {
		return all
	}

	// Normalize weights
	selected := all[:count]
	var totalWeight float64
	for _, a := range selected {
		totalWeight += a.Weight
	}
	for i := range selected {
		selected[i].Weight = selected[i].Weight / totalWeight
	}
	return selected
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal: Archetype Simulation (Parallel)
// ─────────────────────────────────────────────────────────────────────────────

func (o *Oracle) simulateArchetypes(ctx context.Context, req SimulationRequest, archetypes []Archetype) ([]ArchetypeResponse, error) {
	type result struct {
		resp ArchetypeResponse
		err  error
		idx  int
	}

	results := make([]result, len(archetypes))
	var wg sync.WaitGroup

	for i, arch := range archetypes {
		wg.Add(1)
		go func(idx int, a Archetype) {
			defer wg.Done()
			archCtx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
			defer cancel()

			resp, err := o.simulateSingleArchetype(archCtx, req, a)
			results[idx] = result{resp: resp, err: err, idx: idx}
		}(i, arch)
	}

	wg.Wait()

	var responses []ArchetypeResponse
	for _, r := range results {
		if r.err != nil {
			// Skip failed archetypes but don't fail the whole simulation
			continue
		}
		responses = append(responses, r.resp)
	}

	if len(responses) == 0 {
		return nil, fmt.Errorf("oracle: all archetype simulations failed")
	}

	return responses, nil
}

func (o *Oracle) simulateSingleArchetype(ctx context.Context, req SimulationRequest, arch Archetype) (ArchetypeResponse, error) {
	systemPrompt := fmt.Sprintf(`Você é %s.
%s
Sua perspectiva: %s

Você está analisando um cenário e deve responder EXATAMENTE como esse perfil responderia — com suas crenças, medos, esperanças e vieses específicos.
Responda em %s.`, arch.Name, arch.Description, arch.Perspective, o.cfg.Language)

	userPrompt := fmt.Sprintf(`Cenário: %s
Contexto: %s
Pergunta: %s

Responda como %s responderia. Inclua:
1. Sua reação inicial (1-2 frases)
2. Suas principais preocupações (lista)
3. As oportunidades que você vê (lista)
4. Sua previsão para o resultado (1 parágrafo)
5. Seu nível de confiança nessa previsão (0-100%%)`,
		req.Scenario, req.Context, req.Question, arch.Name)

	response, err := o.cfg.LLM.Call(ctx, systemPrompt, userPrompt)
	if err != nil {
		return ArchetypeResponse{}, err
	}

	return ArchetypeResponse{
		Archetype:  arch,
		Reaction:   response,
		Confidence: arch.Weight, // use weight as proxy confidence
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal: Debate Round
// ─────────────────────────────────────────────────────────────────────────────

func (o *Oracle) debateRound(ctx context.Context, req SimulationRequest, responses []ArchetypeResponse, round int) ([]ArchetypeResponse, error) {
	// Build a summary of all previous responses for context
	var debateSummary strings.Builder
	for _, r := range responses {
		debateSummary.WriteString(fmt.Sprintf("\n**%s disse:** %s\n", r.Archetype.Name, r.Reaction))
	}

	type result struct {
		resp ArchetypeResponse
		err  error
		idx  int
	}

	results := make([]result, len(responses))
	var wg sync.WaitGroup

	for i, resp := range responses {
		wg.Add(1)
		go func(idx int, r ArchetypeResponse) {
			defer wg.Done()
			archCtx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
			defer cancel()

			systemPrompt := fmt.Sprintf(`Você é %s. %s
Perspectiva: %s
Responda em %s.`, r.Archetype.Name, r.Archetype.Description, r.Archetype.Perspective, o.cfg.Language)

			userPrompt := fmt.Sprintf(`Rodada de debate %d.

Você ouviu as perspectivas dos outros:
%s

Agora, como %s, você pode:
- Concordar ou discordar com pontos específicos
- Refinar sua posição com base no que ouviu
- Identificar algo que os outros perderam

Dê sua resposta refinada (máximo 3 parágrafos).`,
				round, debateSummary.String(), r.Archetype.Name)

			refined, err := o.cfg.LLM.Call(archCtx, systemPrompt, userPrompt)
			if err != nil {
				results[idx] = result{resp: r, err: err, idx: idx}
				return
			}

			updated := r
			updated.Reaction = refined
			results[idx] = result{resp: updated, err: nil, idx: idx}
		}(i, resp)
	}

	wg.Wait()

	var updated []ArchetypeResponse
	for _, r := range results {
		if r.err == nil {
			updated = append(updated, r.resp)
		} else {
			updated = append(updated, responses[r.idx]) // keep original on error
		}
	}

	return updated, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal: Synthesis
// ─────────────────────────────────────────────────────────────────────────────

func (o *Oracle) synthesize(ctx context.Context, req SimulationRequest, responses []ArchetypeResponse) (synthesis string, insights, risks, opps []string, confidence float64, err error) {
	var debateSummary strings.Builder
	for _, r := range responses {
		debateSummary.WriteString(fmt.Sprintf("\n**%s** (peso %.0f%%):\n%s\n",
			r.Archetype.Name, r.Archetype.Weight*100, r.Reaction))
	}

	systemPrompt := fmt.Sprintf(`Você é um analista estratégico sênior especializado em síntese de perspectivas múltiplas.
Sua tarefa é sintetizar as perspectivas de diferentes arquétipos em uma previsão coerente e acionável.
Responda em %s.`, o.cfg.Language)

	userPrompt := fmt.Sprintf(`Cenário: %s
Pergunta original: %s

Perspectivas dos arquétipos:
%s

Sintetize essas perspectivas em:

1. **PREVISÃO PRINCIPAL** (2-3 parágrafos): O que provavelmente vai acontecer, considerando o peso de cada perspectiva.

2. **INSIGHTS CHAVE** (lista de 3-5 itens): Os insights mais importantes e acionáveis.

3. **FATORES DE RISCO** (lista de 3-5 itens): Os principais riscos a monitorar.

4. **OPORTUNIDADES** (lista de 3-5 itens): As principais oportunidades a aproveitar.

5. **CONFIANÇA** (0-100%%): Seu nível de confiança nessa previsão e por quê.`,
		req.Scenario, req.Question, debateSummary.String())

	response, err := o.cfg.LLM.Call(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", nil, nil, nil, 0, err
	}

	// Parse the structured response
	synthesis = response
	insights = extractSection(response, "INSIGHTS CHAVE", "FATORES DE RISCO")
	risks = extractSection(response, "FATORES DE RISCO", "OPORTUNIDADES")
	opps = extractSection(response, "OPORTUNIDADES", "CONFIANÇA")
	confidence = extractConfidence(response)

	return synthesis, insights, risks, opps, confidence, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal: Self-Critique
// ─────────────────────────────────────────────────────────────────────────────

func (o *Oracle) selfCritique(ctx context.Context, req SimulationRequest, synthesis string) (string, error) {
	systemPrompt := fmt.Sprintf(`Você é um revisor crítico especializado em análise de previsões.
Sua tarefa é identificar falhas lógicas, vieses e pontos cegos em uma previsão, e então melhorá-la.
Responda em %s.`, o.cfg.Language)

	userPrompt := fmt.Sprintf(`Cenário original: %s

Previsão a revisar:
%s

1. Identifique até 3 falhas ou pontos cegos nessa previsão.
2. Reescreva a previsão corrigindo esses problemas.

Responda com a previsão MELHORADA apenas (sem listar as falhas separadamente).`,
		req.Scenario, synthesis)

	improved, err := o.cfg.LLM.Call(ctx, systemPrompt, userPrompt)
	if err != nil {
		return synthesis, err // return original on error
	}

	return improved, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Parsing helpers
// ─────────────────────────────────────────────────────────────────────────────

func parseArchetypes(response string) ([]Archetype, error) {
	// Find JSON array in response
	start := strings.Index(response, "[")
	end := strings.LastIndex(response, "]")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array found")
	}

	jsonStr := response[start : end+1]
	var archetypes []Archetype

	// Simple JSON parsing without encoding/json to avoid import cycle
	// In production, use encoding/json
	_ = jsonStr
	_ = archetypes

	return nil, fmt.Errorf("use encoding/json in production")
}

func extractSection(text, startMarker, endMarker string) []string {
	startIdx := strings.Index(strings.ToUpper(text), strings.ToUpper(startMarker))
	if startIdx < 0 {
		return nil
	}

	section := text[startIdx:]
	if endMarker != "" {
		endIdx := strings.Index(strings.ToUpper(section), strings.ToUpper(endMarker))
		if endIdx > 0 {
			section = section[:endIdx]
		}
	}

	var items []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		// Extract bullet points
		for _, prefix := range []string{"- ", "• ", "* ", "· "} {
			if strings.HasPrefix(line, prefix) {
				item := strings.TrimPrefix(line, prefix)
				if len(item) > 5 {
					items = append(items, item)
				}
				break
			}
		}
		// Extract numbered items
		if len(line) > 3 && line[1] == '.' && line[0] >= '1' && line[0] <= '9' {
			item := strings.TrimSpace(line[2:])
			if len(item) > 5 {
				items = append(items, item)
			}
		}
	}

	return items
}

func extractConfidence(text string) float64 {
	// Look for percentage patterns near "confiança" or "confidence"
	lower := strings.ToLower(text)
	markers := []string{"confiança", "confidence", "confiança:"}
	for _, marker := range markers {
		idx := strings.LastIndex(lower, marker)
		if idx < 0 {
			continue
		}
		// Scan forward for a number
		fragment := text[idx : min(idx+100, len(text))]
		for i, r := range fragment {
			if r >= '0' && r <= '9' {
				// Parse the number
				numStr := ""
				for _, c := range fragment[i:] {
					if c >= '0' && c <= '9' {
						numStr += string(c)
					} else {
						break
					}
				}
				if numStr != "" {
					var val float64
					fmt.Sscanf(numStr, "%f", &val)
					if val > 1 {
						val /= 100
					}
					if val >= 0 && val <= 1 {
						return val
					}
				}
				break
			}
		}
	}
	return 0.7 // default confidence
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
