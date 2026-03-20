// Package offline provides CLI commands for managing offline (local) AI models in PicoClaw.
// Commands:
//   picoclaw offline setup    — Interactive wizard to choose Online/Offline mode
//   picoclaw offline install  — Install Ollama on the current machine
//   picoclaw offline download — Download a specific model
//   picoclaw offline list     — List available and downloaded models
//   picoclaw offline start    — Start the Ollama server
//   picoclaw offline status   — Show current offline mode status
package offline

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/sipeed/picoclaw/pkg/reasoning"
	"github.com/spf13/cobra"
)

// NewOfflineCommand creates the 'offline' command group.
func NewOfflineCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "offline",
		Short: "Gerenciar modo offline (IA local sem internet)",
		Long: `Gerenciar o modo offline do PicoClaw.

No modo offline, o PicoClaw usa um modelo de IA rodando localmente no seu
computador, sem precisar de internet ou chave de API após o download inicial.

Exemplos:
  picoclaw offline setup              # Configuração interativa (recomendado)
  picoclaw offline install            # Instalar Ollama
  picoclaw offline download llama3.2:3b  # Baixar modelo específico
  picoclaw offline list               # Ver modelos disponíveis
  picoclaw offline status             # Ver status atual`,
	}

	cmd.AddCommand(newSetupCommand())
	cmd.AddCommand(newInstallCommand())
	cmd.AddCommand(newDownloadCommand())
	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newStartCommand())
	cmd.AddCommand(newStatusCommand())

	return cmd
}

// ─────────────────────────────────────────────────────────────────────────────
// setup command
// ─────────────────────────────────────────────────────────────────────────────

func newSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Assistente de configuração interativo (Online/Offline/Auto)",
		Long:  "Executa o assistente de configuração para escolher entre modo Online, Offline ou Auto.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			cfg, err := reasoning.RunSetupWizard(ctx)
			if err != nil {
				return fmt.Errorf("erro na configuração: %w", err)
			}

			fmt.Printf("\n✅ Configuração salva! Modo: %s\n", cfg.Mode)
			return nil
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// install command
// ─────────────────────────────────────────────────────────────────────────────

func newInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Instalar Ollama no seu computador",
		Long:  "Baixa e instala o Ollama, necessário para usar modelos de IA localmente.",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager := reasoning.NewOllamaManager("")

			if manager.IsOllamaRunning() {
				fmt.Println("✅ Ollama já está instalado e rodando!")
				return nil
			}

			fmt.Println("📥 Instalando Ollama...")
			err := manager.InstallOllama(func(msg string) {
				fmt.Println(msg)
			})
			if err != nil {
				return fmt.Errorf("erro ao instalar Ollama: %w", err)
			}

			fmt.Println("\n🚀 Iniciando Ollama...")
			if err := manager.StartOllama(); err != nil {
				fmt.Printf("⚠️  %v\n", err)
				fmt.Println("Execute 'ollama serve' manualmente para iniciar o servidor.")
				return nil
			}

			fmt.Println("✅ Ollama instalado e rodando!")
			fmt.Println("\nPróximo passo: baixe um modelo com:")
			fmt.Println("  picoclaw offline download llama3.2:3b")
			return nil
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// download command
// ─────────────────────────────────────────────────────────────────────────────

func newDownloadCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "download [model]",
		Short: "Baixar um modelo de IA para uso offline",
		Long: `Baixar um modelo de IA para uso offline.

Se nenhum modelo for especificado, mostra a lista de modelos recomendados.

Exemplos:
  picoclaw offline download                # Mostra modelos recomendados
  picoclaw offline download llama3.2:3b   # Baixa Llama 3.2 (3B) — recomendado
  picoclaw offline download phi3:mini     # Baixa Phi-3 Mini (Microsoft)
  picoclaw offline download gemma2:2b     # Baixa Gemma 2 (Google)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager := reasoning.NewOllamaManager("")

			if !manager.IsOllamaRunning() {
				fmt.Println("❌ Ollama não está rodando.")
				fmt.Println("Execute primeiro: picoclaw offline install")
				return nil
			}

			// If no model specified, show recommendations
			if len(args) == 0 {
				printModelTable()
				fmt.Println("\nUse: picoclaw offline download <nome-do-modelo>")
				return nil
			}

			modelName := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			if manager.IsModelAvailable(modelName) {
				fmt.Printf("✅ Modelo '%s' já está disponível!\n", modelName)
				return nil
			}

			err := manager.DownloadModel(ctx, modelName, func(msg string) {
				fmt.Println(msg)
			})
			if err != nil {
				return fmt.Errorf("erro ao baixar modelo: %w", err)
			}

			fmt.Printf("\n✅ Modelo '%s' pronto para uso!\n", modelName)
			fmt.Println("Para usar: picoclaw model " + modelName)
			return nil
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// list command
// ─────────────────────────────────────────────────────────────────────────────

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Listar modelos disponíveis para download",
		RunE: func(cmd *cobra.Command, args []string) error {
			printModelTable()

			manager := reasoning.NewOllamaManager("")
			if manager.IsOllamaRunning() {
				fmt.Println("\n✅ Ollama está rodando. Modelos já baixados marcados com ★")
			} else {
				fmt.Println("\n⚠️  Ollama não está rodando. Execute: picoclaw offline install")
			}
			return nil
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// start command
// ─────────────────────────────────────────────────────────────────────────────

func newStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Iniciar o servidor Ollama em segundo plano",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager := reasoning.NewOllamaManager("")

			if manager.IsOllamaRunning() {
				fmt.Println("✅ Ollama já está rodando!")
				return nil
			}

			fmt.Println("🚀 Iniciando Ollama...")
			if err := manager.StartOllama(); err != nil {
				return fmt.Errorf("erro ao iniciar Ollama: %w", err)
			}

			fmt.Println("✅ Ollama iniciado com sucesso!")
			return nil
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// status command
// ─────────────────────────────────────────────────────────────────────────────

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Verificar status do modo offline",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager := reasoning.NewOllamaManager("")

			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println("  🔍 STATUS DO MODO OFFLINE")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			if manager.IsOllamaRunning() {
				fmt.Println("  Ollama Server : ✅ Rodando (localhost:11434)")

				// Check which models are available
				models := reasoning.RecommendedModels()
				fmt.Println()
				fmt.Println("  Modelos Recomendados:")
				for _, m := range models {
					status := "  ○ Não baixado"
					if manager.IsModelAvailable(m.Name) {
						status = "  ★ Disponível"
					}
					fmt.Printf("    %s  %s (%s)\n", status, m.DisplayName, m.Size)
				}
			} else {
				fmt.Println("  Ollama Server : ❌ Não rodando")
				fmt.Println()
				fmt.Println("  Para ativar o modo offline:")
				fmt.Println("    picoclaw offline install   # Instalar Ollama")
				fmt.Println("    picoclaw offline start     # Iniciar Ollama")
				fmt.Println("    picoclaw offline download  # Baixar um modelo")
			}

			fmt.Println()
			return nil
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func printModelTable() {
	models := reasoning.RecommendedModels()

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  📦 MODELOS DISPONÍVEIS PARA USO OFFLINE")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  MODELO\tTAMANHO\tRAM\tVELOCIDADE\tQUALIDADE")
	fmt.Fprintln(w, "  ──────\t───────\t───\t──────────\t─────────")
	for i, m := range models {
		marker := " "
		if i == 0 {
			marker = "★"
		}
		fmt.Fprintf(w, "  %s %-20s\t%s\t%s\t%s\t%s\n",
			marker, m.Name, m.Size, m.RAM, m.Speed, m.Quality)
	}
	w.Flush()
	fmt.Println()
	fmt.Println("  ★ = Recomendado para a maioria dos usuários")
}
