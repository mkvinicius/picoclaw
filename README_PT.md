# PicoClaw - Documentação em Português

Esta documentação foi gerada para fornecer uma visão geral detalhada sobre o projeto PicoClaw.

## 1. O que é o PicoClaw?

O **PicoClaw** é um assistente de inteligência artificial (AI Agent) ultra-eficiente e de código aberto, iniciado pela [Sipeed](https://sipeed.com). Ele foi escrito inteiramente em **Go** e projetado para ser executado em hardwares com recursos extremamente limitados, como dispositivos de $10 e com menos de 10MB de RAM. O principal objetivo do PicoClaw é fornecer uma IA poderosa e portátil que inicie em menos de 1 segundo.

## 2. A Arquitetura do Projeto

A arquitetura do PicoClaw é focada na leveza, portabilidade e eficiência. Ele não utiliza os conceitos de `EventBus`, `HookManager`, `SubTurn` e `Steering` com esses nomes, embora ele apresente comportamentos semelhantes através de outros mecanismos internos em sua base de código (como `loop.go`, `registry.go` para ferramentas, etc). A base do projeto compila em um único executável binário autossuficiente compatível com múltiplas arquiteturas (RISC-V, ARM, MIPS e x86). O PicoClaw é modular, separando responsabilidades como comunicação de canais (Canais), integração de modelos de linguagem (Provedores) e habilidades (Ferramentas).

## 3. Principais Módulos e Responsabilidades

*   **Agente (`pkg/agent/`)**: O núcleo de processamento do assistente, responsável por manter o contexto, executar os "loops" do agente (`loop.go`), integrar a memória (`memory.go`) e interagir com o Model Context Protocol (MCP) e outros provedores.
*   **Canais (`pkg/channels/` - inferido por sua arquitetura de integração)**: Responsável pela integração com diversos aplicativos de chat (Telegram, Discord, WhatsApp, Matrix, QQ, DingTalk, LINE, WeCom). O sistema utiliza uma arquitetura de Gateway para concentrar e expor webhooks e conexões.
*   **Provedores (`pkg/providers/` - inferido)**: Camada de integração com as APIs de LLMs como OpenAI, Anthropic, Gemini, Zhipu, etc.
*   **Ferramentas (`pkg/tools/`)**: Um registro central (`ToolRegistry`) de habilidades que o agente pode utilizar, como pesquisa web (DuckDuckGo, Brave, Tavily, etc.) e agendamentos.
*   **Migração (`pkg/migrate/`)**: Módulo dedicado a converter e carregar dados/arquivos de configuração de versões ou projetos mais antigos, como o formato do OpenClaw.

## 4. Como os conceitos EventBus, HookManager, ToolRegistry, SubTurn e Steering funcionam

*   **ToolRegistry**: Localizado em `pkg/tools/registry.go`, o `ToolRegistry` é o componente responsável por gerenciar as ferramentas que o agente pode invocar. Ele permite registrar novas ferramentas (`Register`), buscar (`Get`), listar definições e executá-las com contexto (`ExecuteWithContext`). Ele promove a extensibilidade do sistema, permitindo adicionar habilidades de forma modular (como pesquisa web e MCP).
*   **EventBus, HookManager, SubTurn, e Steering**: Uma inspeção profunda no código-fonte em Go do PicoClaw revelou que **esses conceitos/arquiteturas não existem** com essa nomenclatura no repositório. Ao contrário do que foi presumido, o PicoClaw utiliza loops de evento próprios e interações diretas (como no `loop.go`), além de não possuir uma camada explícita de `HookManager` ou `Steering`. Esses termos parecem referir-se a conceitos de outras arquiteturas de IA (talvez do próprio OpenClaw) e não se aplicam ao PicoClaw.

## 5. Como o PicoClaw se compara ao OpenClaw

*   **Linguagem de Programação:** O PicoClaw é escrito em **Go**, enquanto o OpenClaw é escrito em TypeScript.
*   **Consumo de Memória:** O PicoClaw consome menos de **10MB** de RAM, o que o torna 99% mais leve do que as funcionalidades centrais do OpenClaw (que consome >1GB).
*   **Tempo de Inicialização:** O PicoClaw inicia em menos de **1 segundo** (mesmo em CPUs single-core de baixa frequência), enquanto o OpenClaw pode levar centenas de segundos.
*   **Custo e Hospedagem:** O PicoClaw pode ser executado em placas Linux incrivelmente baratas (a partir de $10), diferente do OpenClaw, que muitas vezes exige hardwares mais robustos (como um Mac Mini).
*   **Projeto Independente:** É essencial destacar que o PicoClaw **não é um fork** do OpenClaw. Ele foi feito do zero para focar em leveza, embora ofereça um módulo de migração (`pkg/migrate/sources/openclaw/`) para facilitar a transição de configurações do OpenClaw para o formato do PicoClaw.

## 6. Possíveis Casos de Uso

*   **Assistente Pessoal Doméstico (Minimalista):** Embutido em uma placa de $10 (como a LicheeRV-Nano) para automação de tarefas domésticas, lembretes e conversas inteligentes no dia-a-dia.
*   **Manutenção Automatizada de Servidores:** Instalado junto a ferramentas de KVM (como o NanoKVM) para realizar triagem, diagnóstico e análise de logs no caso de falhas em servidores de forma autônoma.
*   **Câmeras Inteligentes (Smart Monitoring):** Em conjunto com a MaixCAM, servindo como a "inteligência" local capaz de relatar o que está "vendo" na câmera, graças ao seu suporte de "Vision Pipeline".
*   **Reaproveitamento de Celulares Antigos:** Utilizando o *Termux*, é possível transformar smartphones com Android desatualizados em "Cérebros de IA" com baixo consumo de energia.
*   **Orquestração de Grupos de Mensagens:** Integrado a chats como Telegram ou Discord para auxiliar desenvolvedores, agendar tarefas diárias e realizar buscas rápidas.

## 7. Como rodar o projeto localmente

Você pode iniciar o projeto de diversas formas. A recomendada para testes e produção é usar Docker, e para desenvolvimento, compilar da fonte.

### Via Docker:
1. Clone o repositório:
   ```bash
   git clone https://github.com/sipeed/picoclaw.git
   cd picoclaw
   ```
2. Inicialize as configurações gerando um setup "First-run":
   ```bash
   docker compose -f docker/docker-compose.yml --profile gateway up
   ```
3. Edite as chaves de API necessárias no arquivo que foi criado: `docker/data/config.json`.
4. Inicie em segundo plano:
   ```bash
   docker compose -f docker/docker-compose.yml --profile gateway up -d
   ```

### Modo Interativo no Terminal (Sem Docker/Gateway local):
Você pode rodar diretamente na sua máquina local baixando os binários do Github ou compilando da fonte com Go:

1. Instale o pacote e suas dependências:
   ```bash
   make deps
   make build
   ```
2. Inicialize sua configuração local (isso cria uma pasta `~/.picoclaw` com as configurações):
   ```bash
   ./picoclaw onboard
   ```
3. Fale com o agente pelo terminal:
   ```bash
   ./picoclaw agent
   ```

## 8. Sugestões para evoluir o projeto

Baseado no ROADMAP do projeto, o PicoClaw pode evoluir nos seguintes tópicos:

*   **Redução Extrema de Memória:** Chegar à execução em placas com apenas 64MB de RAM, como pequenos computadores RISC-V, otimizando o executável e removendo dependências desnecessárias.
*   **Evolução como Agente Autônomo:**
    *   Melhorar suporte para múltiplos agentes interagindo em "Swarm Mode" (Instâncias cooperativas na mesma rede LAN).
    *   Integrar Automação de Navegadores ("Browser Automation") para que o agente execute tarefas complexas em sites, imitando navegação humana através do Chrome DevTools Protocol.
    *   Desenvolver o recurso "Model Routing", capaz de enviar tarefas fáceis para modelos baratos/locais e desafios analíticos complexos para LLMs premium de forma invisível para o usuário.
*   **Integração Profunda Local:** Otimizar o PicoClaw para executar em conluio com os modelos rodando *on-device* pelo **Ollama** ou **vLLM**, garantindo um ambiente corporativo com vazamento de dados nulo.
*   **Segurança Avançada:** Bloquear acessos indesejados da IA aos serviços locais de rede (SSRF Protection) e adicionar criptografia nas chaves de API ao invés de textos expostos nas configurações.
