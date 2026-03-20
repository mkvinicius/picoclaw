// Package reasoning - Setup Wizard for first-time PicoClaw configuration.
// This wizard guides the user through choosing Online or Offline mode,
// downloading the appropriate model, and configuring the Tri-Brain.
package reasoning

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// WizardConfig holds the result of the setup wizard.
type WizardConfig struct {
	Mode        Mode
	OllamaModel string
	TriBrain    bool
}

// RunSetupWizard runs the interactive first-time setup wizard.
// It guides the user through choosing Online or Offline mode.
func RunSetupWizard(ctx context.Context) (*WizardConfig, error) {
	reader := bufio.NewReader(os.Stdin)

	printBanner()

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🚀 BEM-VINDO AO PICOCLAW — CONFIGURAÇÃO INICIAL")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("  Como você quer usar o PicoClaw?")
	fmt.Println()
	fmt.Println("  [1] 🌐 ONLINE  — Conecta a uma IA na nuvem (OpenAI, Claude, etc.)")
	fmt.Println("       ✅ Mais inteligente e atualizado")
	fmt.Println("       ✅ Não precisa baixar nada")
	fmt.Println("       ⚠️  Requer internet e chave de API")
	fmt.Println("       ⚠️  Custo por uso (tokens)")
	fmt.Println()
	fmt.Println("  [2] 💻 OFFLINE — Baixa uma IA para rodar no seu computador")
	fmt.Println("       ✅ 100% privado, seus dados nunca saem da máquina")
	fmt.Println("       ✅ Funciona sem internet após o download")
	fmt.Println("       ✅ Sem custo de API")
	fmt.Println("       ⚠️  Requer ~2-5 GB de espaço em disco")
	fmt.Println("       ⚠️  Precisa de internet apenas para baixar o modelo")
	fmt.Println()
	fmt.Println("  [3] 🔄 AUTO   — Usa online quando disponível, offline como backup")
	fmt.Println("       ✅ Melhor dos dois mundos")
	fmt.Println("       ✅ Continua funcionando mesmo sem internet")
	fmt.Println()
	fmt.Print("  Sua escolha [1/2/3]: ")

	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)

	cfg := &WizardConfig{TriBrain: true}

	switch choice {
	case "1":
		cfg.Mode = ModeOnline
		fmt.Println()
		fmt.Println("  ✅ Modo ONLINE selecionado.")
		fmt.Println("  Configure sua chave de API em ~/.picoclaw/config.json")
		fmt.Println("  Consulte o README para ver todos os provedores suportados.")

	case "2":
		cfg.Mode = ModeOffline
		model, err := runModelSelector(ctx, reader)
		if err != nil {
			return nil, err
		}
		cfg.OllamaModel = model

	case "3", "":
		cfg.Mode = ModeAuto
		fmt.Println()
		fmt.Println("  ✅ Modo AUTO selecionado.")
		fmt.Println("  O PicoClaw usará online quando disponível e offline como backup.")
		fmt.Println()

		// Ask if they want to set up offline as backup
		fmt.Print("  Deseja configurar o modo offline como backup agora? [S/n]: ")
		backupChoice, _ := reader.ReadString('\n')
		backupChoice = strings.TrimSpace(strings.ToLower(backupChoice))
		if backupChoice != "n" && backupChoice != "nao" && backupChoice != "não" {
			model, err := runModelSelector(ctx, reader)
			if err != nil {
				return nil, err
			}
			cfg.OllamaModel = model
		}

	default:
		fmt.Println("  ⚠️  Opção inválida. Usando modo AUTO por padrão.")
		cfg.Mode = ModeAuto
	}

	// Ask about Tri-Brain
	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🛡️  TRI-BRAIN — Validação de Segurança (MIL-SPEC)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("  O Tri-Brain valida cada resposta da IA através de 3 camadas:")
	fmt.Println("  1. 🔒 Safety Brain  — Bloqueia conteúdo perigoso e injeções")
	fmt.Println("  2. 🧠 Logic Brain   — Detecta contradições e alucinações")
	fmt.Println("  3. 🎯 Intent Brain  — Verifica se a resposta resolve o pedido")
	fmt.Println()
	fmt.Println("  Recomendado: ATIVADO (pequeno impacto na velocidade, grande ganho em segurança)")
	fmt.Println()
	fmt.Print("  Ativar Tri-Brain? [S/n]: ")

	triChoice, _ := reader.ReadString('\n')
	triChoice = strings.TrimSpace(strings.ToLower(triChoice))
	if triChoice == "n" || triChoice == "nao" || triChoice == "não" {
		cfg.TriBrain = false
		fmt.Println("  ⚠️  Tri-Brain DESATIVADO. Segurança reduzida.")
	} else {
		cfg.TriBrain = true
		fmt.Println("  ✅ Tri-Brain ATIVADO. Máxima segurança e confiabilidade.")
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🎉 CONFIGURAÇÃO CONCLUÍDA!")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	printConfigSummary(cfg)
	fmt.Println()

	return cfg, nil
}

// runModelSelector shows the model selection menu and handles download.
func runModelSelector(ctx context.Context, reader *bufio.Reader) (string, error) {
	models := RecommendedModels()

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  📦 ESCOLHA O MODELO OFFLINE")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	for i, m := range models {
		marker := " "
		if i == 0 {
			marker = "★"
		}
		fmt.Printf("  [%d] %s %s\n", i+1, marker, m.DisplayName)
		fmt.Printf("      Tamanho: %s | RAM: %s | Velocidade: %s | Qualidade: %s\n",
			m.Size, m.RAM, m.Speed, m.Quality)
		fmt.Printf("      %s\n", m.Description)
		fmt.Println()
	}

	fmt.Print("  Sua escolha [1-5] (padrão: 1): ")
	modelChoice, _ := reader.ReadString('\n')
	modelChoice = strings.TrimSpace(modelChoice)

	selectedIdx := 0
	if modelChoice != "" {
		idx := int(modelChoice[0] - '1')
		if idx >= 0 && idx < len(models) {
			selectedIdx = idx
		}
	}

	selectedModel := models[selectedIdx]
	fmt.Printf("\n  ✅ Modelo selecionado: %s\n", selectedModel.DisplayName)

	// Check if Ollama is installed and running
	manager := NewOllamaManager("")

	if !manager.IsOllamaRunning() {
		fmt.Println()
		fmt.Println("  ℹ️  Ollama não está instalado/rodando.")
		fmt.Print("  Instalar Ollama automaticamente? [S/n]: ")
		installChoice, _ := reader.ReadString('\n')
		installChoice = strings.TrimSpace(strings.ToLower(installChoice))

		if installChoice != "n" && installChoice != "nao" && installChoice != "não" {
			fmt.Println()
			err := manager.InstallOllama(func(msg string) {
				fmt.Println(" ", msg)
			})
			if err != nil {
				fmt.Printf("\n  ❌ Erro ao instalar Ollama: %v\n", err)
				fmt.Println("  Instale manualmente em: https://ollama.com/download")
				fmt.Println("  Depois execute: picoclaw offline download " + selectedModel.Name)
				return selectedModel.Name, nil
			}

			// Start Ollama after installation
			fmt.Println("  🚀 Iniciando Ollama...")
			if err := manager.StartOllama(); err != nil {
				fmt.Printf("  ⚠️  %v\n", err)
			}
			time.Sleep(2 * time.Second)
		} else {
			fmt.Println("\n  ℹ️  Pulando instalação do Ollama.")
			fmt.Println("  Para usar offline depois, execute:")
			fmt.Println("    picoclaw offline install")
			fmt.Println("    picoclaw offline download " + selectedModel.Name)
			return selectedModel.Name, nil
		}
	}

	// Check if model is already downloaded
	if manager.IsModelAvailable(selectedModel.Name) {
		fmt.Printf("\n  ✅ Modelo %s já está disponível!\n", selectedModel.Name)
		return selectedModel.Name, nil
	}

	// Download the model
	fmt.Println()
	fmt.Printf("  📥 Baixando %s (%s)...\n", selectedModel.DisplayName, selectedModel.Size)
	fmt.Println("  ⏳ Isso pode levar alguns minutos. Não feche o terminal.")
	fmt.Println()

	err := manager.DownloadModel(ctx, selectedModel.Name, func(msg string) {
		fmt.Println(" ", msg)
	})
	if err != nil {
		fmt.Printf("\n  ❌ Erro ao baixar modelo: %v\n", err)
		fmt.Println("  Tente manualmente depois: picoclaw offline download " + selectedModel.Name)
		return selectedModel.Name, nil
	}

	return selectedModel.Name, nil
}

func printBanner() {
	fmt.Println()
	fmt.Println(`  ██████╗ ██╗ ██████╗ ██████╗  ██████╗██╗      █████╗ ██╗    ██╗`)
	fmt.Println(`  ██╔══██╗██║██╔════╝██╔═══██╗██╔════╝██║     ██╔══██╗██║    ██║`)
	fmt.Println(`  ██████╔╝██║██║     ██║   ██║██║     ██║     ███████║██║ █╗ ██║`)
	fmt.Println(`  ██╔═══╝ ██║██║     ██║   ██║██║     ██║     ██╔══██║██║███╗██║`)
	fmt.Println(`  ██║     ██║╚██████╗╚██████╔╝╚██████╗███████╗██║  ██║╚███╔███╔╝`)
	fmt.Println(`  ╚═╝     ╚═╝ ╚═════╝ ╚═════╝  ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝`)
	fmt.Println()
	fmt.Println("  Powered by APEX MIL-SPEC Security · Tri-Brain Validation")
	fmt.Println()
}

func printConfigSummary(cfg *WizardConfig) {
	modeStr := map[Mode]string{
		ModeOnline:  "🌐 Online (API externa)",
		ModeOffline: "💻 Offline (modelo local)",
		ModeAuto:    "🔄 Auto (online + fallback offline)",
	}[cfg.Mode]

	triBrainStr := "❌ Desativado"
	if cfg.TriBrain {
		triBrainStr = "✅ Ativado (3 camadas de validação)"
	}

	modelStr := "N/A"
	if cfg.OllamaModel != "" {
		modelStr = cfg.OllamaModel
	}

	fmt.Printf("  Modo de Raciocínio : %s\n", modeStr)
	fmt.Printf("  Modelo Offline     : %s\n", modelStr)
	fmt.Printf("  Tri-Brain          : %s\n", triBrainStr)
	fmt.Println()
	fmt.Println("  Para alterar as configurações: picoclaw setup")
	fmt.Println("  Para iniciar:                  picoclaw agent -m \"Olá!\"")
}
