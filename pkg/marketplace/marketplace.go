// Package marketplace provides the Squad Marketplace for PicoClaw.
// Squads are pre-configured agent groups for specific business use cases.
// Supports local catalog, remote APEX marketplace, and one-click installation.
package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// SquadCategory classifies a squad by use case.
type SquadCategory string

const (
	CategorySales       SquadCategory = "vendas"
	CategorySupport     SquadCategory = "suporte"
	CategoryMarketing   SquadCategory = "marketing"
	CategoryFinance     SquadCategory = "financeiro"
	CategoryHR          SquadCategory = "rh"
	CategorySecurity    SquadCategory = "segurança"
	CategoryDev         SquadCategory = "desenvolvimento"
	CategoryHome        SquadCategory = "automação_residencial"
	CategoryResearch    SquadCategory = "pesquisa"
	CategoryCustom      SquadCategory = "personalizado"
)

// AgentRole defines a role within a squad.
type AgentRole struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Model        string            `json:"model"`         // e.g., "gpt-4o", "llama3.2", "phi3"
	SystemPrompt string            `json:"system_prompt"`
	Tools        []string          `json:"tools"`         // allowed tools
	Permissions  []string          `json:"permissions"`   // file, web, api, etc.
	Config       map[string]string `json:"config,omitempty"`
}

// SquadTemplate is a pre-configured agent squad.
type SquadTemplate struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Category    SquadCategory `json:"category"`
	Version     string        `json:"version"`
	Author      string        `json:"author"`
	Tags        []string      `json:"tags"`
	Agents      []AgentRole   `json:"agents"`
	Triggers    []string      `json:"triggers"`    // what activates this squad
	Integrations []string     `json:"integrations"` // required integrations
	OfflineOK   bool          `json:"offline_ok"`  // works without internet
	Downloads   int           `json:"downloads"`
	Rating      float64       `json:"rating"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Price       float64       `json:"price"` // 0 = free
	Currency    string        `json:"currency"`
}

// InstalledSquad tracks a locally installed squad.
type InstalledSquad struct {
	Template    SquadTemplate `json:"template"`
	InstalledAt time.Time     `json:"installed_at"`
	Active      bool          `json:"active"`
	CustomConfig map[string]string `json:"custom_config,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Marketplace
// ─────────────────────────────────────────────────────────────────────────────

// Marketplace manages the squad catalog and installations.
type Marketplace struct {
	dataDir   string
	catalog   []SquadTemplate
	installed map[string]*InstalledSquad
	mu        sync.RWMutex
}

// NewMarketplace creates a new marketplace instance.
func NewMarketplace(dataDir string) *Marketplace {
	m := &Marketplace{
		dataDir:   dataDir,
		installed: make(map[string]*InstalledSquad),
	}
	m.catalog = builtinCatalog()
	return m
}

// Load loads installed squads from disk.
func (m *Marketplace) Load() error {
	os.MkdirAll(m.dataDir, 0700)
	path := filepath.Join(m.dataDir, "installed_squads.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // no installed squads yet
	}
	var installed map[string]*InstalledSquad
	if err := json.Unmarshal(data, &installed); err != nil {
		return err
	}
	m.mu.Lock()
	m.installed = installed
	m.mu.Unlock()
	fmt.Printf("🛒 Marketplace: %d squads instalados, %d no catálogo\n", len(installed), len(m.catalog))
	return nil
}

// Search searches the catalog by query and category.
func (m *Marketplace) Search(query string, category SquadCategory) []SquadTemplate {
	m.mu.RLock()
	defer m.mu.RUnlock()

	query = strings.ToLower(query)
	var results []SquadTemplate

	for _, t := range m.catalog {
		if category != "" && t.Category != category {
			continue
		}
		if query != "" {
			text := strings.ToLower(t.Name + " " + t.Description + " " + strings.Join(t.Tags, " "))
			if !strings.Contains(text, query) {
				continue
			}
		}
		results = append(results, t)
	}
	return results
}

// Install installs a squad from the catalog.
func (m *Marketplace) Install(ctx context.Context, squadID string, customConfig map[string]string) error {
	// Find in catalog
	var template *SquadTemplate
	for i := range m.catalog {
		if m.catalog[i].ID == squadID {
			template = &m.catalog[i]
			break
		}
	}
	if template == nil {
		return fmt.Errorf("squad não encontrado: %s", squadID)
	}

	m.mu.Lock()
	m.installed[squadID] = &InstalledSquad{
		Template:     *template,
		InstalledAt:  time.Now(),
		Active:       true,
		CustomConfig: customConfig,
	}
	m.mu.Unlock()

	m.save()
	fmt.Printf("✅ Squad instalado: '%s' (%d agentes)\n", template.Name, len(template.Agents))
	return nil
}

// Uninstall removes an installed squad.
func (m *Marketplace) Uninstall(squadID string) error {
	m.mu.Lock()
	if _, ok := m.installed[squadID]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("squad não instalado: %s", squadID)
	}
	delete(m.installed, squadID)
	m.mu.Unlock()
	m.save()
	return nil
}

// ListInstalled returns all installed squads.
func (m *Marketplace) ListInstalled() []InstalledSquad {
	m.mu.RLock()
	defer m.mu.RUnlock()
	squads := make([]InstalledSquad, 0, len(m.installed))
	for _, s := range m.installed {
		squads = append(squads, *s)
	}
	return squads
}

// GetSquad returns an installed squad by ID.
func (m *Marketplace) GetSquad(squadID string) (*InstalledSquad, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.installed[squadID]
	if !ok {
		return nil, fmt.Errorf("squad não instalado: %s", squadID)
	}
	return s, nil
}

func (m *Marketplace) save() {
	m.mu.RLock()
	data, err := json.MarshalIndent(m.installed, "", "  ")
	m.mu.RUnlock()
	if err == nil {
		os.WriteFile(filepath.Join(m.dataDir, "installed_squads.json"), data, 0600)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Built-in Squad Catalog
// ─────────────────────────────────────────────────────────────────────────────

func builtinCatalog() []SquadTemplate {
	now := time.Now()
	return []SquadTemplate{
		{
			ID:          "squad-vendas-whatsapp",
			Name:        "Vendedor WhatsApp Pro",
			Description: "Squad especializado em vendas pelo WhatsApp. Qualifica leads, envia propostas, faz follow-up automático e fecha negócios.",
			Category:    CategorySales,
			Version:     "1.0.0",
			Author:      "Synapse",
			Tags:        []string{"vendas", "whatsapp", "leads", "crm"},
			OfflineOK:   false,
			Downloads:   1247,
			Rating:      4.8,
			Price:       0,
			Currency:    "BRL",
			CreatedAt:   now,
			UpdatedAt:   now,
			Integrations: []string{"whatsapp"},
			Agents: []AgentRole{
				{
					Name:         "Qualificador",
					Description:  "Qualifica leads recebidos no WhatsApp",
					Model:        "gpt-4o-mini",
					SystemPrompt: "Você é um especialista em qualificação de leads. Faça perguntas estratégicas para entender as necessidades do cliente e classificar o lead como quente, morno ou frio.",
					Tools:        []string{"whatsapp_send", "crm_update"},
					Permissions:  []string{"api"},
				},
				{
					Name:         "Negociador",
					Description:  "Negocia e fecha vendas",
					Model:        "gpt-4o",
					SystemPrompt: "Você é um negociador experiente. Use técnicas de vendas consultivas para fechar negócios, superar objeções e criar urgência genuína.",
					Tools:        []string{"whatsapp_send", "proposal_generate", "calendar_book"},
					Permissions:  []string{"api", "web"},
				},
				{
					Name:         "Follow-up",
					Description:  "Faz follow-up automático com leads",
					Model:        "gpt-4o-mini",
					SystemPrompt: "Você é responsável pelo follow-up de vendas. Envie mensagens personalizadas nos momentos certos para reengajar leads.",
					Tools:        []string{"whatsapp_send", "scheduler_add"},
					Permissions:  []string{"api"},
				},
			},
			Triggers: []string{"whatsapp_message", "lead_created", "scheduled"},
		},
		{
			ID:          "squad-suporte-cliente",
			Name:        "Suporte ao Cliente 24/7",
			Description: "Atendimento automatizado com escalonamento inteligente. Resolve dúvidas, abre chamados e escala para humanos quando necessário.",
			Category:    CategorySupport,
			Version:     "1.2.0",
			Author:      "Synapse",
			Tags:        []string{"suporte", "atendimento", "helpdesk", "24h"},
			OfflineOK:   true,
			Downloads:   3891,
			Rating:      4.9,
			Price:       0,
			Currency:    "BRL",
			CreatedAt:   now,
			UpdatedAt:   now,
			Integrations: []string{"whatsapp", "telegram"},
			Agents: []AgentRole{
				{
					Name:         "Triagem",
					Description:  "Classifica e prioriza tickets de suporte",
					Model:        "phi3",
					SystemPrompt: "Você é um agente de triagem. Classifique o problema do cliente em: técnico, financeiro, dúvida ou reclamação. Priorize por urgência.",
					Tools:        []string{"ticket_create", "knowledge_base_search"},
					Permissions:  []string{"api"},
				},
				{
					Name:         "Resolvedor",
					Description:  "Resolve problemas técnicos e dúvidas",
					Model:        "llama3.2",
					SystemPrompt: "Você é um especialista em suporte técnico. Use a base de conhecimento para resolver problemas. Se não souber, escale para um humano.",
					Tools:        []string{"knowledge_base_search", "ticket_update", "escalate"},
					Permissions:  []string{"api"},
				},
			},
			Triggers: []string{"whatsapp_message", "telegram_message", "ticket_created"},
		},
		{
			ID:          "squad-marketing-conteudo",
			Name:        "Criador de Conteúdo Marketing",
			Description: "Cria posts para redes sociais, newsletters e campanhas de email. Adapta o tom para cada plataforma.",
			Category:    CategoryMarketing,
			Version:     "1.0.0",
			Author:      "Synapse",
			Tags:        []string{"marketing", "conteúdo", "redes sociais", "email"},
			OfflineOK:   false,
			Downloads:   2156,
			Rating:      4.7,
			Price:       0,
			Currency:    "BRL",
			CreatedAt:   now,
			UpdatedAt:   now,
			Integrations: []string{"google_drive", "notion"},
			Agents: []AgentRole{
				{
					Name:         "Estrategista",
					Description:  "Define estratégia e pauta de conteúdo",
					Model:        "gpt-4o",
					SystemPrompt: "Você é um estrategista de marketing digital. Crie pautas de conteúdo alinhadas com os objetivos do negócio e o público-alvo.",
					Tools:        []string{"web_search", "notion_write", "calendar_schedule"},
					Permissions:  []string{"web", "api"},
				},
				{
					Name:         "Redator",
					Description:  "Escreve o conteúdo para cada plataforma",
					Model:        "gpt-4o",
					SystemPrompt: "Você é um redator criativo especializado em marketing digital. Escreva textos envolventes, otimizados para cada plataforma.",
					Tools:        []string{"notion_write", "drive_save"},
					Permissions:  []string{"api"},
				},
			},
			Triggers: []string{"scheduled", "manual"},
		},
		{
			ID:          "squad-financeiro-relatorios",
			Name:        "Analista Financeiro",
			Description: "Analisa planilhas financeiras, gera relatórios, detecta anomalias e envia alertas automáticos.",
			Category:    CategoryFinance,
			Version:     "1.0.0",
			Author:      "Synapse",
			Tags:        []string{"financeiro", "planilhas", "relatórios", "análise"},
			OfflineOK:   true,
			Downloads:   987,
			Rating:      4.6,
			Price:       0,
			Currency:    "BRL",
			CreatedAt:   now,
			UpdatedAt:   now,
			Integrations: []string{"google_sheets", "onedrive"},
			Agents: []AgentRole{
				{
					Name:         "Analista",
					Description:  "Analisa dados financeiros",
					Model:        "gpt-4o",
					SystemPrompt: "Você é um analista financeiro. Analise os dados fornecidos, identifique tendências, anomalias e oportunidades de melhoria.",
					Tools:        []string{"sheets_read", "sheets_write", "report_generate"},
					Permissions:  []string{"api", "file"},
				},
			},
			Triggers: []string{"scheduled", "file_changed"},
		},
		{
			ID:          "squad-casa-inteligente",
			Name:        "Automação Residencial Inteligente",
			Description: "Controla dispositivos smart home, aprende rotinas, otimiza energia e responde a comandos de voz.",
			Category:    CategoryHome,
			Version:     "2.0.0",
			Author:      "Synapse",
			Tags:        []string{"casa", "iot", "home assistant", "automação"},
			OfflineOK:   true,
			Downloads:   1543,
			Rating:      4.9,
			Price:       0,
			Currency:    "BRL",
			CreatedAt:   now,
			UpdatedAt:   now,
			Integrations: []string{"home_assistant", "mqtt"},
			Agents: []AgentRole{
				{
					Name:         "Controlador",
					Description:  "Controla dispositivos e cenas",
					Model:        "phi3",
					SystemPrompt: "Você controla dispositivos de automação residencial. Execute comandos de forma segura e confirme as ações realizadas.",
					Tools:        []string{"ha_control", "mqtt_publish"},
					Permissions:  []string{"api"},
				},
				{
					Name:         "Aprendiz de Rotinas",
					Description:  "Aprende e otimiza rotinas do usuário",
					Model:        "llama3.2",
					SystemPrompt: "Você aprende os padrões de comportamento do usuário e sugere automações inteligentes para melhorar o conforto e economizar energia.",
					Tools:        []string{"ha_read", "memory_store", "scheduler_add"},
					Permissions:  []string{"api"},
				},
			},
			Triggers: []string{"voice_command", "presence_detected", "scheduled", "sensor_trigger"},
		},
		{
			ID:          "squad-seguranca-monitoramento",
			Name:        "Segurança e Monitoramento",
			Description: "Monitora câmeras, detecta intrusos, envia alertas e registra eventos de segurança com análise de IA.",
			Category:    CategorySecurity,
			Version:     "1.0.0",
			Author:      "Synapse",
			Tags:        []string{"segurança", "câmeras", "alertas", "monitoramento"},
			OfflineOK:   true,
			Downloads:   2234,
			Rating:      4.8,
			Price:       0,
			Currency:    "BRL",
			CreatedAt:   now,
			UpdatedAt:   now,
			Integrations: []string{"home_assistant", "whatsapp", "telegram"},
			Agents: []AgentRole{
				{
					Name:         "Sentinela",
					Description:  "Monitora e detecta ameaças",
					Model:        "phi3",
					SystemPrompt: "Você é um agente de segurança. Monitore eventos, detecte anomalias e emita alertas quando necessário. Seja preciso para evitar falsos positivos.",
					Tools:        []string{"camera_analyze", "ha_read", "alert_send"},
					Permissions:  []string{"api"},
				},
			},
			Triggers: []string{"motion_detected", "door_opened", "sensor_trigger"},
		},
		{
			ID:          "squad-dev-code-review",
			Name:        "Code Review Automatizado",
			Description: "Revisa pull requests, sugere melhorias, detecta bugs e garante boas práticas de código.",
			Category:    CategoryDev,
			Version:     "1.0.0",
			Author:      "Synapse",
			Tags:        []string{"desenvolvimento", "code review", "github", "qualidade"},
			OfflineOK:   false,
			Downloads:   1876,
			Rating:      4.7,
			Price:       0,
			Currency:    "BRL",
			CreatedAt:   now,
			UpdatedAt:   now,
			Integrations: []string{"github"},
			Agents: []AgentRole{
				{
					Name:         "Revisor",
					Description:  "Revisa código e sugere melhorias",
					Model:        "gpt-4o",
					SystemPrompt: "Você é um engenheiro de software sênior. Revise o código com foco em: bugs, segurança, performance, legibilidade e boas práticas.",
					Tools:        []string{"github_read", "github_comment"},
					Permissions:  []string{"api"},
				},
			},
			Triggers: []string{"github_pr", "manual"},
		},
	}
}
