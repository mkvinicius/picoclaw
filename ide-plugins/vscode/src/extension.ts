/**
 * PicoClaw Synapse - VS Code Extension
 * Connects your IDE to the local PicoClaw AI agent orchestration system.
 */

import * as vscode from 'vscode';

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

interface PicoClawConfig {
  serverUrl: string;
  apiKey: string;
  autoConnect: boolean;
  language: string;
  inlineCompletions: boolean;
}

interface AgentResponse {
  success: boolean;
  output: string;
  error?: string;
  duration_ms?: number;
}

interface Squad {
  id: string;
  name: string;
  description: string;
  category: string;
  active: boolean;
}

interface ScheduledTask {
  id: string;
  name: string;
  schedule: string;
  status: string;
  next_run: string;
}

// ─────────────────────────────────────────────────────────────────────────────
// Extension State
// ─────────────────────────────────────────────────────────────────────────────

let statusBarItem: vscode.StatusBarItem;
let outputChannel: vscode.OutputChannel;
let isConnected = false;
let connectionCheckInterval: NodeJS.Timeout | undefined;

// ─────────────────────────────────────────────────────────────────────────────
// Activation
// ─────────────────────────────────────────────────────────────────────────────

export function activate(context: vscode.ExtensionContext) {
  outputChannel = vscode.window.createOutputChannel('PicoClaw Synapse');
  outputChannel.appendLine('🚀 PicoClaw Synapse ativado');

  // Status bar
  statusBarItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  statusBarItem.command = 'picoclaw.openDashboard';
  statusBarItem.text = '$(loading~spin) PicoClaw';
  statusBarItem.tooltip = 'PicoClaw Synapse - Conectando...';
  statusBarItem.show();
  context.subscriptions.push(statusBarItem);

  // Register commands
  context.subscriptions.push(
    vscode.commands.registerCommand('picoclaw.connect', cmdConnect),
    vscode.commands.registerCommand('picoclaw.askAgent', cmdAskAgent),
    vscode.commands.registerCommand('picoclaw.reviewCode', cmdReviewCode),
    vscode.commands.registerCommand('picoclaw.explainCode', cmdExplainCode),
    vscode.commands.registerCommand('picoclaw.fixCode', cmdFixCode),
    vscode.commands.registerCommand('picoclaw.generateTests', cmdGenerateTests),
    vscode.commands.registerCommand('picoclaw.runSquad', cmdRunSquad),
    vscode.commands.registerCommand('picoclaw.openDashboard', cmdOpenDashboard),
    vscode.commands.registerCommand('picoclaw.scheduleTask', cmdScheduleTask),
  );

  // Register tree data providers
  vscode.window.registerTreeDataProvider('picoclaw.squads', new SquadsProvider());
  vscode.window.registerTreeDataProvider('picoclaw.tasks', new TasksProvider());
  vscode.window.registerTreeDataProvider('picoclaw.memory', new MemoryProvider());

  // Auto-connect
  const config = getConfig();
  if (config.autoConnect) {
    setTimeout(() => cmdConnect(), 1000);
  }

  // Periodic connection check
  connectionCheckInterval = setInterval(checkConnection, 30000);
  context.subscriptions.push({ dispose: () => clearInterval(connectionCheckInterval) });
}

export function deactivate() {
  if (connectionCheckInterval) {
    clearInterval(connectionCheckInterval);
  }
  outputChannel.appendLine('PicoClaw Synapse desativado');
}

// ─────────────────────────────────────────────────────────────────────────────
// Commands
// ─────────────────────────────────────────────────────────────────────────────

async function cmdConnect() {
  const config = getConfig();
  statusBarItem.text = '$(loading~spin) PicoClaw';
  statusBarItem.tooltip = 'Conectando...';

  try {
    const resp = await fetch(`${config.serverUrl}/api/health`, {
      headers: apiHeaders(config),
      signal: AbortSignal.timeout(5000),
    });

    if (resp.ok) {
      const data = await resp.json() as { version?: string; node_id?: string };
      isConnected = true;
      statusBarItem.text = '$(check) PicoClaw';
      statusBarItem.tooltip = `PicoClaw Synapse conectado\nNó: ${data.node_id || 'local'}\nVersão: ${data.version || '2.0'}`;
      statusBarItem.backgroundColor = undefined;
      outputChannel.appendLine(`✅ Conectado ao PicoClaw: ${config.serverUrl}`);
    } else {
      throw new Error(`HTTP ${resp.status}`);
    }
  } catch (err) {
    isConnected = false;
    statusBarItem.text = '$(error) PicoClaw';
    statusBarItem.tooltip = `PicoClaw offline\nInicie com: picoclaw start\n${config.serverUrl}`;
    statusBarItem.backgroundColor = new vscode.ThemeColor('statusBarItem.errorBackground');
    outputChannel.appendLine(`❌ PicoClaw offline: ${err}`);
  }
}

async function cmdAskAgent() {
  const question = await vscode.window.showInputBox({
    prompt: 'O que você quer perguntar ao agente?',
    placeHolder: 'Ex: Como posso otimizar esta função?',
  });

  if (!question) return;

  // Include current file context
  const editor = vscode.window.activeTextEditor;
  let context = '';
  if (editor) {
    const doc = editor.document;
    const selection = editor.selection;
    if (!selection.isEmpty) {
      context = `\n\nCódigo selecionado (${doc.languageId}):\n\`\`\`${doc.languageId}\n${doc.getText(selection)}\n\`\`\``;
    } else {
      // Include surrounding context (50 lines)
      const line = selection.active.line;
      const start = Math.max(0, line - 25);
      const end = Math.min(doc.lineCount - 1, line + 25);
      const range = new vscode.Range(start, 0, end, doc.lineAt(end).text.length);
      context = `\n\nArquivo: ${doc.fileName}\nLinha atual: ${line + 1}\n\`\`\`${doc.languageId}\n${doc.getText(range)}\n\`\`\``;
    }
  }

  await runWithProgress(`Perguntando ao agente...`, async () => {
    const response = await callAgent('ask', { question: question + context });
    if (response.success) {
      showAgentResponse(question, response.output);
    } else {
      vscode.window.showErrorMessage(`Erro: ${response.error}`);
    }
  });
}

async function cmdReviewCode() {
  const code = getSelectedCode();
  if (!code) {
    vscode.window.showWarningMessage('Selecione o código que deseja revisar');
    return;
  }

  await runWithProgress('Revisando código...', async () => {
    const response = await callAgent('review_code', {
      code,
      language: vscode.window.activeTextEditor?.document.languageId || 'unknown',
    });
    if (response.success) {
      showAgentResponse('Revisão de Código', response.output);
    }
  });
}

async function cmdExplainCode() {
  const code = getSelectedCode();
  if (!code) {
    vscode.window.showWarningMessage('Selecione o código que deseja explicar');
    return;
  }

  await runWithProgress('Explicando código...', async () => {
    const response = await callAgent('explain_code', {
      code,
      language: vscode.window.activeTextEditor?.document.languageId || 'unknown',
    });
    if (response.success) {
      showAgentResponse('Explicação do Código', response.output);
    }
  });
}

async function cmdFixCode() {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;

  const code = getSelectedCode();
  if (!code) {
    vscode.window.showWarningMessage('Selecione o código que deseja corrigir');
    return;
  }

  await runWithProgress('Corrigindo código...', async () => {
    const response = await callAgent('fix_code', {
      code,
      language: editor.document.languageId,
    });

    if (response.success) {
      // Ask user if they want to apply the fix
      const choice = await vscode.window.showInformationMessage(
        'Correção encontrada. Deseja aplicar?',
        'Aplicar', 'Ver Primeiro', 'Cancelar'
      );

      if (choice === 'Aplicar') {
        // Extract code block from response
        const fixedCode = extractCodeBlock(response.output);
        if (fixedCode) {
          editor.edit(editBuilder => {
            editBuilder.replace(editor.selection, fixedCode);
          });
          vscode.window.showInformationMessage('✅ Código corrigido!');
        }
      } else if (choice === 'Ver Primeiro') {
        showAgentResponse('Código Corrigido', response.output);
      }
    }
  });
}

async function cmdGenerateTests() {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return;

  const code = getSelectedCode() || editor.document.getText();

  await runWithProgress('Gerando testes...', async () => {
    const response = await callAgent('generate_tests', {
      code,
      language: editor.document.languageId,
      framework: detectTestFramework(editor.document.languageId),
    });

    if (response.success) {
      // Create new file with tests
      const testContent = extractCodeBlock(response.output) || response.output;
      const testDoc = await vscode.workspace.openTextDocument({
        content: testContent,
        language: editor.document.languageId,
      });
      vscode.window.showTextDocument(testDoc, vscode.ViewColumn.Beside);
    }
  });
}

async function cmdRunSquad() {
  const config = getConfig();

  // Fetch available squads
  let squads: Squad[] = [];
  try {
    const resp = await fetch(`${config.serverUrl}/api/squads`, { headers: apiHeaders(config) });
    squads = await resp.json() as Squad[];
  } catch {
    vscode.window.showErrorMessage('Não foi possível carregar os squads. PicoClaw está rodando?');
    return;
  }

  const items = squads.map(s => ({
    label: s.name,
    description: s.category,
    detail: s.description,
    id: s.id,
  }));

  const selected = await vscode.window.showQuickPick(items, {
    placeHolder: 'Selecione um squad para executar',
    matchOnDescription: true,
    matchOnDetail: true,
  });

  if (!selected) return;

  const input = await vscode.window.showInputBox({
    prompt: `Instrução para o squad "${selected.label}"`,
    placeHolder: 'O que você quer que o squad faça?',
  });

  if (!input) return;

  await runWithProgress(`Executando squad "${selected.label}"...`, async () => {
    const response = await callAgent('run_squad', { squad_id: selected.id, input });
    if (response.success) {
      showAgentResponse(`Squad: ${selected.label}`, response.output);
    } else {
      vscode.window.showErrorMessage(`Erro ao executar squad: ${response.error}`);
    }
  });
}

async function cmdOpenDashboard() {
  const config = getConfig();
  vscode.env.openExternal(vscode.Uri.parse(config.serverUrl));
}

async function cmdScheduleTask() {
  const name = await vscode.window.showInputBox({
    prompt: 'Nome da tarefa',
    placeHolder: 'Ex: Revisão diária de código',
  });
  if (!name) return;

  const schedule = await vscode.window.showInputBox({
    prompt: 'Quando executar?',
    placeHolder: 'Ex: todo dia às 9h, toda segunda às 8h30',
  });
  if (!schedule) return;

  const command = await vscode.window.showInputBox({
    prompt: 'O que executar?',
    placeHolder: 'Ex: Revisar todos os arquivos modificados hoje',
  });
  if (!command) return;

  const config = getConfig();
  try {
    const resp = await fetch(`${config.serverUrl}/api/scheduler/tasks`, {
      method: 'POST',
      headers: { ...apiHeaders(config), 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, schedule, command }),
    });

    if (resp.ok) {
      vscode.window.showInformationMessage(`✅ Tarefa agendada: "${name}" (${schedule})`);
    } else {
      vscode.window.showErrorMessage('Erro ao agendar tarefa');
    }
  } catch (err) {
    vscode.window.showErrorMessage(`Erro: ${err}`);
  }
}

// ─────────────────────────────────────────────────────────────────────────────
// Tree Data Providers
// ─────────────────────────────────────────────────────────────────────────────

class SquadsProvider implements vscode.TreeDataProvider<vscode.TreeItem> {
  getTreeItem(element: vscode.TreeItem): vscode.TreeItem { return element; }

  async getChildren(): Promise<vscode.TreeItem[]> {
    if (!isConnected) {
      return [new vscode.TreeItem('PicoClaw offline', vscode.TreeItemCollapsibleState.None)];
    }
    const config = getConfig();
    try {
      const resp = await fetch(`${config.serverUrl}/api/squads/installed`, { headers: apiHeaders(config) });
      const squads = await resp.json() as Squad[];
      return squads.map(s => {
        const item = new vscode.TreeItem(s.name, vscode.TreeItemCollapsibleState.None);
        item.description = s.category;
        item.tooltip = s.description;
        item.iconPath = new vscode.ThemeIcon(s.active ? 'check' : 'circle-outline');
        item.command = { command: 'picoclaw.runSquad', title: 'Executar', arguments: [s.id] };
        return item;
      });
    } catch {
      return [new vscode.TreeItem('Erro ao carregar squads')];
    }
  }
}

class TasksProvider implements vscode.TreeDataProvider<vscode.TreeItem> {
  getTreeItem(element: vscode.TreeItem): vscode.TreeItem { return element; }

  async getChildren(): Promise<vscode.TreeItem[]> {
    if (!isConnected) return [];
    const config = getConfig();
    try {
      const resp = await fetch(`${config.serverUrl}/api/scheduler/tasks`, { headers: apiHeaders(config) });
      const tasks = await resp.json() as ScheduledTask[];
      return tasks.map(t => {
        const item = new vscode.TreeItem(t.name, vscode.TreeItemCollapsibleState.None);
        item.description = t.schedule;
        item.tooltip = `Próxima execução: ${t.next_run}`;
        item.iconPath = new vscode.ThemeIcon(t.status === 'active' ? 'calendar' : 'circle-slash');
        return item;
      });
    } catch {
      return [new vscode.TreeItem('Sem tarefas agendadas')];
    }
  }
}

class MemoryProvider implements vscode.TreeDataProvider<vscode.TreeItem> {
  getTreeItem(element: vscode.TreeItem): vscode.TreeItem { return element; }

  async getChildren(): Promise<vscode.TreeItem[]> {
    if (!isConnected) return [];
    const config = getConfig();
    try {
      const resp = await fetch(`${config.serverUrl}/api/memory/stats`, { headers: apiHeaders(config) });
      const stats = await resp.json() as Record<string, unknown>;
      return [
        makeStatItem('Memórias', String(stats.total_memories || 0), 'database'),
        makeStatItem('Conversas', String(stats.total_turns || 0), 'comment-discussion'),
        makeStatItem('Sessões', String(stats.total_sessions || 0), 'history'),
      ];
    } catch {
      return [new vscode.TreeItem('Memória não disponível')];
    }
  }
}

function makeStatItem(label: string, value: string, icon: string): vscode.TreeItem {
  const item = new vscode.TreeItem(label, vscode.TreeItemCollapsibleState.None);
  item.description = value;
  item.iconPath = new vscode.ThemeIcon(icon);
  return item;
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

function getConfig(): PicoClawConfig {
  const cfg = vscode.workspace.getConfiguration('picoclaw');
  return {
    serverUrl: cfg.get('serverUrl', 'http://localhost:8080'),
    apiKey: cfg.get('apiKey', ''),
    autoConnect: cfg.get('autoConnect', true),
    language: cfg.get('language', 'pt-BR'),
    inlineCompletions: cfg.get('inlineCompletions', false),
  };
}

function apiHeaders(config: PicoClawConfig): Record<string, string> {
  const headers: Record<string, string> = {
    'Accept': 'application/json',
    'X-PicoClaw-Client': 'vscode-extension',
  };
  if (config.apiKey) {
    headers['Authorization'] = `Bearer ${config.apiKey}`;
  }
  return headers;
}

async function callAgent(action: string, params: Record<string, unknown>): Promise<AgentResponse> {
  const config = getConfig();
  try {
    const resp = await fetch(`${config.serverUrl}/api/agent/${action}`, {
      method: 'POST',
      headers: { ...apiHeaders(config), 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...params, language: config.language }),
      signal: AbortSignal.timeout(60000),
    });

    if (!resp.ok) {
      return { success: false, output: '', error: `HTTP ${resp.status}` };
    }

    return await resp.json() as AgentResponse;
  } catch (err) {
    return { success: false, output: '', error: String(err) };
  }
}

function getSelectedCode(): string | undefined {
  const editor = vscode.window.activeTextEditor;
  if (!editor) return undefined;
  const selection = editor.selection;
  if (selection.isEmpty) return undefined;
  return editor.document.getText(selection);
}

function showAgentResponse(title: string, content: string) {
  // Create a markdown preview panel
  const panel = vscode.window.createWebviewPanel(
    'picoclaw.response',
    `PicoClaw: ${title}`,
    vscode.ViewColumn.Beside,
    { enableScripts: false }
  );

  panel.webview.html = `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<style>
  body { font-family: var(--vscode-font-family); padding: 20px; color: var(--vscode-foreground); background: var(--vscode-editor-background); }
  h1 { color: var(--vscode-textLink-foreground); border-bottom: 1px solid var(--vscode-panel-border); padding-bottom: 8px; }
  pre { background: var(--vscode-textBlockQuote-background); padding: 12px; border-radius: 4px; overflow-x: auto; }
  code { font-family: var(--vscode-editor-font-family); }
  .badge { background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); padding: 2px 8px; border-radius: 10px; font-size: 12px; }
</style>
</head>
<body>
<h1>🤖 ${title}</h1>
<div class="content">${markdownToHtml(content)}</div>
</body>
</html>`;
}

function markdownToHtml(md: string): string {
  return md
    .replace(/```(\w+)?\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>')
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\*([^*]+)\*/g, '<em>$1</em>')
    .replace(/^### (.+)$/gm, '<h3>$1</h3>')
    .replace(/^## (.+)$/gm, '<h2>$1</h2>')
    .replace(/^# (.+)$/gm, '<h1>$1</h1>')
    .replace(/^- (.+)$/gm, '<li>$1</li>')
    .replace(/\n\n/g, '</p><p>')
    .replace(/^(?!<[hpl])/gm, '<p>')
    + '</p>';
}

function extractCodeBlock(text: string): string | undefined {
  const match = text.match(/```\w*\n([\s\S]*?)```/);
  return match ? match[1].trim() : undefined;
}

function detectTestFramework(language: string): string {
  const frameworks: Record<string, string> = {
    typescript: 'jest',
    javascript: 'jest',
    python: 'pytest',
    go: 'testing',
    rust: 'cargo test',
    java: 'junit',
    csharp: 'xunit',
  };
  return frameworks[language] || 'default';
}

async function checkConnection() {
  if (!isConnected) {
    await cmdConnect();
  }
}

async function runWithProgress(title: string, fn: () => Promise<void>) {
  await vscode.window.withProgress(
    { location: vscode.ProgressLocation.Notification, title, cancellable: false },
    fn
  );
}
