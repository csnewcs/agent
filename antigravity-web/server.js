const http = require('http');
const https = require('https');
const url = require('url');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const os = require('os');
const net = require('net');
const zlib = require('zlib');
const { exec } = require('child_process');

// Load .env files
function loadEnv() {
  const envPaths = [
    path.join(__dirname, '.env'),
    path.join(__dirname, '..', 'bot', '.env'),
    path.join(__dirname, '..', '.env')
  ];
  for (const p of envPaths) {
    if (fs.existsSync(p)) {
      const content = fs.readFileSync(p, 'utf-8');
      for (const line of content.split('\n')) {
        const trimmed = line.trim();
        if (!trimmed || trimmed.startsWith('#')) continue;
        const idx = trimmed.indexOf('=');
        if (idx !== -1) {
          const k = trimmed.substring(0, idx).trim();
          let v = trimmed.substring(idx + 1).trim();
          if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) {
            v = v.substring(1, v.length - 1);
          }
          if (!process.env[k]) {
            process.env[k] = v;
          }
        }
      }
    }
  }
}
loadEnv();

const PORT = parseInt(process.env.WEB_PORT || process.env.PORT || '8095', 10);
const PROXY_URL = process.env.PROXY_URL || 'http://127.0.0.1:8090';
const CODEX_PROXY_URL = process.env.CODEX_PROXY_URL || 'http://127.0.0.1:8091';
const REDIS_HOST = process.env.REDIS_HOST || '127.0.0.1';
const REDIS_PORT = parseInt(process.env.REDIS_PORT || '6379', 10);
const DISCORD_CLIENT_ID = process.env.DISCORD_CLIENT_ID || '1523906805657370755';
const DISCORD_BOT_TOKEN = process.env.TOKEN || process.env.DISCORD_BOT_TOKEN;
const SESSION_SECRET = process.env.SESSION_SECRET || 'antigravity_secret_session_key_2026';
const DEFAULT_ALLOWED_USERS = ['453554012353069090', '670981324798165012'];
const ALLOWED_USERS = (process.env.ALLOWED_USERS ? process.env.ALLOWED_USERS.split(',').map(u => u.trim()).filter(Boolean) : DEFAULT_ALLOWED_USERS);

function isUserAllowedWeb(userId) {
  if (!userId) return false;
  return ALLOWED_USERS.includes(String(userId));
}

function sanitizeProjectId(projId) {
  if (!projId || typeof projId !== 'string') return 'default';
  const clean = projId.replace(/[^a-zA-Z0-9_-]/g, '').trim();
  return clean || 'default';
}

function normalizeProvider(provider) {
  return provider === 'codex' ? 'codex' : 'antigravity';
}

function proxyUrlFor(provider) {
  return normalizeProvider(provider) === 'codex' ? CODEX_PROXY_URL : PROXY_URL;
}

function sessionRevision(session) {
  return crypto
    .createHash('sha1')
    .update(JSON.stringify(session || {}))
    .digest('hex')
    .slice(0, 16);
}

function toSessionSummary(session) {
  const history = Array.isArray(session?.history) ? session.history : [];
  const tools = Array.isArray(session?.tools) ? session.tools : [];
  return {
    sessionId: session?.sessionId || '',
    projectId: session?.projectId || 'ai-agent',
    title: session?.title || '',
    prompt: session?.prompt || '',
    status: session?.status || (session?.isRunning ? 'running' : 'completed'),
    isRunning: Boolean(session?.isRunning),
    model: session?.model || '',
    effort: session?.effort || '',
    threadId: session?.threadId || '',
    turnId: session?.turnId || '',
    startedAt: session?.startedAt || null,
    updatedAt: session?.updatedAt || null,
    historyCount: history.length,
    toolCount: tools.length,
    pendingApproval: session?.pendingApproval || null,
    revision: sessionRevision(session)
  };
}

// Discord REST API Helper
function sendDiscordRequest(method, endpoint, body) {
  return new Promise((resolve, reject) => {
    if (!DISCORD_BOT_TOKEN) return resolve({ status: 500, error: 'No Discord Token' });
    const data = body ? JSON.stringify(body) : null;
    const req = https.request({
      hostname: 'discord.com',
      path: `/api/v10${endpoint}`,
      method: method,
      headers: {
        'Authorization': `Bot ${DISCORD_BOT_TOKEN}`,
        'Content-Type': 'application/json',
        ...(data ? { 'Content-Length': Buffer.byteLength(data) } : {})
      }
    }, (res) => {
      let resBody = '';
      res.on('data', chunk => resBody += chunk);
      res.on('end', () => {
        try {
          const parsed = JSON.parse(resBody);
          resolve({ status: res.statusCode, data: parsed });
        } catch (e) {
          resolve({ status: res.statusCode, raw: resBody });
        }
      });
    });
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}

// Send Discord DM notification when task finishes
async function sendDiscordTaskCompletionDM({
  user,
  projectId,
  sessionId,
  turnId,
  model,
  prompt,
  finalResponse,
  status,
  errorMessage,
  toolsUsed = [],
  usage = null,
  durationMs = 0,
  provider = 'antigravity'
}) {
  if (!user || !user.id || !DISCORD_BOT_TOKEN) return;

  try {
    const dmRes = await sendDiscordRequest('POST', '/users/@me/channels', { recipient_id: user.id });
    if (dmRes.status !== 200 || !dmRes.data?.id) {
      console.warn('Failed to create/get DM channel for user:', user.id, dmRes);
      return;
    }
    const channelId = dmRes.data.id;

    const providerName = normalizeProvider(provider) === 'codex' ? 'Codex' : 'Antigravity';
    let title = `✨ ${providerName} 작업 완료`;
    let desc = '웹 콘솔([antigravity.csnewcs.dev](https://antigravity.csnewcs.dev))에서 요청하신 작업이 완료되었습니다.';

    if (status === 'error') {
      title = `❌ ${providerName} 작업 오류`;
      desc = '웹 콘솔에서 요청하신 작업 처리 중 오류가 발생했습니다.';
    } else if (status === 'cancelled') {
      title = `⏹️ ${providerName} 작업 취소됨`;
      desc = '웹 콘솔에서 요청하신 작업이 사용자에 의해 중단되었습니다.';
    }

    const fields = [];

    // Prompt field
    let cleanPrompt = (prompt || '').trim();
    if (cleanPrompt.length > 280) {
      cleanPrompt = cleanPrompt.substring(0, 277) + '...';
    }
    if (cleanPrompt) {
      fields.push({
        name: '📌 요청 프롬프트',
        value: cleanPrompt.startsWith('```') ? cleanPrompt : `\`\`\`\n${cleanPrompt}\n\`\`\``,
        inline: false
      });
    }

    // Meta fields
    fields.push({
      name: '📁 프로젝트 / 세션',
      value: `\`${projectId || 'ai-agent'}\` / \`${sessionId || 'default'}\``,
      inline: true
    });

    if (model) {
      fields.push({
        name: '🤖 추론 모델',
        value: `\`${model}\``,
        inline: true
      });
    }

    const durationSec = (Math.max(durationMs, 0) / 1000).toFixed(1);
    fields.push({
      name: '⏱️ 소요 시간',
      value: `${durationSec}초`,
      inline: true
    });

    // Tools used
    if (toolsUsed && toolsUsed.length > 0) {
      const toolCounts = {};
      for (const t of toolsUsed) {
        toolCounts[t] = (toolCounts[t] || 0) + 1;
      }
      const toolStr = Object.entries(toolCounts)
        .map(([name, count]) => `\`${name}\`${count > 1 ? ` (${count}회)` : ''}`)
        .join(', ');
      fields.push({
        name: '🛠️ 실행된 도구',
        value: toolStr.length > 200 ? toolStr.substring(0, 197) + '...' : toolStr,
        inline: true
      });
    }

    // Token Usage
    if (usage && (usage.total_tokens || usage.input_tokens)) {
      const inTok = (usage.input_tokens || 0).toLocaleString();
      const outTok = (usage.output_tokens || 0).toLocaleString();
      const totTok = (usage.total_tokens || 0).toLocaleString();
      fields.push({
        name: '📊 토큰 사용량',
        value: `입력: ${inTok} | 출력: ${outTok} (합계: ${totTok})`,
        inline: false
      });
    }

    // Result summary
    if (status === 'error' && errorMessage) {
      let errStr = errorMessage.trim();
      if (errStr.length > 400) errStr = errStr.substring(0, 397) + '...';
      fields.push({
        name: '⚠️ 오류 내용',
        value: `\`\`\`\n${errStr}\n\`\`\``,
        inline: false
      });
    } else if (finalResponse) {
      let respStr = finalResponse.trim();
      respStr = respStr.replace(/<thinking>[\s\S]*?<\/thinking>/gi, '').trim();
      if (respStr.length > 500) {
        respStr = respStr.substring(0, 497) + '...';
      }
      if (respStr) {
        fields.push({
          name: '💬 작업 결과 요약',
          value: respStr,
          inline: false
        });
      }
    }

    const sessionUrl = `https://antigravity.csnewcs.dev/?session=${encodeURIComponent(sessionId || '')}&project=${encodeURIComponent(projectId || '')}`;

    const detailText = fields.map(field => `**${field.name}**\n${field.value}`).join('\n\n');
    const components = [
      { type: 10, content: `### ${title}\n${desc}` },
      { type: 14, divider: true, spacing: 1 },
      { type: 10, content: detailText || '작업 상세 정보가 없습니다.' },
      { type: 14, divider: true, spacing: 1 },
      { type: 10, content: `-# ${providerName} Web Console • ${user.username || 'User'} | ${new Date().toLocaleString('ko-KR', { timeZone: 'Asia/Seoul' })}` },
      {
        type: 1,
        components: [
          {
            type: 2,
            style: 5,
            label: '🌐 웹 콘솔에서 세션 확인하기',
            url: sessionUrl
          }
        ]
      }
    ];

    const sendRes = await sendDiscordRequest('POST', `/channels/${channelId}/messages`, {
      flags: 32768,
      embeds: [],
      components
    });

    if (sendRes.status !== 200) {
      console.warn('Failed to send DM message to user:', user.id, sendRes);
    } else {
      console.log(`[NOTIFY] Sent task completion DM to user ${user.id} (${user.username}) for session ${sessionId}`);
    }
  } catch (err) {
    console.error('Error in sendDiscordTaskCompletionDM:', err);
  }
}

function forwardChatToProxyAndNotify({ user, projectId, sessionId, prompt, turnId, model, effort, files, provider }) {
  const selectedProvider = normalizeProvider(provider);
  const selectedProxyUrl = proxyUrlFor(selectedProvider);
  const proxyRequest = {
    sessionId,
    projectId,
    prompt,
    turnId,
    files
  };
  if (selectedProvider !== 'codex') proxyRequest.model = model || 'gemini-3.8-flash-high';
  else if (model !== undefined) proxyRequest.model = model;
  if (effort !== undefined) proxyRequest.effort = effort;
  const payload = JSON.stringify(proxyRequest);

  const parsed = new URL(`${selectedProxyUrl}/api/chat`);
  const startTime = Date.now();
  let buffer = '';
  const toolsUsed = [];
  const chatChunks = [];
  let finalResponse = '';
  let status = 'running';
  let errorMessage = '';
  let usage = null;

  const proxyReq = http.request({
    hostname: parsed.hostname,
    port: parsed.port,
    path: '/api/chat',
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Content-Length': Buffer.byteLength(payload)
    }
  }, (proxyRes) => {
    proxyRes.on('data', (chunk) => {
      buffer += chunk.toString('utf-8');
      const lines = buffer.split('\n');
      buffer = lines.pop();

      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        try {
          const obj = JSON.parse(trimmed);
          const ctype = obj.type;
          const coutput = obj.output || '';
          const ctool = obj.tool || '';

          if (ctype === 'tool' && ctool) {
            toolsUsed.push(ctool);
          } else if (ctype === 'chat' && coutput) {
            chatChunks.push(coutput);
          } else if (ctype === 'result' && coutput) {
            finalResponse = coutput;
            status = 'completed';
          } else if (ctype === 'completed') {
            status = 'completed';
            if (coutput) finalResponse = coutput;
            if (!finalResponse && chatChunks.length > 0) {
              finalResponse = chatChunks.join('');
            }
          } else if (ctype === 'fatal_error' || ctype === 'error') {
            errorMessage = coutput;
            status = 'error';
          } else if (ctype === 'cancelled') {
            status = 'cancelled';
          } else if (ctype === 'usage' && coutput) {
            try {
              usage = typeof coutput === 'string' ? JSON.parse(coutput) : coutput;
            } catch (e) {}
          }
        } catch (e) {}
      }
    });

    proxyRes.on('end', () => {
      if (buffer.trim()) {
        try {
          const obj = JSON.parse(buffer.trim());
          if (obj.type === 'result' && obj.output) finalResponse = obj.output;
          if (obj.type === 'completed') {
            status = 'completed';
            if (obj.output) finalResponse = obj.output;
          }
        } catch (e) {}
      }

      if (status === 'running') {
        if (finalResponse || chatChunks.length > 0) {
          status = 'completed';
          if (!finalResponse) finalResponse = chatChunks.join('');
        } else if (errorMessage) {
          status = 'error';
        } else {
          status = 'completed';
        }
      }

      const durationMs = Date.now() - startTime;
      sendDiscordTaskCompletionDM({
        user,
        projectId,
        sessionId,
        turnId,
        model,
        prompt,
        finalResponse,
        status,
        errorMessage,
        toolsUsed,
        usage,
        durationMs,
        provider: selectedProvider
      }).catch(err => console.error('Failed to send Discord task DM:', err));
    });
  });

  proxyReq.on('error', (err) => {
    console.error('Proxy chat error:', err);
    sendDiscordTaskCompletionDM({
      user,
      projectId,
      sessionId,
      turnId,
      model,
      prompt,
      finalResponse: '',
      status: 'error',
      errorMessage: `프록시 통신 오류: ${err.message}`,
      toolsUsed: [],
      usage: null,
      durationMs: Date.now() - startTime,
      provider: selectedProvider
    }).catch(e => {});
  });

  proxyReq.write(payload);
  proxyReq.end();
}

// In-memory active SSE clients
const sseClients = new Set();

// Simple Raw Redis Client (0 dependencies)
function executeRedisCommand(commandArgs) {
  return new Promise((resolve, reject) => {
    const client = net.createConnection({ host: REDIS_HOST, port: REDIS_PORT }, () => {
      let resp = `*${commandArgs.length}\r\n`;
      for (const arg of commandArgs) {
        const str = String(arg);
        const buf = Buffer.from(str, 'utf-8');
        resp += `$${buf.length}\r\n${str}\r\n`;
      }
      client.write(resp);
    });

    let buffer = Buffer.alloc(0);
    client.on('data', chunk => {
      buffer = Buffer.concat([buffer, chunk]);
      const resStr = buffer.toString('utf-8');
      if (resStr.endsWith('\r\n')) {
        client.end();
        const firstChar = resStr[0];
        if (firstChar === '+') {
          resolve(resStr.substring(1, resStr.length - 2));
        } else if (firstChar === '$') {
          const firstLineEnd = resStr.indexOf('\r\n');
          const len = parseInt(resStr.substring(1, firstLineEnd), 10);
          if (len === -1) {
            resolve(null);
          } else {
            resolve(resStr.substring(firstLineEnd + 2, firstLineEnd + 2 + len));
          }
        } else if (firstChar === ':') {
          resolve(parseInt(resStr.substring(1, resStr.length - 2), 10));
        } else if (firstChar === '-') {
          reject(new Error(resStr.substring(1, resStr.length - 2)));
        } else {
          resolve(resStr);
        }
      }
    });

    client.on('error', err => {
      client.destroy();
      reject(err);
    });

    setTimeout(() => {
      client.destroy();
      resolve(null);
    }, 3000);
  });
}

// Discover workspaces
function getProjectList() {
  const workspaceBase = '/mnt/antigravity_workspaces';
  const projects = [];
  try {
    if (fs.existsSync(workspaceBase)) {
      const entries = fs.readdirSync(workspaceBase, { withFileTypes: true });
      for (const entry of entries) {
        if (entry.isDirectory() || entry.isSymbolicLink()) {
          projects.push({
            id: entry.name,
            name: entry.name,
            path: path.join(workspaceBase, entry.name)
          });
        }
      }
    }
  } catch (e) {
    console.error('Failed to list workspace dirs:', e);
  }
  if (!projects.some(p => p.id === 'default')) {
    projects.unshift({ id: 'default', name: 'default', path: path.join(workspaceBase, 'default') });
  }
  return projects;
}

// Helper: Sign session token
function signSession(data) {
  const payload = Buffer.from(JSON.stringify(data)).toString('base64url');
  const hmac = crypto.createHmac('sha256', SESSION_SECRET).update(payload).digest('base64url');
  return `${payload}.${hmac}`;
}

// Helper: Verify session token
function verifySession(token) {
  if (!token || !token.includes('.')) return null;
  const [payload, hmac] = token.split('.');
  if (!payload || !hmac) return null;
  const expected = crypto.createHmac('sha256', SESSION_SECRET).update(payload).digest('base64url');

  const hmacBuf = Buffer.from(hmac);
  const expectedBuf = Buffer.from(expected);
  if (hmacBuf.length !== expectedBuf.length || !crypto.timingSafeEqual(hmacBuf, expectedBuf)) {
    return null;
  }

  try {
    const jsonStr = Buffer.from(payload, 'base64url').toString('utf-8');
    const data = JSON.parse(jsonStr);
    if (data.exp && Date.now() > data.exp) return null;
    if (!isUserAllowedWeb(data.id)) return null;
    return data;
  } catch (e) {
    return null;
  }
}

// Helper: Parse cookies
function parseCookies(req) {
  const list = {};
  const rc = req.headers.cookie;
  if (!rc) return list;
  rc.split(';').forEach(cookie => {
    const parts = cookie.split('=');
    if (parts.length >= 2) {
      list[parts.shift().trim()] = decodeURIComponent(parts.join('=').trim());
    }
  });
  return list;
}

// Helper: Get user from request
function getUserFromReq(req) {
  const cookies = parseCookies(req);
  const token = cookies.ag_session;
  if (!token) return null;
  return verifySession(token);
}

// Helper: Fetch JSON via http/https
function fetchJson(targetUrl, options = {}) {
  return new Promise((resolve, reject) => {
    const parsed = new URL(targetUrl);
    const lib = parsed.protocol === 'https:' ? https : http;
    const bodyPayload = options.body ? (typeof options.body === 'string' ? options.body : JSON.stringify(options.body)) : null;
    const headers = { ...(options.headers || {}) };
    if (bodyPayload) {
      headers['Content-Length'] = Buffer.byteLength(bodyPayload);
    }
    const timeoutMs = Number.isFinite(options.timeoutMs) ? options.timeoutMs : 10000;
    const reqOptions = { ...options, headers };
    delete reqOptions.body;
    delete reqOptions.timeoutMs;

    const req = lib.request(targetUrl, reqOptions, (res) => {
      let body = '';
      res.on('data', chunk => body += chunk);
      res.on('end', () => {
        try {
          const data = JSON.parse(body);
          resolve({ status: res.statusCode, data });
        } catch (err) {
          resolve({ status: res.statusCode, raw: body });
        }
      });
    });
    req.on('error', reject);
    req.setTimeout(timeoutMs, () => {
      req.destroy(new Error(`Upstream request timed out after ${timeoutMs}ms`));
    });
    if (bodyPayload) {
      req.write(bodyPayload);
    }
    req.end();
  });
}

function getClientIp(req) {
  return (
    req.headers['cf-connecting-ip'] ||
    req.headers['x-real-ip'] ||
    (req.headers['x-forwarded-for'] ? req.headers['x-forwarded-for'].split(',')[0].trim() : null) ||
    req.socket?.remoteAddress ||
    '127.0.0.1'
  );
}

// In-memory Fail2ban fallback storage
const memoryBans = new Map();
const memoryFails = new Map();

async function isIpBanned(ip) {
  const memUnban = memoryBans.get(ip);
  if (memUnban && Date.now() < memUnban) {
    return Math.ceil((memUnban - Date.now()) / 1000);
  }
  try {
    const ttlStr = await executeRedisCommand(['TTL', `ag:ban:ip:${ip}`]);
    const ttl = parseInt(ttlStr, 10);
    if (ttl > 0) return ttl;
  } catch (e) {}
  return 0;
}

async function recordFailedAttempt(ip) {
  const MAX_FAILS = 5;
  const BAN_SECONDS = 900;
  const FAIL_WINDOW_SECONDS = 300;

  try {
    const failKey = `ag:fail:ip:${ip}`;
    const countRes = await executeRedisCommand(['INCR', failKey]);
    const count = parseInt(countRes, 10) || 1;
    if (count === 1) {
      await executeRedisCommand(['EXPIRE', failKey, FAIL_WINDOW_SECONDS]);
    }

    if (count >= MAX_FAILS) {
      await executeRedisCommand(['SET', `ag:ban:ip:${ip}`, 'banned', 'EX', BAN_SECONDS]);
      await executeRedisCommand(['DEL', failKey]);
      memoryBans.set(ip, Date.now() + BAN_SECONDS * 1000);
      console.warn(`[SECURITY] 🚨 IP ${ip} has been BANNED for ${BAN_SECONDS}s due to ${count} failed OTP attempts!`);
      return { banned: true, remainingTime: BAN_SECONDS };
    }
    return { banned: false, attemptsLeft: MAX_FAILS - count };
  } catch (e) {
    let record = memoryFails.get(ip);
    if (!record || Date.now() > record.expireAt) {
      record = { count: 0, expireAt: Date.now() + FAIL_WINDOW_SECONDS * 1000 };
    }
    record.count++;
    memoryFails.set(ip, record);

    if (record.count >= MAX_FAILS) {
      memoryBans.set(ip, Date.now() + BAN_SECONDS * 1000);
      memoryFails.delete(ip);
      return { banned: true, remainingTime: BAN_SECONDS };
    }
    return { banned: false, attemptsLeft: MAX_FAILS - record.count };
  }
}

async function clearFailedAttempts(ip) {
  try {
    await executeRedisCommand(['DEL', `ag:fail:ip:${ip}`]);
  } catch (e) {}
  memoryFails.delete(ip);
}

// Helper: Calculate CPU & Memory usage
function getSystemStats() {
  const cpus = os.cpus();
  const totalMem = os.totalmem();
  const freeMem = os.freemem();
  const usedMem = totalMem - freeMem;
  const loadAvg = os.loadavg();
  const uptime = os.uptime();

  return {
    cpuCount: cpus.length,
    cpuModel: cpus[0]?.model || 'Generic CPU',
    loadAvg: [loadAvg[0].toFixed(2), loadAvg[1].toFixed(2), loadAvg[2].toFixed(2)],
    memory: {
      totalMB: Math.round(totalMem / (1024 * 1024)),
      usedMB: Math.round(usedMem / (1024 * 1024)),
      freeMB: Math.round(freeMem / (1024 * 1024)),
      totalGB: (totalMem / 1024 / 1024 / 1024).toFixed(2),
      usedGB: (usedMem / 1024 / 1024 / 1024).toFixed(2),
      percent: Math.round((usedMem / totalMem) * 100)
    },
    uptimeSeconds: Math.round(uptime)
  };
}

// Background Ping updater (Runs asynchronously without blocking event loop)
let cachedPing = { avg: '4.2 ms', min: '4.0 ms', max: '4.5 ms' };
function updatePingAsync() {
  exec('ping -c 2 -W 1 1.1.1.1', { timeout: 2500 }, (err, out) => {
    if (!err && out) {
      for (const line of out.split('\n')) {
        if (line.includes('min/avg/max')) {
          const parts = line.split(' = ')[1].replace(' ms', '').split('/');
          cachedPing = { avg: parts[1] + ' ms', min: parts[0] + ' ms', max: parts[2] + ' ms' };
        }
      }
    }
  });
}
updatePingAsync();
setInterval(updatePingAsync, 10000);

// Helper: Get detailed Host Metrics (Non-blocking & cached)
function getDetailedSystemStats() {
  const base = getSystemStats();
  let cpuTemp = '48.0°C';
  try {
    const p = '/sys/class/thermal/thermal_zone0/temp';
    if (fs.existsSync(p)) {
      const v = parseInt(fs.readFileSync(p, 'utf-8').trim(), 10);
      cpuTemp = (v / 1000).toFixed(1) + '°C';
    }
  } catch (e) {}

  return {
    ...base,
    cpuTemp,
    ping: cachedPing
  };
}

// Helper: Auto-detect Antigravity Language Server PID, CSRF Token and Ports (Async & Cached)
let cachedLanguageServerInfo = null;
let lastLanguageServerInfoCheck = 0;

function getLanguageServerInfo() {
  const now = Date.now();
  if (cachedLanguageServerInfo && (now - lastLanguageServerInfoCheck < 15000)) {
    return Promise.resolve(cachedLanguageServerInfo);
  }

  return new Promise((resolve) => {
    exec('ps -ef', { timeout: 3000 }, (err, psOut) => {
      if (err || !psOut) return resolve(cachedLanguageServerInfo);

      let csrfToken = null;
      let pid = null;
      for (const line of psOut.split('\n')) {
        if (line.includes('language_server') && line.includes('--csrf_token')) {
          const mCsrf = line.match(/--csrf_token\s+([a-zA-Z0-9-]+)/);
          if (mCsrf) csrfToken = mCsrf[1];
          const parts = line.trim().split(/\s+/);
          if (parts.length >= 2) pid = parts[1];
        }
      }
      if (!csrfToken || !pid) {
        return resolve(cachedLanguageServerInfo);
      }

      exec('ss -tlpn', { timeout: 3000 }, (err2, ssOut) => {
        if (err2 || !ssOut) return resolve(cachedLanguageServerInfo);
        const ports = [];
        for (const line of ssOut.split('\n')) {
          if (line.includes(`pid=${pid}`)) {
            const mPort = line.match(/:(\d+)\s+/);
            if (mPort) ports.push(parseInt(mPort[1], 10));
          }
        }
        if (ports.length > 0) {
          cachedLanguageServerInfo = { pid, csrfToken, ports };
          lastLanguageServerInfoCheck = now;
        }
        resolve(cachedLanguageServerInfo);
      });
    });
  });
}

// Helper: Query Antigravity Language Server for real-time model quotas & credits
let cachedQuotaResult = null;
let lastQuotaFetchTime = 0;

async function fetchLanguageServerQuota() {
  const now = Date.now();
  if (cachedQuotaResult && (now - lastQuotaFetchTime < 3000)) {
    return cachedQuotaResult;
  }

  const lsInfo = await getLanguageServerInfo();
  if (!lsInfo || !lsInfo.ports || !lsInfo.ports.length) {
    return cachedQuotaResult || { success: false, error: 'Antigravity Language Server not detected' };
  }

  for (const port of lsInfo.ports) {
    try {
      const data = await new Promise((resolve, reject) => {
        const req = https.request({
          hostname: '127.0.0.1',
          port: port,
          path: '/exa.language_server_pb.LanguageServerService/GetUserStatus',
          method: 'POST',
          rejectUnauthorized: false,
          headers: {
            'Content-Type': 'application/json',
            'X-Codeium-Csrf-Token': lsInfo.csrfToken
          },
          timeout: 2500
        }, (res) => {
          let body = '';
          res.on('data', d => body += d);
          res.on('end', () => {
            try { resolve(JSON.parse(body)); } catch (e) { reject(e); }
          });
        });
        req.on('error', reject);
        req.on('timeout', () => { req.destroy(); reject(new Error('timeout')); });
        req.write('{}');
        req.end();
      });

      if (data && data.userStatus) {
        const u = data.userStatus;
        const planStatus = u.planStatus || {};
        const planInfo = planStatus.planInfo || {};
        const rawModels = u.cascadeModelConfigData?.clientModelConfigs || [];

        const models = rawModels.map(m => {
          const q = m.quotaInfo || {};
          const remFrac = typeof q.remainingFraction === 'number' ? q.remainingFraction
            : (q.resetTime ? 0.0 : 1.0);
          return {
            modelId: m.modelId || '',
            label: m.label || m.modelId || 'Unknown Model',
            remainingFraction: remFrac,
            remainingPercent: parseFloat((remFrac * 100).toFixed(1)),
            resetTime: q.resetTime || null,
            tagTitle: m.tagTitle || '',
            tagDescription: m.tagDescription || '',
            supportsImages: Boolean(m.supportsImages),
            isRecommended: Boolean(m.isRecommended)
          };
        });

        let earliestReset = null;
        for (const m of models) {
          if (m.resetTime) {
            const dt = new Date(m.resetTime).getTime();
            if (!isNaN(dt) && (!earliestReset || dt < earliestReset)) {
              earliestReset = dt;
            }
          }
        }

        // Group models into Pooled Quota Groups: Gemini Group vs Claude/GPT Group
        const geminiModels = models.filter(m => m.modelId.startsWith('gemini') || m.label.toLowerCase().includes('gemini'));
        const premiumModels = models.filter(m => !m.modelId.startsWith('gemini') && !m.label.toLowerCase().includes('gemini'));

        const quotaGroups = [];

        if (geminiModels.length > 0) {
          const rep = geminiModels[0];
          quotaGroups.push({
            id: 'gemini_pool',
            family: 'Google Gemini',
            iconSvg: `<svg width="24" height="24" viewBox="0 0 24 24" fill="none"><path d="M12 24C12 17.3726 6.62742 12 0 12C6.62742 12 12 6.62742 12 0C12 6.62742 17.3726 12 24 12C17.3726 12 12 17.3726 12 24Z" fill="url(#geminiGrad)"/><defs><linearGradient id="geminiGrad" x1="0" y1="0" x2="24" y2="24" gradientUnits="userSpaceOnUse"><stop stop-color="#1BA1E3"/><stop offset="0.5" stop-color="#5470FF"/><stop offset="1" stop-color="#9C5DFF"/></linearGradient></defs></svg>`,
            title: 'Google Gemini 모델 쿼터 풀',
            subtitle: 'Gemini 3.8 Flash, 3.7 Flash, 3.6 Flash, 3.5 Flash, 3.1 Pro 통합 쿼터',
            remainingFraction: rep.remainingFraction,
            remainingPercent: rep.remainingPercent,
            resetTime: rep.resetTime,
            modelList: geminiModels.map(m => m.label)
          });
        }

        if (premiumModels.length > 0) {
          const rep = premiumModels[0];
          quotaGroups.push({
            id: 'claude_gpt_pool',
            family: 'Claude & GPT-OSS',
            iconSvg: `<svg width="24" height="24" viewBox="0 0 24 24" fill="#D97706"><path d="M13.784 2.5h-3.568v6.784L5.42 4.488 2.9 7.008l4.796 4.796L2.9 16.6l2.52 2.52 4.796-4.796V21.5h3.568v-7.176l4.796 4.796 2.52-2.52-4.796-4.796 4.796-4.796-2.52-2.52-4.796 4.796V2.5z"/></svg>`,
            title: 'Claude & GPT 프리미엄 모델 쿼터 풀',
            subtitle: 'Claude Sonnet 4.6, Claude Opus 4.6, GPT-OSS 120B 통합 쿼터',
            remainingFraction: rep.remainingFraction,
            remainingPercent: rep.remainingPercent,
            resetTime: rep.resetTime,
            modelList: premiumModels.map(m => m.label)
          });
        }

        const availablePromptCredits = planStatus.availablePromptCredits ?? 500;
        const monthlyPromptCredits = planInfo.monthlyPromptCredits ?? 50000;
        const availableFlowCredits = planStatus.availableFlowCredits ?? 100;
        const monthlyFlowCredits = planInfo.monthlyFlowCredits ?? 150000;
        const maxInputTokens = parseInt(planInfo.maxNumChatInputTokens || '16384', 10);

        const result = {
          success: true,
          email: u.email || '',
          name: u.name || '',
          userTier: u.userTier || { name: 'Google AI Pro', id: 'g1-pro-tier', description: 'Google AI Pro' },
          planInfo: {
            planName: planInfo.planName || 'Pro',
            teamsTier: planInfo.teamsTier || 'TEAMS_TIER_PRO',
            maxNumChatInputTokens: maxInputTokens,
            maxNumPremiumChatMessages: planInfo.maxNumPremiumChatMessages || '-1',
            monthlyPromptCredits,
            monthlyFlowCredits
          },
          promptCredits: {
            available: availablePromptCredits,
            monthly: monthlyPromptCredits,
            percent: parseFloat(((availablePromptCredits / (monthlyPromptCredits || 50000)) * 100).toFixed(1))
          },
          flowCredits: {
            available: availableFlowCredits,
            monthly: monthlyFlowCredits,
            percent: parseFloat(((availableFlowCredits / (monthlyFlowCredits || 150000)) * 100).toFixed(1))
          },
          availableCredits: u.userTier?.availableCredits || [],
          earliestResetTime: earliestReset ? new Date(earliestReset).toISOString() : null,
          quotaGroups,
          models
        };

        cachedQuotaResult = result;
        lastQuotaFetchTime = now;
        return result;
      }
    } catch (err) {
      // try next port
    }
  }
  return cachedQuotaResult || { success: false, error: 'Could not communicate with language server' };
}

// HTTP Server
const server = http.createServer(async (req, res) => {
  const parsedUrl = url.parse(req.url, true);
  const pathname = parsedUrl.pathname;
  const provider = normalizeProvider(parsedUrl.query.provider);
  const selectedProxyUrl = proxyUrlFor(provider);
  const user = getUserFromReq(req);
  const clientIp = getClientIp(req);

  // Helper response functions
  const encodeResponseBody = (value) => {
    const raw = Buffer.isBuffer(value) ? value : Buffer.from(value, 'utf8');
    const acceptsGzip = /\bgzip\b/i.test(req.headers['accept-encoding'] || '');
    if (!acceptsGzip || raw.length < 1024) return { body: raw, encoding: null };
    return { body: zlib.gzipSync(raw, { level: 6 }), encoding: 'gzip' };
  };

  const sendJson = (data, code = 200) => {
    const encoded = encodeResponseBody(JSON.stringify(data));
    res.writeHead(code, {
      'Content-Type': 'application/json; charset=utf-8',
      'Content-Length': encoded.body.length,
      ...(encoded.encoding ? { 'Content-Encoding': encoded.encoding, 'Vary': 'Accept-Encoding' } : {}),
      'Cache-Control': 'no-store, no-cache, must-revalidate, proxy-revalidate',
      'Pragma': 'no-cache',
      'Expires': '0'
    });
    res.end(encoded.body);
  };

  const sendHtml = (htmlStr, code = 200) => {
    const encoded = encodeResponseBody(htmlStr);
    res.writeHead(code, {
      'Content-Type': 'text/html; charset=utf-8',
      'Content-Length': encoded.body.length,
      ...(encoded.encoding ? { 'Content-Encoding': encoded.encoding, 'Vary': 'Accept-Encoding' } : {}),
      'Cache-Control': 'no-store, no-cache, must-revalidate, proxy-revalidate',
      'Pragma': 'no-cache',
      'Expires': '0'
    });
    res.end(encoded.body);
  };

  const redirect = (targetUrl) => {
    res.writeHead(302, { 'Location': targetUrl });
    res.end();
  };

  // 1. Static Public Files with strict Cache-Control and Path Traversal Protection
  if (pathname.startsWith('/public/')) {
    const publicDir = path.resolve(__dirname, 'public');
    const relPath = pathname.replace(/^\/public\//, '').split('?')[0];
    const safeRelPath = path.normalize(relPath).replace(/^(\.\.[\/\\])+/, '');
    const filePath = path.resolve(publicDir, safeRelPath);

    if (filePath.startsWith(publicDir + path.sep) || filePath === publicDir) {
      if (fs.existsSync(filePath) && fs.statSync(filePath).isFile()) {
        const encoded = encodeResponseBody(fs.readFileSync(filePath));
        const ext = path.extname(filePath).toLowerCase();
        const mimeTypes = {
          '.css': 'text/css',
          '.js': 'application/javascript',
          '.svg': 'image/svg+xml',
          '.png': 'image/png',
          '.jpg': 'image/jpeg',
          '.json': 'application/json'
        };
        res.writeHead(200, {
          'Content-Type': mimeTypes[ext] || 'text/plain',
          'Content-Length': encoded.body.length,
          ...(encoded.encoding ? { 'Content-Encoding': encoded.encoding, 'Vary': 'Accept-Encoding' } : {}),
          'Cache-Control': 'no-store, no-cache, must-revalidate, max-age=0',
          'Pragma': 'no-cache',
          'Expires': '0'
        });
        res.end(encoded.body);
        return;
      }
    }
    res.writeHead(404, { 'Content-Type': 'text/plain' });
    res.end('Not Found');
    return;
  }

  // 2. Authentication Routes
  if (pathname === '/login') {
    if (user) return redirect('/');
    return sendHtml(renderLoginPage());
  }

  // OTP Code Login endpoint
  if (pathname === '/auth/code-login' && req.method === 'POST') {
    const banRemainingSeconds = await isIpBanned(clientIp);
    if (banRemainingSeconds > 0) {
      const mins = Math.ceil(banRemainingSeconds / 60);
      return sendJson({
        success: false,
        error: `🚫 과도한 인증 실패로 인해 접속이 차단되었습니다. (${mins}분 후 다시 시도해 주세요)`
      }, 429);
    }

    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        let code = '';
        try {
          const parsed = JSON.parse(body);
          code = (parsed.code || '').trim();
        } catch (e) {
          const params = new URLSearchParams(body);
          code = (params.get('code') || '').trim();
        }

        if (!code) {
          return sendJson({ success: false, error: '인증 코드를 입력해 주세요.' }, 400);
        }

        const redisKey = `ag:web_auth:${code}`;
        const rawTokenData = await executeRedisCommand(['GET', redisKey]);

        if (!rawTokenData) {
          await new Promise(r => setTimeout(r, 600));
          const failResult = await recordFailedAttempt(clientIp);
          if (failResult.banned) {
            return sendJson({
              success: false,
              error: `🚫 5회 연속 인증 실패로 인해 IP(${clientIp})가 15분간 차단되었습니다.`
            }, 429);
          } else {
            return sendJson({
              success: false,
              error: `유효하지 않거나 만료된 인증 코드입니다. (남은 시도 횟수: ${failResult.attemptsLeft}회)`
            }, 401);
          }
        }

        const tokenObj = JSON.parse(rawTokenData);
        if (!isUserAllowedWeb(tokenObj.userId)) {
          await recordFailedAttempt(clientIp);
          return sendJson({
            success: false,
            error: '🚫 웹 콘솔 접속 권한이 없는 계정입니다.'
          }, 403);
        }

        await clearFailedAttempts(clientIp);
        await executeRedisCommand(['DEL', redisKey]);

        const sessionPayload = {
          id: tokenObj.userId,
          username: tokenObj.username,
          globalName: tokenObj.globalName || tokenObj.username,
          avatar: tokenObj.avatarUrl || 'https://cdn.discordapp.com/embed/avatars/0.png',
          exp: Date.now() + 7 * 24 * 60 * 60 * 1000
        };

        const sessionToken = signSession(sessionPayload);
        res.writeHead(200, {
          'Content-Type': 'application/json; charset=utf-8',
          'Set-Cookie': `ag_session=${sessionToken}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${7 * 86400}`
        });
        res.end(JSON.stringify({ success: true, redirect: '/', user: sessionPayload }));
      } catch (err) {
        console.error('Code login error:', err);
        return sendJson({ success: false, error: '서버 인증 처리 중 오류가 발생했습니다.' }, 500);
      }
    });
    return;
  }

  // Logout
  if (pathname === '/auth/logout') {
    res.writeHead(302, {
      'Location': '/',
      'Set-Cookie': `ag_session=; Path=/; HttpOnly; Max-Age=0`
    });
    res.end();
    return;
  }

  // 3. API Routes (Public read-only, Protected for mutations)
  if (pathname === '/api/me') {
    return sendJson({ authenticated: !!user, user: user || null });
  }

  if (pathname === '/api/system') {
    return sendJson(getDetailedSystemStats());
  }

  // Quota & Resources API: GET /api/resources or /api/quota (Public)
  if (pathname === '/api/resources' || pathname === '/api/quota') {
    try {
      if (provider === 'codex') {
        const [quotaResp, sysStats] = await Promise.all([
          fetchJson(`${selectedProxyUrl}/api/quota`),
          Promise.resolve(getDetailedSystemStats())
        ]);
        return sendJson({
          ...(quotaResp.data || { success: false, provider: 'codex' }),
          timestamp: Date.now(),
          system: sysStats
        }, quotaResp.status >= 400 ? quotaResp.status : 200);
      }
      const [agQuota, sysStats] = await Promise.all([
        fetchLanguageServerQuota(),
        Promise.resolve(getDetailedSystemStats())
      ]);

      // If user is not authenticated, redact personal email and name
      let safeAgQuota = agQuota;
      if (!user && agQuota && agQuota.success) {
        safeAgQuota = {
          ...agQuota,
          email: agQuota.email ? agQuota.email.replace(/(.{2})(.*)(@.*)/, '$1***$3') : '',
          name: agQuota.name ? agQuota.name.charAt(0) + '***' : ''
        };
      }

      return sendJson({
        success: true,
        timestamp: Date.now(),
        antigravity: safeAgQuota,
        system: sysStats
      });
    } catch (err) {
      console.error('Resource API error:', err);
      return sendJson({ success: false, error: err.message }, 500);
    }
  }

  // Discover Projects: GET /api/projects (Public)
  if (pathname === '/api/projects') {
    const projects = getProjectList();
    return sendJson({ projects });
  }

  // Proxy: GET /api/sessions (Public)
  if (pathname === '/api/sessions') {
    try {
      const resp = await fetchJson(`${selectedProxyUrl}/api/sessions`, { timeoutMs: 5000 });
      const data = resp.data || { sessions: [] };
      if (parsedUrl.query.summary === '1') {
        return sendJson({ sessions: (data.sessions || []).map(toSessionSummary) });
      }
      return sendJson(data);
    } catch (err) {
      return sendJson({ error: `Proxy unavailable: ${err.message}`, sessions: [] }, 502);
    }
  }

  // Proxy: GET /api/session (Public)
  if (pathname === '/api/session') {
    const sid = parsedUrl.query.sessionId;
    try {
      const resp = await fetchJson(`${selectedProxyUrl}/api/session?sessionId=${encodeURIComponent(sid || '')}`, { timeoutMs: 5000 });
      return sendJson(resp.data || {}, resp.status);
    } catch (err) {
      return sendJson({ error: `Proxy unavailable: ${err.message}` }, 502);
    }
  }

  // File & Artifact Serving API: GET /api/file (Public/Safe with strict isolation)
  if (pathname === '/api/file') {
    let rawPath = parsedUrl.query.path || '';
    if (!rawPath) {
      return sendJson({ error: 'Missing path parameter' }, 400);
    }
    if (rawPath.startsWith('file://')) {
      rawPath = rawPath.replace(/^file:\/\//, '');
    }

    let filePath = path.normalize(rawPath);
    if (!path.isAbsolute(filePath)) {
      filePath = path.join('/mnt/antigravity_workspaces/ai-agent', filePath);
    }

    // 1. Security Base Whitelist check
    const allowedBases = [
      '/mnt/antigravity_workspaces',
      '/home/fedora/git',
      '/home/fedora/.gemini/antigravity-cli/brain',
      '/tmp'
    ];
    const isAllowed = allowedBases.some(base => filePath.startsWith(base));
    if (!isAllowed) {
      return sendJson({ error: 'Access denied: Path outside allowed workspace boundaries' }, 403);
    }

    if (!fs.existsSync(filePath)) {
      return sendJson({ error: 'File not found' }, 404);
    }

    try {
      // 2. Canonical Realpath resolution (Symlink traversal protection)
      const realPath = fs.realpathSync(filePath);
      const isRealAllowed = allowedBases.some(base => realPath.startsWith(base));
      if (!isRealAllowed) {
        return sendJson({ error: 'Access denied: Target resolves outside allowed workspace boundaries' }, 403);
      }

      // 3. Blacklist sensitive credential & configuration files
      const filename = path.basename(realPath).toLowerCase();
      const sensitivePattern = /(\.env|\.pem|\.key|\.crt|\.pfx|\.p12|id_rsa|id_ed25519|authorized_keys|credentials|\.git|\.ssh|aistudio_key|openai_key)/i;
      if (sensitivePattern.test(filename) || realPath.includes('/.git/') || realPath.includes('/.ssh/')) {
        return sendJson({ error: 'Access denied: Sensitive system/credential files cannot be accessed' }, 403);
      }

      // 4. Strict Public vs Private Isolation:
      // Only files inside dedicated shared/upload directories or brain images can be viewed publicly without admin login.
      const isPublicShared = realPath.includes('/shared/') ||
                             realPath.includes('/uploads/') ||
                             realPath.startsWith('/tmp/antigravity_shared') ||
                             (realPath.startsWith('/home/fedora/.gemini/antigravity-cli/brain') && /\.(jpg|jpeg|png|webp|gif|svg)$/i.test(realPath));

      const isAdmin = user && isUserAllowedWeb(user.id);
      if (!isPublicShared && !isAdmin) {
        return sendJson({ error: 'Unauthorized: 관리자 로그인이 필요한 내부 프로젝트 파일입니다. 관리자 계정으로 로그인해 주세요.' }, 401);
      }

      const stat = fs.statSync(realPath);
      if (stat.isDirectory()) {
        return sendJson({ error: 'Cannot view directory as file' }, 400);
      }

      const ext = path.extname(realPath).toLowerCase();
      const mimeTypes = {
        '.jpg': 'image/jpeg',
        '.jpeg': 'image/jpeg',
        '.png': 'image/png',
        '.webp': 'image/webp',
        '.gif': 'image/gif',
        '.svg': 'image/svg+xml',
        '.ico': 'image/x-icon',
        '.mp4': 'video/mp4',
        '.webm': 'video/webm',
        '.mp3': 'audio/mpeg',
        '.wav': 'audio/wav',
        '.pdf': 'application/pdf',
        '.json': 'application/json; charset=utf-8',
        '.md': 'text/markdown; charset=utf-8',
        '.txt': 'text/plain; charset=utf-8',
        '.log': 'text/plain; charset=utf-8',
        '.html': 'text/plain; charset=utf-8', // Treat HTML as plain text to prevent stored XSS
        '.css': 'text/css; charset=utf-8',
        '.js': 'text/plain; charset=utf-8',
        '.ts': 'text/plain; charset=utf-8',
        '.go': 'text/plain; charset=utf-8',
        '.py': 'text/plain; charset=utf-8',
        '.csv': 'text/csv; charset=utf-8',
        '.xml': 'application/xml; charset=utf-8'
      };

      const contentType = mimeTypes[ext] || 'application/octet-stream';
      const isDownload = parsedUrl.query.download === '1' || parsedUrl.query.download === 'true';

      const headers = {
        'Content-Type': contentType,
        'Content-Length': stat.size,
        'Cache-Control': 'public, max-age=3600',
        'Last-Modified': stat.mtime.toUTCString(),
        'X-Content-Type-Options': 'nosniff',
        'X-Frame-Options': 'SAMEORIGIN',
        'Content-Security-Policy': "default-src 'none'; style-src 'unsafe-inline'; sandbox"
      };

      if (isDownload || contentType === 'application/octet-stream') {
        headers['Content-Disposition'] = `attachment; filename="${encodeURIComponent(filename)}"`;
      } else {
        headers['Content-Disposition'] = `inline; filename="${encodeURIComponent(filename)}"`;
      }

      res.writeHead(200, headers);
      const stream = fs.createReadStream(realPath);
      stream.pipe(res);
      return;
    } catch (err) {
      console.error('File serving error:', err);
      return sendJson({ error: `File read error: ${err.message}` }, 500);
    }
  }

  // File Deletion API: POST /api/file/delete (Admin Only, Restricted to Shared / Uploads / Brain Artifacts)
  if (pathname === '/api/file/delete' && req.method === 'POST') {
    if (!user || !isUserAllowedWeb(user.id)) {
      return sendJson({ success: false, error: '관리자 권한이 필요합니다. 관리자 계정으로 로그인해 주세요.' }, 403);
    }

    try {
      const body = await parseJsonBody(req);
      let rawPath = (body && body.path) || '';
      if (!rawPath) {
        return sendJson({ success: false, error: '삭제할 파일 경로(path)가 전달되지 않았습니다.' }, 400);
      }
      if (rawPath.startsWith('file://')) {
        rawPath = rawPath.replace(/^file:\/\//, '');
      }

      let filePath = path.normalize(rawPath);
      if (!path.isAbsolute(filePath)) {
        filePath = path.join('/mnt/antigravity_workspaces/shared', filePath);
      }

      if (!fs.existsSync(filePath)) {
        return sendJson({ success: false, error: '삭제하려는 파일이 존재하지 않습니다.' }, 404);
      }

      const realPath = fs.realpathSync(filePath);

      // Strict Deletion Whitelist: ONLY allow deleting inside shared, uploads, tmp, or brain artifact images!
      const isDeletable = realPath.includes('/shared/') ||
                          realPath.includes('/uploads/') ||
                          realPath.startsWith('/tmp/antigravity_shared') ||
                          (realPath.startsWith('/home/fedora/.gemini/antigravity-cli/brain') && /\.(jpg|jpeg|png|webp|gif|svg)$/i.test(realPath));

      if (!isDeletable || realPath.includes('/bot/') || realPath.includes('/antigravity-web/') || realPath.endsWith('.go') || realPath.endsWith('.js') || realPath.endsWith('.ts')) {
        return sendJson({
          success: false,
          error: '보안 정책 위반: 소스코드 및 프로젝트 기본 파일은 삭제할 수 없습니다. 공유 디렉토리(/shared/, /uploads/)의 생성물만 삭제 가능합니다.'
        }, 403);
      }

      const stat = fs.statSync(realPath);
      if (stat.isDirectory()) {
        return sendJson({ success: false, error: '폴더 전체 삭제는 불가능합니다. 개별 파일만 삭제해 주세요.' }, 400);
      }

      // Delete the file
      fs.unlinkSync(realPath);
      console.log(`[FILE DELETE] Admin ${user.username} (${user.id}) deleted shared file: ${realPath}`);

      return sendJson({
        success: true,
        message: '파일이 성공적으로 삭제되었습니다.',
        deletedPath: realPath,
        filename: path.basename(realPath)
      });
    } catch (err) {
      console.error('File delete error:', err);
      return sendJson({ success: false, error: `파일 삭제 실패: ${err.message}` }, 500);
    }
  }

  // Artifacts Discovery API: GET /api/artifacts (Public)
  if (pathname === '/api/artifacts') {
    const sid = parsedUrl.query.sessionId || '';
    const projId = parsedUrl.query.projectId || 'ai-agent';
    const artifacts = [];

    // 1. Look up conversation UUID
    let convId = '';
    const convMapPath = '/tmp/antigravity_conv_map.json';
    if (fs.existsSync(convMapPath)) {
      try {
        const convMap = JSON.parse(fs.readFileSync(convMapPath, 'utf-8'));
        convId = convMap[sid] || '';
      } catch (e) {}
    }

    // 2. Scan conversation brain directory
    if (convId) {
      const brainDir = path.join('/home/fedora/.gemini/antigravity-cli/brain', convId);
      if (fs.existsSync(brainDir)) {
        try {
          const files = fs.readdirSync(brainDir);
          for (const f of files) {
            if (f.startsWith('.')) continue;
            const fullPath = path.join(brainDir, f);
            const stat = fs.statSync(fullPath);
            if (stat.isFile()) {
              const ext = path.extname(f).toLowerCase();
              const isImage = ['.jpg', '.jpeg', '.png', '.webp', '.gif', '.svg'].includes(ext);
              artifacts.push({
                name: f,
                path: fullPath,
                url: `/api/file?path=${encodeURIComponent(fullPath)}`,
                size: stat.size,
                mtime: stat.mtimeMs,
                ext: ext,
                isImage: isImage
              });
            }
          }

          // Also scan .tempmediaStorage
          const mediaDir = path.join(brainDir, '.tempmediaStorage');
          if (fs.existsSync(mediaDir)) {
            const mediaFiles = fs.readdirSync(mediaDir);
            for (const mf of mediaFiles) {
              const fullPath = path.join(mediaDir, mf);
              const stat = fs.statSync(fullPath);
              if (stat.isFile()) {
                const ext = path.extname(mf).toLowerCase();
                const isImage = ['.jpg', '.jpeg', '.png', '.webp', '.gif', '.svg'].includes(ext);
                artifacts.push({
                  name: mf,
                  path: fullPath,
                  url: `/api/file?path=${encodeURIComponent(fullPath)}`,
                  size: stat.size,
                  mtime: stat.mtimeMs,
                  ext: ext,
                  isImage: isImage
                });
              }
            }
          }
        } catch (e) {
          console.warn('Artifact scan error:', e);
        }
      }
    }

    // Sort by modification time desc
    artifacts.sort((a, b) => b.mtime - a.mtime);
    return sendJson({ success: true, count: artifacts.length, artifacts: artifacts });
  }

  // Proxy: POST /api/chat (Protected - Mutation)
  if (pathname === '/api/chat' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다. 우측 상단에서 로그인해 주세요.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        let reqData = {};
        try { reqData = JSON.parse(body); } catch(e) {}
        const rawProjectId = reqData.projectId || 'ai-agent';
        const projectId = sanitizeProjectId(rawProjectId);
        const sessionId = reqData.sessionId ? String(reqData.sessionId).replace(/[^a-zA-Z0-9_:-]/g, '') : `${provider === 'codex' ? 'codex' : 'ag'}_proj_${projectId}`;
        const model = Object.hasOwn(reqData, 'model') ? reqData.model : (provider === 'codex' ? undefined : 'gemini-3.8-flash-high');
        const effort = Object.hasOwn(reqData, 'effort') ? reqData.effort : undefined;
        const prompt = reqData.prompt || '';
        const turnId = reqData.turnId ? String(reqData.turnId).replace(/[^a-zA-Z0-9_:-]/g, '') : `turn_${Date.now()}`;
        const files = reqData.files || [];

        const savedFiles = [];
        if (Array.isArray(files) && files.length > 0) {
          const uploadsDir = path.join('/mnt/antigravity_workspaces', projectId, 'uploads');
          if (!fs.existsSync(uploadsDir)) {
            fs.mkdirSync(uploadsDir, { recursive: true });
          }
          for (const f of files) {
            const rawName = f.name || f.filename || `file_${Date.now()}`;
            const cleanName = path.basename(rawName).replace(/[^a-zA-Z0-9._-]/g, '_');
            const filePath = path.join(uploadsDir, cleanName);
            if (f.content_base64) {
              try {
                const buffer = Buffer.from(f.content_base64, 'base64');
                fs.writeFileSync(filePath, buffer);
                savedFiles.push({
                  name: cleanName,
                  path: filePath,
                  size: buffer.length
                });
              } catch (ex) {
                console.error('Failed to save uploaded file:', ex);
              }
            } else if (f.path && typeof f.path === 'string') {
              const safePath = path.resolve(f.path);
              if (safePath.startsWith('/mnt/antigravity_workspaces/')) {
                savedFiles.push({ name: cleanName, path: safePath });
              }
            }
          }
        }

        forwardChatToProxyAndNotify({
          user,
          projectId,
          sessionId,
          model,
          effort,
          prompt,
          turnId,
          files: savedFiles.length > 0 ? savedFiles : files,
          provider
        });

        return sendJson({ success: true, sessionId, projectId, model, prompt, turnId, files: savedFiles });
      } catch (err) {
        return sendJson({ error: err.message }, 500);
      }
    });
    return;
  }

  // Action: POST /api/prompt (Protected - Alias for /api/chat)
  if (pathname === '/api/prompt' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다. 우측 상단에서 로그인해 주세요.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        let reqData = {};
        try { reqData = JSON.parse(body); } catch(e) {}
        const rawProjectId = reqData.projectId || 'ai-agent';
        const projectId = sanitizeProjectId(rawProjectId);
        const sessionId = reqData.sessionId ? String(reqData.sessionId).replace(/[^a-zA-Z0-9_:-]/g, '') : `${provider === 'codex' ? 'codex' : 'ag'}_proj_${projectId}`;
        const model = Object.hasOwn(reqData, 'model') ? reqData.model : (provider === 'codex' ? undefined : 'gemini-3.8-flash-high');
        const effort = Object.hasOwn(reqData, 'effort') ? reqData.effort : undefined;
        const prompt = reqData.prompt || '';
        const turnId = reqData.turnId ? String(reqData.turnId).replace(/[^a-zA-Z0-9_:-]/g, '') : `turn_${Date.now()}`;
        const files = reqData.files || [];

        const savedFiles = [];
        if (Array.isArray(files) && files.length > 0) {
          const uploadsDir = path.join('/mnt/antigravity_workspaces', projectId, 'uploads');
          if (!fs.existsSync(uploadsDir)) {
            fs.mkdirSync(uploadsDir, { recursive: true });
          }
          for (const f of files) {
            const rawName = f.name || f.filename || `file_${Date.now()}`;
            const cleanName = path.basename(rawName).replace(/[^a-zA-Z0-9._-]/g, '_');
            const filePath = path.join(uploadsDir, cleanName);
            if (f.content_base64) {
              try {
                const buffer = Buffer.from(f.content_base64, 'base64');
                fs.writeFileSync(filePath, buffer);
                savedFiles.push({
                  name: cleanName,
                  path: filePath,
                  size: buffer.length
                });
              } catch (ex) {
                console.error('Failed to save uploaded file:', ex);
              }
            } else if (f.path && typeof f.path === 'string') {
              const safePath = path.resolve(f.path);
              if (safePath.startsWith('/mnt/antigravity_workspaces/')) {
                savedFiles.push({ name: cleanName, path: safePath });
              }
            }
          }
        }

        forwardChatToProxyAndNotify({
          user,
          projectId,
          sessionId,
          model,
          effort,
          prompt,
          turnId,
          files: savedFiles.length > 0 ? savedFiles : files,
          provider
        });

        return sendJson({ success: true, sessionId, projectId, model, prompt, turnId, files: savedFiles });
      } catch (err) {
        return sendJson({ error: err.message }, 500);
      }
    });
    return;
  }

  // Proxy: POST /api/cancel (Protected - Mutation)
  if (pathname === '/api/cancel' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다. 우측 상단에서 로그인해 주세요.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        const resp = await fetchJson(`${selectedProxyUrl}/api/cancel`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body
        });
        return sendJson(resp.data || { status: 'cancelled' });
      } catch (err) {
        return sendJson({ error: err.message }, 502);
      }
    });
    return;
  }

  // Proxy: POST /api/approval (Protected - Mutation)
  if (pathname === '/api/approval' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다. 우측 상단에서 로그인해 주세요.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        const resp = await fetchJson(`${selectedProxyUrl}/api/approval`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body
        });
        broadcastSessionsUpdate();
        return sendJson(resp.data || { status: 'resolved' });
      } catch (err) {
        return sendJson({ error: err.message }, 502);
      }
    });
    return;
  }

  // Proxy: POST /api/compact (Protected - Mutation)
  if (pathname === '/api/compact' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다. 우측 상단에서 로그인해 주세요.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        const resp = await fetchJson(`${selectedProxyUrl}/api/compact`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body
        });
        return sendJson(resp.data || { status: 'compacted' });
      } catch (err) {
        return sendJson({ error: err.message }, 502);
      }
    });
    return;
  }

  // Proxy: POST /api/clear (Protected - Mutation)
  if (pathname === '/api/clear' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다. 우측 상단에서 로그인해 주세요.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        const resp = await fetchJson(`${selectedProxyUrl}/api/clear`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body
        });
        broadcastSessionsUpdate();
        return sendJson(resp.data || { status: 'cleared' });
      } catch (err) {
        return sendJson({ error: err.message }, 502);
      }
    });
    return;
  }

  // Proxy: POST /api/session/rename (Protected - Mutation)
  if (pathname === '/api/session/rename' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        const resp = await fetchJson(`${selectedProxyUrl}/api/session/rename`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body
        });
        broadcastSessionsUpdate();
        return sendJson(resp.data || { status: 'renamed' });
      } catch (err) {
        return sendJson({ error: err.message }, 502);
      }
    });
    return;
  }

  // Proxy: POST /api/session/create (Protected - Mutation)
  if (pathname === '/api/session/create' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        const resp = await fetchJson(`${selectedProxyUrl}/api/session/create`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body
        });
        broadcastSessionsUpdate();
        return sendJson(resp.data || { success: true });
      } catch (err) {
        return sendJson({ error: err.message }, 502);
      }
    });
    return;
  }

  // Proxy: POST /api/session/delete (Protected - Mutation)
  if (pathname === '/api/session/delete' && req.method === 'POST') {
    if (!user) {
      return sendJson({ success: false, error: '로그인이 필요한 기능입니다.' }, 401);
    }
    let body = '';
    req.on('data', chunk => body += chunk);
    req.on('end', async () => {
      try {
        let reqData = {};
        try { reqData = JSON.parse(body); } catch(e) {}
        const sid = reqData.sessionId || reqData.session_id || reqData.id || '';

        const resp = await fetchJson(`${selectedProxyUrl}/api/session/delete`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sessionId: sid })
        });

        // Clean up any Redis metadata keys as well
        if (sid) {
          try {
            const redisPrefix = provider === 'codex' ? 'codex' : 'ag';
            await executeRedisCommand(['DEL', `${redisPrefix}:meta:${sid}`]);
            await executeRedisCommand(['DEL', `${redisPrefix}:state:${sid}`]);
            await executeRedisCommand(['SREM', `${redisPrefix}:sessions`, sid]);
          } catch(re) {}
        }

        await broadcastSessionsUpdate();
        return sendJson(resp.data || { status: 'deleted', sessionId: sid });
      } catch (err) {
        return sendJson({ error: err.message }, 502);
      }
    });
    return;
  }

  // Real-time SSE Stream: GET /api/stream (Public)
  if (pathname === '/api/stream') {
    res.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      'Connection': 'keep-alive',
      'X-Accel-Buffering': 'no'
    });
    res.write(': heartbeat\n\n');

    const client = { res, provider, lastData: null, lastWriteAt: Date.now() };
    sseClients.add(client);

    req.on('close', () => {
      sseClients.delete(client);
    });
    return;
  }

  // 4. Root Dashboard & Sub-pages (Public View-Only access for all)
  if (pathname === '/' || pathname === '/resources' || pathname === '/quota' || pathname === '/console') {
    return sendHtml(renderDashboardPage(user));
  }

  // Fallback 404
  res.writeHead(404, { 'Content-Type': 'text/plain' });
  res.end('Not Found');
});

async function broadcastSessionsUpdate() {
  if (sseClients.size === 0 || broadcastSessionsUpdate.inFlight) return;
  broadcastSessionsUpdate.inFlight = true;
  try {
    for (const provider of ['antigravity', 'codex']) {
      const clients = [...sseClients].filter(client => client.provider === provider);
      if (clients.length === 0) continue;
      try {
        const resp = await fetchJson(`${proxyUrlFor(provider)}/api/sessions`, { timeoutMs: 5000 });
        const sessions = (resp.data?.sessions || []).map(toSessionSummary);
        const payload = JSON.stringify({ type: 'sessions_update', provider, sessions });
        const now = Date.now();
        for (const client of clients) {
          if (client.lastData !== payload) {
            client.lastData = payload;
            client.lastWriteAt = now;
            client.res.write(`data: ${payload}\n\n`);
          } else if (now - client.lastWriteAt >= 15000) {
            client.lastWriteAt = now;
            client.res.write(': heartbeat\n\n');
          }
        }
      } catch (err) {
        // Selected proxy is down; keep the SSE connection alive.
      }
    }
  } finally {
    broadcastSessionsUpdate.inFlight = false;
  }
}

// Periodic SSE worker. Only changed summaries are sent; comments keep idle streams alive.
setInterval(broadcastSessionsUpdate, 1000);

// HTML Renderers
function renderLoginPage() {
  return `<!DOCTYPE html>
<html lang="ko">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no, interactive-widget=resizes-content">
  <title>Antigravity Console — 로그인</title>
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@500;700&display=swap" rel="stylesheet">
  <style>
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
      background: #fafafa;
      color: #09090b;
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
      min-height: 100dvh;
      padding: 16px;
      -webkit-font-smoothing: antialiased;
      letter-spacing: -0.011em;
    }
    .login-card {
      background: #ffffff;
      border: 1px solid #e4e4e7;
      border-radius: 16px;
      padding: 36px 28px;
      width: 100%;
      max-width: 400px;
      text-align: center;
      box-shadow: 0 4px 20px -2px rgba(0, 0, 0, 0.05), 0 2px 6px -1px rgba(0, 0, 0, 0.02);
      animation: fadeIn 0.25s ease-out;
    }
    @keyframes fadeIn {
      from { opacity: 0; transform: scale(0.98); }
      to { opacity: 1; transform: scale(1); }
    }
    .logo-badge {
      width: 48px;
      height: 48px;
      background: #18181b;
      border-radius: 12px;
      display: flex;
      align-items: center;
      justify-content: center;
      margin: 0 auto 16px;
      box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
    }
    .logo-badge svg { width: 24px; height: 24px; fill: #ffffff; }
    h1 {
      font-size: 20px;
      font-weight: 600;
      margin-bottom: 6px;
      color: #09090b;
      letter-spacing: -0.02em;
    }
    p.subtitle {
      font-size: 13px;
      color: #71717a;
      margin-bottom: 24px;
      line-height: 1.4;
    }

    .form-group {
      text-align: left;
      margin-bottom: 18px;
    }
    .form-label {
      display: block;
      font-size: 12px;
      font-weight: 500;
      color: #09090b;
      margin-bottom: 8px;
    }
    .otp-input {
      width: 100%;
      height: 48px;
      background: #ffffff;
      border: 1px solid #e4e4e7;
      border-radius: 10px;
      padding: 0 14px;
      font-size: 22px;
      font-family: 'JetBrains Mono', monospace;
      font-weight: 600;
      color: #09090b;
      text-align: center;
      letter-spacing: 8px;
      outline: none;
      transition: all 0.15s ease;
      appearance: none;
      -webkit-appearance: none;
    }
    .otp-input:focus {
      border-color: #18181b;
      box-shadow: 0 0 0 2px rgba(24, 24, 27, 0.08);
    }

    .btn-submit {
      width: 100%;
      height: 44px;
      background: #18181b;
      color: #ffffff;
      border: none;
      border-radius: 10px;
      font-size: 14px;
      font-weight: 500;
      cursor: pointer;
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      transition: all 0.15s ease;
      box-shadow: 0 1px 2px rgba(0, 0, 0, 0.05);
    }
    .btn-submit:hover {
      background: #27272a;
    }
    .btn-submit:active {
      transform: translateY(0);
    }
    .btn-submit:disabled {
      opacity: 0.5;
      cursor: not-allowed;
      transform: none;
    }

    .guide-box {
      margin-top: 22px;
      background: #f4f4f5;
      border: 1px solid #e4e4e7;
      border-radius: 10px;
      padding: 14px;
      font-size: 12px;
      color: #71717a;
      text-align: left;
      line-height: 1.6;
    }
    .guide-box strong { color: #09090b; font-weight: 600; }
    .guide-box code {
      background: #ffffff;
      border: 1px solid #e4e4e7;
      padding: 2px 6px;
      border-radius: 4px;
      color: #09090b;
      font-family: 'JetBrains Mono', monospace;
      font-size: 11px;
    }

    .error-msg {
      margin-top: 14px;
      background: #fef2f2;
      border: 1px solid #fecaca;
      color: #b91c1c;
      padding: 10px 14px;
      border-radius: 8px;
      font-size: 12px;
      display: none;
      animation: shake 0.3s ease;
    }
    @keyframes shake {
      0%, 100% { transform: translateX(0); }
      25% { transform: translateX(-4px); }
      75% { transform: translateX(4px); }
    }
  </style>
</head>
<body>
  <div class="login-card">
    <div class="logo-badge">
      <svg viewBox="0 0 24 24"><path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/></svg>
    </div>
    <h1>Antigravity Console</h1>
    <p class="subtitle">디스코드 DM으로 발급된 인증 코드를 입력하세요</p>

    <form id="otpForm">
      <div class="form-group">
        <label class="form-label" for="otpCode">6자리 일회용 로그인 코드</label>
        <input type="text" id="otpCode" class="otp-input" placeholder="000000" maxlength="6" inputmode="numeric" autocomplete="one-time-code" required autofocus>
      </div>

      <button type="submit" id="btnSubmit" class="btn-submit">
        인증 및 대시보드 접속
      </button>

      <div id="errorBox" class="error-msg"></div>
    </form>

    <div class="guide-box">
      <strong>인증 코드 발급 방법</strong><br>
      1. 디스코드에서 <code>/web_login</code> 슬래시 커맨드를 실행합니다.<br>
      2. 봇이 <strong>개인 DM</strong>으로 전송한 6자리 숫자를 위 입력창에 입력해 주세요. (발급 후 5분간 유효)
    </div>
  </div>

  <script>
    const form = document.getElementById('otpForm');
    const input = document.getElementById('otpCode');
    const btn = document.getElementById('btnSubmit');
    const errorBox = document.getElementById('errorBox');

    input.addEventListener('input', (e) => {
      input.value = input.value.replace(/[^0-9]/g, '');
    });

    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const code = input.value.trim();
      if (!code) return;

      btn.disabled = true;
      btn.textContent = '인증 확인 중...';
      errorBox.style.display = 'none';

      try {
        const res = await fetch('/auth/code-login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ code })
        });
        const data = await res.json();
        if (data.success) {
          window.location.href = data.redirect || '/';
        } else {
          errorBox.textContent = data.error || '인증에 실패했습니다.';
          errorBox.style.display = 'block';
          btn.disabled = false;
          btn.textContent = '인증 및 대시보드 접속';
          input.select();
        }
      } catch (err) {
        errorBox.textContent = '서버 통신 오류가 발생했습니다.';
        errorBox.style.display = 'block';
        btn.disabled = false;
        btn.textContent = '인증 및 대시보드 접속';
      }
    });
  </script>
</body>
</html>`;
}

function renderDashboardPage(user) {
  const avatarUrl = user?.avatar || 'https://cdn.discordapp.com/embed/avatars/0.png';
  const displayName = user?.globalName || user?.username || 'User';
  const handleName = user?.username || '';
  const cacheBuster = Date.now();

  return `<!DOCTYPE html>
<html lang="ko">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no, interactive-widget=resizes-content">
  <title>Antigravity Web Console & Quota</title>
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500;600;700&display=swap" rel="stylesheet">
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.9.0/styles/github-dark-dimmed.min.css">
  <link rel="stylesheet" href="/public/style.css?v=${cacheBuster}">
  <script src="https://cdnjs.cloudflare.com/ajax/libs/marked/12.0.1/marked.min.js"></script>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/dompurify/3.0.9/purify.min.js"></script>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.9.0/highlight.min.js"></script>
</head>
<body>
  <!-- Mobile Backdrop -->
  <div class="sidebar-backdrop" id="sidebarBackdrop"></div>

  <div class="app-root">
    <!-- Sidebar -->
    <aside class="sidebar" id="sidebar">
      <div class="sidebar-header">
        <div class="brand">
          <div class="brand-icon">
            <svg viewBox="0 0 24 24"><path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/></svg>
          </div>
          <span id="providerBrandText">Antigravity Web</span>
        </div>
        <span class="chip" id="proxyStatusChip"><span class="status-dot online"></span> Online</span>
      </div>

      <div class="provider-switch" aria-label="AI provider">
        <button type="button" class="provider-tab" data-provider="antigravity">Antigravity</button>
        <button type="button" class="provider-tab" data-provider="codex">Codex</button>
      </div>

      <!-- Project Selector Dropdown inside Sidebar -->
      <div class="project-selector-box">
        <div class="selector-label">
          <span>작업 프로젝트</span>
          <span id="projectCountBadge" style="color: #58a6ff;">-</span>
        </div>
        <select id="projectSelect" class="project-dropdown">
          <option value="">전체 프로젝트 (All)</option>
        </select>
      </div>

      <div class="sessions-toolbar">
        <input type="text" id="sessionSearch" class="search-input" placeholder="세션 검색 (Prompt / ID)...">
      </div>

      <div class="sessions-list" id="sessionsContainer">
        <div class="empty-state" style="padding: 40px 0;">세션을 불러오는 중...</div>
      </div>

      <div class="sidebar-footer">
        ${user ? `
          <div class="user-badge">
            <img class="user-avatar" src="${avatarUrl}" alt="avatar">
            <div class="user-info">
              <span class="user-name">${displayName}</span>
              <span class="user-tag">@${handleName}</span>
            </div>
          </div>
          <a href="/auth/logout" class="btn-logout" title="로그아웃">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"></path>
              <polyline points="16 17 21 12 16 7"></polyline>
              <line x1="21" y1="12" x2="9" y2="12"></line>
            </svg>
          </a>
        ` : `
          <div class="user-badge">
            <div class="guest-avatar">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"></path>
                <circle cx="12" cy="7" r="4"></circle>
              </svg>
            </div>
            <div class="user-info">
              <span class="user-name">게스트 모드</span>
              <span class="user-tag">읽기 전용 뷰어</span>
            </div>
          </div>
        `}
      </div>
    </aside>

    <!-- Main View -->
    <main class="main" id="mainContainer">
      <!-- Universal Top App Bar -->
      <div class="top-appbar">
        <div class="appbar-left">
          <button class="btn-hamburger" id="btnToggleSidebar" title="메뉴 / 세션 목록 열기">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <line x1="3" y1="12" x2="21" y2="12"></line>
              <line x1="3" y1="6" x2="21" y2="6"></line>
              <line x1="3" y1="18" x2="21" y2="18"></line>
            </svg>
          </button>
        </div>

        <!-- Tab switcher between Console and Quotas/Resources -->
        <div class="appbar-nav-switch">
          <button class="nav-tab-btn active" id="btnNavConsole" data-view="console">
            <span>콘솔</span>
          </button>
          <button class="nav-tab-btn" id="btnNavResources" data-view="resources">
            <span>자원 및 쿼터</span>
          </button>
        </div>

        <div class="appbar-right">
          ${user ? `
            <div class="user-top-chip" title="${displayName} (@${handleName})">
              <img src="${avatarUrl}" class="user-top-avatar" alt="avatar">
              <span class="user-top-name">${displayName}</span>
            </div>
          ` : `
            <button type="button" class="btn-login-top" id="btnOpenLoginModal">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4"></path>
                <polyline points="10 17 15 12 10 7"></polyline>
                <line x1="15" y1="12" x2="3" y2="12"></line>
              </svg>
              <span>로그인</span>
            </button>
          `}
        </div>
      </div>

      <!-- VIEW 1: Console / Sessions -->
      <div class="view-pane active" id="consoleView">
        <div class="empty-state" id="noSessionSelected">
          <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#5865F2" stroke-width="1.5">
            <rect x="2" y="3" width="20" height="14" rx="2" ry="2"></rect>
            <line x1="8" y1="21" x2="16" y2="21"></line>
            <line x1="12" y1="17" x2="12" y2="21"></line>
          </svg>
          <h3>선택된 세션이 없습니다</h3>
          <p style="font-size: 13px;">세션을 선택하거나 새로운 대화 세션을 시작하세요.</p>
          <button type="button" class="btn-primary-action" id="btnCreateFirstSession">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
              <line x1="12" y1="5" x2="12" y2="19"></line>
              <line x1="5" y1="12" x2="19" y2="12"></line>
            </svg>
            <span>새 세션 만들기</span>
          </button>
        </div>

        <!-- Active Workspace -->
        <div id="sessionWorkspace" style="display: none; flex-direction: column; height: 100%; overflow: hidden;">
          <div class="main-header">
            <div class="main-title-area">
              <div class="main-title" id="activePromptTitle" title="클릭하여 세션 이름 변경" style="cursor: pointer;">
                대화 불러오는 중...
              </div>
              <div class="main-chips">
                <span class="status-badge" id="activeStatusBadge">RUNNING</span>
                <span class="chip" id="activeProjectChip">default</span>
                <span class="chip" id="activeSessionChip">ID: -</span>
              </div>
            </div>
            ${user ? `
              <div class="main-actions">
                <button class="btn-action" id="btnRenameSession" title="세션 이름 변경">
                  이름 변경
                </button>
                <button class="btn-action" id="btnCompactSession" title="대화 맥락 압축 및 요약">
                  Compact
                </button>
                <button class="btn-action danger" id="btnCancelSession" title="실행 중단">
                  중단
                </button>
                <button class="btn-action" id="btnClearSession" title="세션 초기화">
                  삭제
                </button>
              </div>
            ` : ''}
          </div>

          <!-- Chat Timeline Flow -->
          <div class="chat-timeline" id="chatTimelineContainer">
            <!-- Dynamically populated with chat turns -->
          </div>
        </div>

        ${user ? `
          <!-- Attached Files Preview Bar -->
          <div class="attached-files-bar" id="attachedFilesBar" style="display: none;">
            <div class="attached-files-inner">
              <div class="attached-files-list" id="attachedFilesList"></div>
            </div>
          </div>

          <!-- Bottom Interactive Prompt Bar -->
          <div class="prompt-launcher-box">
            <div class="prompt-launcher-inner">
              <input type="file" id="webFileInput" multiple style="display: none;">
              <button type="button" id="btnAttachFile" class="btn-attach" title="파일 첨부">
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"></path>
                </svg>
              </button>
              <div class="prompt-input-wrapper">
                <input type="text" id="webPromptInput" class="prompt-input" placeholder="선택된 프로젝트로 새 질문 또는 작업 요청 전송...">
                <select id="webModelSelect" class="prompt-model-select" title="추론 모델 선택">
                  <option value="gemini-3.8-flash-high" selected>3.8 Flash High</option>
                  <option value="gemini-3.8-flash-medium">3.8 Flash Mid</option>
                  <option value="gemini-3.8-flash-low">3.8 Flash Low</option>
                  <option value="gemini-3.7-flash-high">3.7 Flash High</option>
                  <option value="gemini-3.7-flash-medium">3.7 Flash Mid</option>
                  <option value="gemini-3.7-flash-low">3.7 Flash Low</option>
                  <option value="gemini-3.1-pro-high">3.1 Pro High</option>
                  <option value="gemini-3.1-pro-low">3.1 Pro Low</option>
                  <option value="claude-sonnet-4-6">Sonnet 4.6</option>
                  <option value="claude-opus-4-6-thinking">Opus 4.6</option>
                  <option value="gpt-oss-120b-medium">GPT-OSS 120B</option>
                  <option value="gemini-3.6-flash-high">3.6 Flash High</option>
                  <option value="gemini-3.5-flash-high">3.5 Flash High</option>
                </select>
              </div>
              <button id="btnSendPrompt" class="btn-send">
                <span>전송</span>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <line x1="22" y1="2" x2="11" y2="13"></line>
                  <polygon points="22 2 15 22 11 13 2 9 22 2"></polygon>
                </svg>
              </button>
            </div>
          </div>
        ` : ''}
      </div>

      <!-- VIEW 2: Resources & Quotas -->
      <div class="view-pane" id="resourcesView">
        <div class="resources-container">
          <div class="resources-header-bar">
            <div class="resources-title-group">
              <h2 id="resourcesTitle">Antigravity AI 자원 및 크레딧 현황</h2>
              <p id="resourcesSubtitle">Google Antigravity 실시간 모델군 쿼터 풀, 크레딧 및 리셋 타이머</p>
            </div>
            <div class="resources-actions">
              <span class="chip" id="lastUpdatedChip">갱신 중...</span>
              <button class="btn-refresh" id="btnRefreshResources" title="자원 현황 다시 불러오기">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <path d="M23 4v6h-6"></path>
                  <path d="M1 20v-6h6"></path>
                  <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
                </svg>
                <span>새로고침</span>
              </button>
            </div>
          </div>

          <!-- Top Metric Cards Grid: Antigravity Plan & Credits & Fastest Reset -->
          <div class="metric-cards-grid">
            <!-- Card 1: Antigravity Plan Tier & Token Window -->
            <div class="metric-card" style="--card-accent: #5865F2;">
              <div class="metric-header">
                <span id="planNameLabel">Google Antigravity 플랜</span>
                <span class="status-badge completed" id="planTierBadge">PRO</span>
              </div>
              <div class="metric-value" id="planTierName">
                Google AI Pro
              </div>
              <div class="metric-sub" id="planLimitsDetail">
                최대 컨텍스트: <strong>16,384 토큰</strong> | 프리미엄: <strong>무제한</strong>
              </div>
            </div>

            <!-- Card 2: Fastest Reset Countdown -->
            <div class="metric-card reset-metric-card" style="--card-accent: #58a6ff;">
              <span class="reset-credit-badge" id="resetCreditBadge" role="img" hidden></span>
              <div class="metric-header">
                <span id="resetNameLabel">가장 빠른 모델 쿼터 리셋</span>
                <span class="countdown-pill" id="liveResetCountdown">--:--:--</span>
              </div>
              <div class="metric-value" id="earliestResetTimeKst" style="font-size: 18px;">
                --
              </div>
              <div class="metric-sub" id="resetRelativeSubtitle">
                리셋 시 모델별 쿼터가 100%로 재충전됩니다
              </div>
            </div>
          </div>

          <!-- AI Models Pooled Quota Groups (Gemini Group & Claude/GPT Group) -->
          <div class="quota-groups-container" id="quotaGroupsContainer">
            <div class="empty-state" style="padding: 30px 0;">
              모델 쿼터 풀 정보를 불러오는 중...
            </div>
          </div>
        </div>
      </div>
    </main>
  </div>

  <!-- In-Page Login Modal Dialog -->
  <div class="login-modal-backdrop" id="loginModalBackdrop" style="display: none;">
    <div class="login-modal-card">
      <div class="login-modal-header">
        <div class="brand">
          <div class="brand-icon">
            <svg viewBox="0 0 24 24"><path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/></svg>
          </div>
          <span style="font-size: 15px; font-weight: 600;">Antigravity 로그인</span>
        </div>
        <button type="button" class="btn-modal-close" id="btnCloseLoginModal" title="닫기">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <line x1="18" y1="6" x2="6" y2="18"></line>
            <line x1="6" y1="6" x2="18" y2="18"></line>
          </svg>
        </button>
      </div>

      <p class="subtitle" style="text-align: left; margin-bottom: 16px;">디스코드 <code>/web_login</code> 커맨드로 발급된 6자리 일회용 코드를 입력하세요.</p>

      <form id="modalOtpForm">
        <div class="form-group">
          <label class="form-label" for="modalOtpCode">6자리 일회용 로그인 코드</label>
          <input type="text" id="modalOtpCode" class="otp-input" placeholder="000000" maxlength="6" inputmode="numeric" autocomplete="one-time-code" required>
        </div>

        <button type="submit" id="btnModalSubmit" class="btn-submit">
          인증 및 로그인
        </button>

        <div id="modalErrorBox" class="error-msg"></div>
      </form>

      <div class="guide-box" style="margin-top: 16px;">
        <strong>인증 코드 발급 방법</strong><br>
        1. 디스코드에서 <code>/web_login</code> 슬래시 커맨드를 실행합니다.<br>
        2. 봇이 <strong>개인 DM</strong>으로 전송한 6자리 숫자를 입력해 주세요. (발급 후 5분간 유효)
      </div>
    </div>
  </div>

  <!-- Right-Click Context Menu for Sessions -->
  <div id="sessionContextMenu" class="context-menu" style="display: none;">
    <div class="context-menu-item" id="ctxRenameSession">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <path d="M12 20h9"></path>
        <path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"></path>
      </svg>
      <span>이름 설정 (Rename)</span>
    </div>
    <div class="context-menu-separator"></div>
    <div class="context-menu-item danger" id="ctxDeleteSession">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <polyline points="3 6 5 6 21 6"></polyline>
        <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path>
      </svg>
      <span>세션 삭제 (Delete)</span>
    </div>
  </div>

  <!-- Rename Session Modal Dialog -->
  <div class="login-modal-backdrop" id="renameModalBackdrop" style="display: none;">
    <div class="login-modal-card" style="max-width: 380px;">
      <div class="login-modal-header">
        <span style="font-size: 15px; font-weight: 600;">세션 이름 변경</span>
        <button type="button" class="btn-modal-close" id="btnCloseRenameModal" title="닫기">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <line x1="18" y1="6" x2="6" y2="18"></line>
            <line x1="6" y1="6" x2="18" y2="18"></line>
          </svg>
        </button>
      </div>
      <form id="renameSessionForm">
        <input type="hidden" id="renameSessionId">
        <div class="form-group">
          <label class="form-label" for="renameSessionInput">새로운 세션 이름 / 별칭</label>
          <input type="text" id="renameSessionInput" class="prompt-input" style="width: 100%; box-sizing: border-box;" placeholder="세션 이름을 입력하세요..." required>
        </div>
        <div style="display: flex; gap: 8px; justify-content: flex-end; margin-top: 14px;">
          <button type="button" class="btn-action" id="btnCancelRename">취소</button>
          <button type="submit" class="btn-submit" style="width: auto; height: 36px; padding: 0 16px;">저장</button>
        </div>
      </form>
    </div>
  </div>

  <!-- New Session Creation Modal Dialog -->
  <div class="login-modal-backdrop" id="newSessionModalBackdrop" style="display: none;">
    <div class="login-modal-card" style="max-width: 440px;">
      <div class="login-modal-header">
        <div class="brand">
          <div class="brand-icon">
            <svg viewBox="0 0 24 24"><path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/></svg>
          </div>
          <span style="font-size: 15px; font-weight: 600;">새 세션 만들기</span>
        </div>
        <button type="button" class="btn-modal-close" id="btnCloseNewSessionModal" title="닫기">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <line x1="18" y1="6" x2="6" y2="18"></line>
            <line x1="6" y1="6" x2="18" y2="18"></line>
          </svg>
        </button>
      </div>

      <p class="subtitle" style="text-align: left; margin-bottom: 16px;">작업할 프로젝트 폴더와 초기 프롬프트를 지정하여 새 세션을 시작합니다.</p>

      <form id="newSessionForm">
        <div class="form-group">
          <label class="form-label" for="newSessionProject">작업 프로젝트 폴더</label>
          <select id="newSessionProject" class="modal-select" required>
            <!-- Dynamically populated from projects -->
          </select>
        </div>

        <div class="form-group">
          <label class="form-label" for="newSessionModel">AI 모델 선택</label>
          <select id="newSessionModel" class="modal-select" required>
            <option value="gemini-3.8-flash-high" selected>Gemini 3.8 Flash (High)</option>
            <option value="gemini-3.8-flash-medium">Gemini 3.8 Flash (Mid)</option>
            <option value="gemini-3.8-flash-low">Gemini 3.8 Flash (Low)</option>
            <option value="gemini-3.7-flash-high">Gemini 3.7 Flash (High)</option>
            <option value="gemini-3.7-flash-medium">Gemini 3.7 Flash (Mid)</option>
            <option value="gemini-3.7-flash-low">Gemini 3.7 Flash (Low)</option>
            <option value="gemini-3.1-pro-high">Gemini 3.1 Pro (High)</option>
            <option value="gemini-3.1-pro-low">Gemini 3.1 Pro (Low)</option>
            <option value="claude-sonnet-4-6">Claude Sonnet 4.6 (Thinking)</option>
            <option value="claude-opus-4-6-thinking">Claude Opus 4.6 (Thinking)</option>
            <option value="gpt-oss-120b-medium">GPT-OSS 120B (Medium)</option>
            <option value="gemini-3.6-flash-high">Gemini 3.6 Flash (High)</option>
            <option value="gemini-3.5-flash-high">Gemini 3.5 Flash (High)</option>
          </select>
        </div>

        <div class="form-group">
          <label class="form-label" for="newSessionPrompt">프롬프트 (선택 사항)</label>
          <textarea id="newSessionPrompt" class="modal-textarea" rows="3" placeholder="수행할 작업이나 질문을 입력하세요... (비워두고 생성만 할 수도 있습니다)"></textarea>
        </div>

        <button type="submit" id="btnSubmitNewSession" class="btn-submit">
          <span>새 세션 생성 및 시작</span>
        </button>

        <div id="newSessionErrorBox" class="error-msg"></div>
      </form>
    </div>
  </div>

  <script src="/public/app.js?v=${cacheBuster}"></script>
</body>
</html>`;
}

// Start Server
server.listen(PORT, '0.0.0.0', () => {
  console.log(`[ANTIGRAVITY WEB] Server running at http://0.0.0.0:${PORT}`);
  console.log(`[ANTIGRAVITY WEB] Antigravity proxy: ${PROXY_URL}`);
  console.log(`[ANTIGRAVITY WEB] Codex proxy: ${CODEX_PROXY_URL}`);
  console.log(`[ANTIGRAVITY WEB] Redis connected to ${REDIS_HOST}:${REDIS_PORT}`);
});
