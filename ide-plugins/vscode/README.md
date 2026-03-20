# PicoClaw Synapse — Extensão VS Code

Integre o poder do PicoClaw diretamente no seu editor. Execute squads de agentes, revise código com IA, agende tarefas e muito mais — tudo sem sair do VS Code ou do Cursor.

## Funcionalidades

| Ação | Atalho | Descrição |
|------|--------|-----------|
| Perguntar ao Agente | `Ctrl+Shift+A` | Faz uma pergunta com contexto do arquivo atual |
| Revisar Código | `Ctrl+Shift+R` | Revisão de código com sugestões de melhoria |
| Corrigir Código | `Ctrl+Shift+F` | Detecta e corrige bugs automaticamente |
| Explicar Código | Menu de contexto | Explicação em linguagem natural |
| Gerar Testes | Menu de contexto | Gera testes unitários para o código selecionado |
| Executar Squad | Painel lateral | Executa um squad pré-configurado |
| Agendar Tarefa | Painel lateral | Agenda tarefas recorrentes |

## Instalação

### Via Marketplace (recomendado)

1. Abra o VS Code
2. Pressione `Ctrl+Shift+X`
3. Busque por "PicoClaw Synapse"
4. Clique em **Instalar**

### Manual (VSIX)

```bash
# Instalar a partir do arquivo .vsix
code --install-extension picoclaw-synapse-1.0.0.vsix
```

### Compatibilidade com Cursor

Esta extensão é totalmente compatível com o [Cursor](https://cursor.sh). Instale da mesma forma que no VS Code.

## Configuração

Abra as configurações (`Ctrl+,`) e busque por "PicoClaw":

```json
{
  "picoclaw.serverUrl": "http://localhost:8080",
  "picoclaw.apiKey": "sua-chave-api",
  "picoclaw.autoConnect": true,
  "picoclaw.language": "pt-BR"
}
```

### Pré-requisito: PicoClaw rodando localmente

```bash
# Iniciar o PicoClaw
picoclaw start

# Verificar se está rodando
picoclaw status
```

## Painel Lateral

O painel lateral do PicoClaw (ícone na barra de atividades) mostra:

- **Squads**: Todos os squads instalados, clique para executar
- **Tarefas Agendadas**: Tarefas recorrentes com próxima execução
- **Memória**: Estatísticas da memória persistente do agente

## Menu de Contexto

Clique com o botão direito em qualquer seleção de código para acessar:

- Revisar Código Selecionado
- Explicar Código
- Corrigir Código
- Gerar Testes

## Barra de Status

O ícone na barra de status inferior mostra o estado da conexão:

- `✓ PicoClaw` — Conectado e funcionando
- `⟳ PicoClaw` — Conectando...
- `✗ PicoClaw` — Offline (clique para reconectar)

## Desenvolvimento

```bash
# Instalar dependências
npm install

# Compilar
npm run compile

# Modo watch (recompila automaticamente)
npm run watch

# Empacotar extensão
npm run package
```

## Licença

MIT — Parte do projeto Synapse AI Platform
