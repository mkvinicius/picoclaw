package dashboard

// dashboardHTML is the complete single-page dashboard embedded in the binary.
// It uses vanilla JS + CSS (no build step, no npm, no external dependencies).
const dashboardHTML = `<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>PicoClaw — Synapse Dashboard</title>
<style>
  :root {
    --bg: #0d1117; --surface: #161b22; --border: #30363d;
    --text: #e6edf3; --muted: #8b949e; --accent: #7c3aed;
    --green: #3fb950; --yellow: #d29922; --red: #f85149;
    --blue: #58a6ff;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: var(--bg); color: var(--text); font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; font-size: 14px; }
  .layout { display: grid; grid-template-columns: 220px 1fr; grid-template-rows: 56px 1fr; min-height: 100vh; }
  
  /* Header */
  header { grid-column: 1/-1; background: var(--surface); border-bottom: 1px solid var(--border); display: flex; align-items: center; padding: 0 20px; gap: 12px; }
  .logo { font-size: 18px; font-weight: 700; color: var(--accent); }
  .logo span { color: var(--text); }
  .status-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--green); animation: pulse 2s infinite; }
  @keyframes pulse { 0%,100%{opacity:1} 50%{opacity:.4} }
  .header-stats { margin-left: auto; display: flex; gap: 20px; }
  .stat-pill { background: var(--bg); border: 1px solid var(--border); border-radius: 20px; padding: 4px 12px; font-size: 12px; color: var(--muted); }
  .stat-pill strong { color: var(--text); }

  /* Sidebar */
  aside { background: var(--surface); border-right: 1px solid var(--border); padding: 16px 0; }
  .nav-item { display: flex; align-items: center; gap: 10px; padding: 10px 16px; cursor: pointer; color: var(--muted); transition: all .15s; border-left: 3px solid transparent; }
  .nav-item:hover { background: rgba(124,58,237,.1); color: var(--text); }
  .nav-item.active { background: rgba(124,58,237,.15); color: var(--accent); border-left-color: var(--accent); }
  .nav-icon { font-size: 16px; width: 20px; text-align: center; }

  /* Main */
  main { padding: 20px; overflow-y: auto; }
  .page { display: none; }
  .page.active { display: block; }
  
  /* Cards */
  .grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 16px; }
  .grid-3 { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; margin-bottom: 16px; }
  .grid-4 { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; margin-bottom: 16px; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 16px; }
  .card-title { font-size: 12px; color: var(--muted); text-transform: uppercase; letter-spacing: .5px; margin-bottom: 8px; }
  .card-value { font-size: 28px; font-weight: 700; }
  .card-sub { font-size: 12px; color: var(--muted); margin-top: 4px; }

  /* Agent cards */
  .agent-card { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 14px; display: flex; align-items: center; gap: 12px; }
  .agent-avatar { width: 40px; height: 40px; border-radius: 50%; background: var(--accent); display: flex; align-items: center; justify-content: center; font-size: 18px; flex-shrink: 0; }
  .agent-info { flex: 1; min-width: 0; }
  .agent-name { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .agent-task { font-size: 12px; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .agent-status { font-size: 11px; padding: 2px 8px; border-radius: 10px; flex-shrink: 0; }
  .status-running { background: rgba(63,185,80,.15); color: var(--green); }
  .status-idle { background: rgba(139,148,158,.15); color: var(--muted); }
  .status-error { background: rgba(248,81,73,.15); color: var(--red); }
  .progress-bar { height: 3px; background: var(--border); border-radius: 2px; margin-top: 6px; }
  .progress-fill { height: 100%; background: var(--accent); border-radius: 2px; transition: width .3s; }

  /* Security events */
  .event-row { display: flex; align-items: flex-start; gap: 10px; padding: 10px 0; border-bottom: 1px solid var(--border); }
  .event-row:last-child { border-bottom: none; }
  .event-badge { font-size: 10px; padding: 2px 6px; border-radius: 4px; flex-shrink: 0; margin-top: 2px; }
  .badge-critical { background: rgba(248,81,73,.2); color: var(--red); }
  .badge-warning { background: rgba(210,153,34,.2); color: var(--yellow); }
  .badge-info { background: rgba(88,166,255,.2); color: var(--blue); }
  .event-msg { font-size: 13px; }
  .event-time { font-size: 11px; color: var(--muted); margin-top: 2px; }

  /* Command input */
  .command-box { display: flex; gap: 10px; margin-bottom: 20px; }
  .command-input { flex: 1; background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 10px 14px; color: var(--text); font-size: 14px; outline: none; transition: border-color .15s; }
  .command-input:focus { border-color: var(--accent); }
  .command-btn { background: var(--accent); color: white; border: none; border-radius: 8px; padding: 10px 20px; cursor: pointer; font-size: 14px; font-weight: 600; transition: opacity .15s; }
  .command-btn:hover { opacity: .85; }

  /* Squad builder */
  .squad-builder { border: 2px dashed var(--border); border-radius: 8px; padding: 20px; text-align: center; color: var(--muted); cursor: pointer; transition: all .15s; margin-bottom: 16px; }
  .squad-builder:hover { border-color: var(--accent); color: var(--accent); }

  /* Schedule */
  .schedule-item { display: flex; align-items: center; gap: 12px; padding: 12px 0; border-bottom: 1px solid var(--border); }
  .schedule-item:last-child { border-bottom: none; }
  .schedule-icon { font-size: 20px; }
  .schedule-info { flex: 1; }
  .schedule-name { font-weight: 600; }
  .schedule-time { font-size: 12px; color: var(--muted); }
  .toggle { width: 36px; height: 20px; background: var(--border); border-radius: 10px; cursor: pointer; position: relative; transition: background .2s; }
  .toggle.on { background: var(--accent); }
  .toggle::after { content:''; position:absolute; top:2px; left:2px; width:16px; height:16px; background:white; border-radius:50%; transition: transform .2s; }
  .toggle.on::after { transform: translateX(16px); }

  /* Connection banner */
  .banner { background: rgba(124,58,237,.1); border: 1px solid rgba(124,58,237,.3); border-radius: 8px; padding: 12px 16px; margin-bottom: 16px; display: flex; align-items: center; gap: 10px; font-size: 13px; }

  h2 { font-size: 18px; margin-bottom: 16px; }
  h3 { font-size: 14px; margin-bottom: 12px; color: var(--muted); text-transform: uppercase; letter-spacing: .5px; }
</style>
</head>
<body>
<div class="layout">
  <header>
    <div class="logo">Pico<span>Claw</span></div>
    <div class="status-dot" id="conn-dot"></div>
    <span id="conn-text" style="font-size:12px;color:var(--muted)">Conectando...</span>
    <div class="header-stats">
      <div class="stat-pill">RAM: <strong id="h-ram">--</strong></div>
      <div class="stat-pill">Agentes: <strong id="h-agents">0</strong></div>
      <div class="stat-pill">Uptime: <strong id="h-uptime">--</strong></div>
      <div class="stat-pill">APEX: <strong id="h-apex" style="color:var(--muted)">Desconectado</strong></div>
    </div>
  </header>

  <aside>
    <div class="nav-item active" onclick="showPage('overview')"><span class="nav-icon">🏠</span> Visão Geral</div>
    <div class="nav-item" onclick="showPage('agents')"><span class="nav-icon">🤖</span> Agentes</div>
    <div class="nav-item" onclick="showPage('squads')"><span class="nav-icon">👥</span> Squads</div>
    <div class="nav-item" onclick="showPage('schedule')"><span class="nav-icon">📅</span> Agendamentos</div>
    <div class="nav-item" onclick="showPage('security')"><span class="nav-icon">🛡️</span> Segurança</div>
    <div class="nav-item" onclick="showPage('home')"><span class="nav-icon">🏡</span> Casa Inteligente</div>
    <div class="nav-item" onclick="showPage('command')"><span class="nav-icon">💬</span> Comandos</div>
    <div class="nav-item" onclick="showPage('settings')"><span class="nav-icon">⚙️</span> Configurações</div>
  </aside>

  <main>
    <!-- OVERVIEW -->
    <div class="page active" id="page-overview">
      <h2>Visão Geral</h2>
      <div class="grid-4">
        <div class="card"><div class="card-title">Agentes Ativos</div><div class="card-value" id="stat-agents">0</div><div class="card-sub">de 0 total</div></div>
        <div class="card"><div class="card-title">RAM Usada</div><div class="card-value" id="stat-ram">--</div><div class="card-sub">MB</div></div>
        <div class="card"><div class="card-title">Eventos de Segurança</div><div class="card-value" id="stat-sec">0</div><div class="card-sub">hoje</div></div>
        <div class="card"><div class="card-title">Tarefas Agendadas</div><div class="card-value" id="stat-tasks">0</div><div class="card-sub">ativas</div></div>
      </div>
      <div class="grid-2">
        <div class="card">
          <h3>Agentes em Execução</h3>
          <div id="overview-agents"><div style="color:var(--muted);font-size:13px">Nenhum agente rodando</div></div>
        </div>
        <div class="card">
          <h3>Últimos Eventos de Segurança</h3>
          <div id="overview-security"><div style="color:var(--muted);font-size:13px">Nenhum evento registrado</div></div>
        </div>
      </div>
    </div>

    <!-- AGENTS -->
    <div class="page" id="page-agents">
      <h2>Agentes</h2>
      <div class="command-box">
        <input class="command-input" id="agent-cmd" placeholder="Criar novo agente... ex: 'Crie um agente pesquisador de notícias'" />
        <button class="command-btn" onclick="sendCommand('agent-cmd')">Criar</button>
      </div>
      <div id="agents-grid" style="display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:12px">
        <div style="color:var(--muted);font-size:13px;padding:20px">Nenhum agente criado ainda. Digite um comando acima para criar.</div>
      </div>
    </div>

    <!-- SQUADS -->
    <div class="page" id="page-squads">
      <h2>Squads</h2>
      <div class="squad-builder" onclick="createSquad()">
        <div style="font-size:32px;margin-bottom:8px">+</div>
        <div>Criar novo Squad</div>
        <div style="font-size:12px;margin-top:4px">Agrupe agentes para trabalhar juntos em uma tarefa</div>
      </div>
      <div id="squads-list"><div style="color:var(--muted);font-size:13px;padding:20px">Nenhum squad criado ainda.</div></div>
    </div>

    <!-- SCHEDULE -->
    <div class="page" id="page-schedule">
      <h2>Agendamentos</h2>
      <div class="command-box">
        <input class="command-input" id="schedule-cmd" placeholder="Agendar tarefa... ex: 'Todo dia às 8h, me manda resumo das notícias'" />
        <button class="command-btn" onclick="scheduleTask()">Agendar</button>
      </div>
      <div class="card">
        <div id="schedule-list"><div style="color:var(--muted);font-size:13px">Nenhuma tarefa agendada.</div></div>
      </div>
    </div>

    <!-- SECURITY -->
    <div class="page" id="page-security">
      <h2>Segurança MIL-SPEC</h2>
      <div class="grid-3">
        <div class="card"><div class="card-title">Semantic Firewall</div><div class="card-value" style="color:var(--green)">✅ Ativo</div><div class="card-sub">7 camadas de proteção</div></div>
        <div class="card"><div class="card-title">Tri-Brain</div><div class="card-value" style="color:var(--green)">✅ Ativo</div><div class="card-sub">3 validações por resposta</div></div>
        <div class="card"><div class="card-title">Ghost Vault</div><div class="card-value" style="color:var(--green)">🔒 Protegido</div><div class="card-sub">Chaves criptografadas</div></div>
      </div>
      <div class="card">
        <h3>Log de Eventos</h3>
        <div id="security-log"><div style="color:var(--muted);font-size:13px">Nenhum evento registrado. Sistema seguro.</div></div>
      </div>
    </div>

    <!-- HOME AUTOMATION -->
    <div class="page" id="page-home">
      <h2>Casa Inteligente</h2>
      <div class="banner">🏡 Conecte ao Home Assistant em Configurações para controlar seus dispositivos aqui.</div>
      <div class="command-box">
        <input class="command-input" id="home-cmd" placeholder="Controle sua casa... ex: 'Liga as luzes da sala' ou 'Modo noite'" />
        <button class="command-btn" onclick="sendCommand('home-cmd')">Executar</button>
      </div>
      <div class="grid-2">
        <div class="card"><h3>Dispositivos</h3><div id="home-devices"><div style="color:var(--muted);font-size:13px">Nenhum dispositivo conectado.</div></div></div>
        <div class="card"><h3>Câmeras</h3><div id="home-cameras"><div style="color:var(--muted);font-size:13px">Nenhuma câmera configurada.</div></div></div>
      </div>
    </div>

    <!-- COMMAND -->
    <div class="page" id="page-command">
      <h2>Comandos</h2>
      <div class="command-box">
        <input class="command-input" id="main-cmd" placeholder="Digite um comando em linguagem natural..." onkeydown="if(event.key==='Enter')sendCommand('main-cmd')" />
        <button class="command-btn" onclick="sendCommand('main-cmd')">Enviar</button>
      </div>
      <div class="card" style="height:400px;overflow-y:auto">
        <div id="cmd-history"><div style="color:var(--muted);font-size:13px">Histórico de comandos aparecerá aqui.</div></div>
      </div>
    </div>

    <!-- SETTINGS -->
    <div class="page" id="page-settings">
      <h2>Configurações</h2>
      <div class="card" style="margin-bottom:16px">
        <h3>Modo de Raciocínio</h3>
        <div style="display:flex;gap:10px;margin-top:8px">
          <button class="command-btn" onclick="setMode('online')" style="background:var(--blue)">🌐 Online</button>
          <button class="command-btn" onclick="setMode('offline')" style="background:var(--green)">💻 Offline</button>
          <button class="command-btn" onclick="setMode('auto')">🔄 Auto</button>
        </div>
      </div>
      <div class="card" style="margin-bottom:16px">
        <h3>Conexão com APEX</h3>
        <div style="display:flex;gap:10px;align-items:center;margin-top:8px">
          <input class="command-input" id="apex-url" placeholder="URL do APEX (ex: https://apex.suaempresa.com)" style="flex:1" />
          <button class="command-btn" onclick="connectAPEX()">Conectar</button>
        </div>
      </div>
      <div class="card">
        <h3>Home Assistant</h3>
        <div style="display:flex;flex-direction:column;gap:8px;margin-top:8px">
          <input class="command-input" id="ha-url" placeholder="URL do Home Assistant (ex: http://homeassistant.local:8123)" />
          <input class="command-input" id="ha-token" placeholder="Token de acesso longo" type="password" />
          <button class="command-btn" onclick="saveHA()" style="align-self:flex-start">Salvar</button>
        </div>
      </div>
    </div>
  </main>
</div>

<script>
let state = { agents: [], squads: [], security_events: [], scheduled_tasks: [], system_stats: {}, apex_connected: false };
let evtSource = null;

function connect() {
  evtSource = new EventSource('/ws');
  evtSource.onopen = () => {
    document.getElementById('conn-dot').style.background = 'var(--green)';
    document.getElementById('conn-text').textContent = 'Conectado';
  };
  evtSource.onerror = () => {
    document.getElementById('conn-dot').style.background = 'var(--red)';
    document.getElementById('conn-text').textContent = 'Reconectando...';
    setTimeout(connect, 3000);
  };
  evtSource.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data);
      handleMessage(msg);
    } catch(err) {}
  };
}

function handleMessage(msg) {
  if (msg.type === 'stats_update') {
    updateStats(msg.data);
  } else if (msg.type === 'agent_update' || msg.type === 'agent_added') {
    fetchState();
  } else if (msg.type === 'security_event') {
    fetchState();
  }
}

function fetchState() {
  fetch('/api/state').then(r => r.json()).then(s => {
    state = s;
    renderAll();
  });
}

function updateStats(stats) {
  document.getElementById('h-ram').textContent = stats.ram_used_mb + ' MB';
  document.getElementById('h-agents').textContent = stats.active_agents || 0;
  document.getElementById('h-uptime').textContent = formatUptime(stats.uptime_seconds);
  document.getElementById('stat-agents').textContent = stats.active_agents || 0;
  document.getElementById('stat-ram').textContent = stats.ram_used_mb || '--';
}

function renderAll() {
  renderAgents();
  renderSecurity();
  renderSchedule();
  document.getElementById('stat-sec').textContent = state.security_events ? state.security_events.length : 0;
  document.getElementById('stat-tasks').textContent = state.scheduled_tasks ? state.scheduled_tasks.filter(t => t.status === 'active').length : 0;
  document.getElementById('h-apex').textContent = state.apex_connected ? 'Conectado' : 'Desconectado';
  document.getElementById('h-apex').style.color = state.apex_connected ? 'var(--green)' : 'var(--muted)';
}

function renderAgents() {
  const agents = state.agents || [];
  const grid = document.getElementById('agents-grid');
  const overview = document.getElementById('overview-agents');
  if (!agents.length) {
    grid.innerHTML = '<div style="color:var(--muted);font-size:13px;padding:20px">Nenhum agente criado ainda.</div>';
    overview.innerHTML = '<div style="color:var(--muted);font-size:13px">Nenhum agente rodando</div>';
    return;
  }
  const icons = { researcher: '🔍', writer: '✍️', designer: '🎨', publisher: '📤', security: '🛡️', default: '🤖' };
  grid.innerHTML = agents.map(a => `
    <div class="agent-card">
      <div class="agent-avatar">${icons[a.role] || icons.default}</div>
      <div class="agent-info">
        <div class="agent-name">${a.name}</div>
        <div class="agent-task">${a.task || 'Aguardando tarefa'}</div>
        <div class="progress-bar"><div class="progress-fill" style="width:${a.progress||0}%"></div></div>
      </div>
      <div class="agent-status status-${a.status}">${a.status}</div>
    </div>`).join('');
  const running = agents.filter(a => a.status === 'running');
  overview.innerHTML = running.length ? running.map(a => `
    <div class="agent-card" style="margin-bottom:8px">
      <div class="agent-avatar" style="width:32px;height:32px;font-size:14px">${icons[a.role]||icons.default}</div>
      <div class="agent-info"><div class="agent-name">${a.name}</div><div class="agent-task">${a.task||''}</div></div>
      <div class="agent-status status-running">rodando</div>
    </div>`).join('') : '<div style="color:var(--muted);font-size:13px">Nenhum agente rodando</div>';
}

function renderSecurity() {
  const events = state.security_events || [];
  const log = document.getElementById('security-log');
  const overview = document.getElementById('overview-security');
  if (!events.length) {
    log.innerHTML = '<div style="color:var(--muted);font-size:13px">Nenhum evento. Sistema seguro. ✅</div>';
    overview.innerHTML = '<div style="color:var(--muted);font-size:13px">Nenhum evento registrado</div>';
    return;
  }
  const html = events.slice(0,20).map(e => `
    <div class="event-row">
      <div class="event-badge badge-${e.severity}">${e.severity.toUpperCase()}</div>
      <div><div class="event-msg">${e.message}</div><div class="event-time">${new Date(e.timestamp).toLocaleTimeString('pt-BR')}</div></div>
    </div>`).join('');
  log.innerHTML = html;
  overview.innerHTML = events.slice(0,5).map(e => `
    <div class="event-row">
      <div class="event-badge badge-${e.severity}">${e.severity}</div>
      <div class="event-msg" style="font-size:12px">${e.message}</div>
    </div>`).join('');
}

function renderSchedule() {
  const tasks = state.scheduled_tasks || [];
  const list = document.getElementById('schedule-list');
  if (!tasks.length) { list.innerHTML = '<div style="color:var(--muted);font-size:13px">Nenhuma tarefa agendada.</div>'; return; }
  list.innerHTML = tasks.map(t => `
    <div class="schedule-item">
      <div class="schedule-icon">📅</div>
      <div class="schedule-info">
        <div class="schedule-name">${t.name}</div>
        <div class="schedule-time">${t.schedule} · Próxima: ${t.next_run ? new Date(t.next_run).toLocaleString('pt-BR') : '--'}</div>
      </div>
      <div class="toggle ${t.status==='active'?'on':''}" onclick="toggleTask('${t.id}')"></div>
    </div>`).join('');
}

function showPage(name) {
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
  document.getElementById('page-' + name).classList.add('active');
  event.currentTarget.classList.add('active');
}

function sendCommand(inputId) {
  const input = document.getElementById(inputId);
  const cmd = input.value.trim();
  if (!cmd) return;
  fetch('/api/command', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({command: cmd}) });
  const history = document.getElementById('cmd-history');
  history.innerHTML = `<div style="padding:8px 0;border-bottom:1px solid var(--border)"><span style="color:var(--accent)">▶</span> ${cmd}</div>` + history.innerHTML;
  input.value = '';
}

function scheduleTask() {
  const input = document.getElementById('schedule-cmd');
  const cmd = input.value.trim();
  if (!cmd) return;
  fetch('/api/schedule', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({name: cmd, schedule: cmd, status: 'active', description: cmd}) })
    .then(r => r.json()).then(() => { fetchState(); input.value = ''; });
}

function createSquad() {
  const name = prompt('Nome do squad:');
  if (!name) return;
  fetch('/api/squads', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({name, agents: [], status: 'idle'}) })
    .then(r => r.json()).then(() => fetchState());
}

function formatUptime(s) {
  if (!s) return '--';
  const h = Math.floor(s/3600), m = Math.floor((s%3600)/60);
  return h > 0 ? h+'h '+m+'m' : m+'m';
}

function setMode(mode) { fetch('/api/command', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({command: 'set_mode:' + mode}) }); }
function connectAPEX() { const url = document.getElementById('apex-url').value; fetch('/api/command', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({command: 'connect_apex:' + url}) }); }
function saveHA() { const url = document.getElementById('ha-url').value; const token = document.getElementById('ha-token').value; fetch('/api/command', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({command: 'save_ha:' + url + ':' + token}) }); }
function toggleTask(id) { fetch('/api/command', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({command: 'toggle_task:' + id}) }).then(() => fetchState()); }

// Init
connect();
fetchState();
setInterval(fetchState, 10000);
</script>
</body>
</html>`
