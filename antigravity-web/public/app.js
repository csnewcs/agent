// Shared Antigravity / Codex Web Console Client Script

const activeProvider = localStorage.getItem('ai_provider') === 'codex' ? 'codex' : 'antigravity';
const providerStorageKey = (suffix) => `${activeProvider}_${suffix}`;
const addProviderToUrl = (rawUrl) => {
  if (typeof rawUrl !== 'string' || !rawUrl.startsWith('/api/')) return rawUrl;
  const parsed = new URL(rawUrl, window.location.origin);
  parsed.searchParams.set('provider', activeProvider);
  return parsed.pathname + parsed.search + parsed.hash;
};
const nativeFetch = window.fetch.bind(window);
window.fetch = (input, init) => nativeFetch(addProviderToUrl(input), init);

let projects = [];
let sessions = [];
let selectedSessionId = null;
let selectedProject = '';
let currentView = 'console';
let quotaData = null;
let countdownInterval = null;
let currentUser = null;
const sessionDetails = new Map();
const sessionDetailRequests = new Map();
const sessionDetailTimers = new Map();
const sessionDetailLastFetch = new Map();
const visibleTurnLimits = new Map();
const SESSION_DETAIL_REFRESH_MS = 1250;
const CODEX_SHELL_COMMANDS = new Set([
  'ls', 'cat', 'grep', 'git', 'go', 'npm', 'node', 'python', 'python3',
  'docker', 'cd', 'mkdir', 'rm', 'cp', 'mv', 'touch', 'echo', 'sed',
  'awk', 'find', 'pkill', 'kill'
]);
const CODEX_QUOTA_ICON = '<img src="/public/chatgpt-logo.png" width="44" height="44" alt="ChatGPT 로고">';

function normalizeCodexToolName(raw) {
  const tool = String(raw || '').trim();
  if (!tool) return '';
  const lower = tool.toLowerCase();
  if (['shell', 'bash', 'sh', 'exec', 'execute', 'run_command', 'commandexecution'].includes(lower)) return 'shell';
  if (['apply_patch', 'filechange', 'file_change'].includes(lower)) return 'apply_patch';
  if (/\s|[|&;<>()$`\\]/.test(tool) || tool.startsWith('/') || tool.startsWith('./')) return 'shell';
  if (!/^[a-zA-Z0-9_-]+$/.test(tool) || CODEX_SHELL_COMMANDS.has(lower)) return 'shell';
  return tool;
}

function compressCodexTools(tools) {
  const groups = [];
  for (const raw of tools || []) {
    const name = normalizeCodexToolName(raw);
    if (!name) continue;
    const previous = groups[groups.length - 1];
    if (previous?.name === name) previous.count += 1;
    else groups.push({ name, count: 1 });
  }
  return groups;
}

// DOM Elements
const sidebar = document.getElementById('sidebar');
const backdrop = document.getElementById('sidebarBackdrop');
const btnToggleSidebar = document.getElementById('btnToggleSidebar');
const projectSelect = document.getElementById('projectSelect');
const btnNavConsole = document.getElementById('btnNavConsole');
const btnNavResources = document.getElementById('btnNavResources');
const consoleView = document.getElementById('consoleView');
const resourcesView = document.getElementById('resourcesView');
const btnRefreshResources = document.getElementById('btnRefreshResources');
const sessionsContainer = document.getElementById('sessionsContainer');

function configureProviderUI() {
  document.querySelectorAll('.provider-tab').forEach((button) => {
    const selected = button.dataset.provider === activeProvider;
    button.classList.toggle('active', selected);
    button.addEventListener('click', () => {
      if (button.dataset.provider === activeProvider) return;
      localStorage.setItem('ai_provider', button.dataset.provider);
      window.location.reload();
    });
  });
  const brand = document.getElementById('providerBrandText');
  if (brand) brand.textContent = activeProvider === 'codex' ? 'Codex Web' : 'Antigravity Web';
  document.title = activeProvider === 'codex' ? 'Codex Web Console' : 'Antigravity Web Console & Quota';

  const modelOptions = activeProvider === 'codex'
    ? `
      <option value="" selected>Codex 기본 모델</option>
      <option value="gpt-6-astra">GPT-6 Astra</option>
      <option value="gpt-6-sol">GPT-6 Sol</option>
      <option value="gpt-6-luna">GPT-6 Luna</option>
      <option value="gpt-5.6-sol">GPT-5.6 Sol</option>
      <option value="gpt-5.6-terra">GPT-5.6 Terra</option>
      <option value="gpt-5.6-luna">GPT-5.6 Luna</option>
      <option value="gpt-5.5">GPT-5.5</option>`
    : `
      <option value="gemini-3.8-flash-high" selected>Gemini 3.8 Flash (High)</option>
      <option value="gemini-3.8-flash-medium">Gemini 3.8 Flash (Medium)</option>
      <option value="gemini-3.8-flash-low">Gemini 3.8 Flash (Low)</option>
      <option value="gemini-3.7-flash-high">Gemini 3.7 Flash (High)</option>
      <option value="gemini-3.1-pro-high">Gemini 3.1 Pro (High)</option>
      <option value="claude-sonnet-4-6">Claude Sonnet 4.6</option>
      <option value="claude-opus-4-6-thinking">Claude Opus 4.6</option>`;
  for (const selectId of ['webModelSelect', 'newSessionModel']) {
    const select = document.getElementById(selectId);
    if (select) select.innerHTML = modelOptions;
  }
}

configureProviderUI();

function renderCustomMarkdown(mdText) {
  if (!mdText) return '';
  let converted = mdText;

  // 1. Convert Discord subtext (-# text) into subtle small caption text
  converted = converted.replace(/^-#\s+(.+)$/gm, '<small class="discord-subtext" style="display:block;font-size:11.5px;color:var(--text-muted);opacity:0.85;margin-top:6px;line-height:1.4;">$1</small>');

  // 2. Convert markdown image syntax with local paths or file:// to /api/file
  converted = converted.replace(/!\[(.*?)\]\(((?:file:\/\/)?\/(?:home\/fedora|\.gemini|mnt\/antigravity_workspaces|tmp)[^\)\s]+)\)/g, (match, alt, rawPath) => {
    const cleanPath = rawPath.replace(/^file:\/\//, '');
    const encoded = encodeURIComponent(cleanPath);
    const filename = cleanPath.split('/').pop();
    const iconImg = `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>`;
    const iconDl = `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>`;
    const iconTrash = `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>`;
    return `<div class="media-preview-card" data-file-path="${escapeHtml(cleanPath)}"><div class="media-preview-header"><span class="media-title">${iconImg} ${escapeHtml(alt || filename)}</span><div class="media-header-btns"><a href="/api/file?path=${encoded}&download=1" target="_blank" class="media-dl-btn" title="다운로드">${iconDl}</a><button class="media-dl-btn danger" onclick="deleteFileByPath('${escapeHtml(cleanPath)}')" title="파일 삭제">${iconTrash}</button></div></div><img src="/api/file?path=${encoded}" alt="${escapeHtml(alt || filename)}" class="media-preview-img" onclick="openMediaModal('/api/file?path=${encoded}', '${escapeHtml(alt || filename)}', '${escapeHtml(cleanPath)}')" loading="lazy" /></div>`;
  });

  // 3. Convert markdown links with file:// or local paths into direct download badges or source code badges
  converted = converted.replace(/\[([^\]]+)\]\(((?:file:\/\/)?\/(?:home\/fedora|\.gemini|mnt\/antigravity_workspaces|tmp)[^\)\s]+)\)/g, (match, label, rawPath) => {
    const cleanPath = rawPath.replace(/^file:\/\//, '');
    const encoded = encodeURIComponent(cleanPath);
    const filename = cleanPath.split('/').pop();
    const isImg = /\.(jpg|jpeg|png|webp|gif|svg)$/i.test(cleanPath);
    const isShared = cleanPath.includes('/shared/') || cleanPath.includes('/uploads/') || cleanPath.includes('/brain/');

    const iconImg = `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>`;
    const iconDl = `<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>`;
    const iconCode = `<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="16 18 22 12 16 6"></polyline><polyline points="8 6 2 12 8 18"></polyline></svg>`;
    const iconClose = `<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>`;

    if (isImg) {
      return `<a href="/api/file?path=${encoded}" target="_blank" class="artifact-file-badge image-badge" onclick="event.preventDefault(); openMediaModal('/api/file?path=${encoded}', '${escapeHtml(label || filename)}', '${escapeHtml(cleanPath)}')">${iconImg} ${escapeHtml(label || filename)}</a>`;
    }

    if (isShared) {
      return `<span class="artifact-badge-group"><a href="/api/file?path=${encoded}&download=1" target="_blank" class="artifact-file-badge" title="${escapeHtml(cleanPath)}">${iconDl} ${escapeHtml(label || filename)}</a><button class="artifact-delete-icon" onclick="deleteFileByPath('${escapeHtml(cleanPath)}')" title="공유 파일 삭제">${iconClose}</button></span>`;
    }

    // Normal repository source code file link: render as protected source badge (no delete button)
    return `<a href="/api/file?path=${encoded}&download=1" target="_blank" class="source-code-badge" title="${escapeHtml(cleanPath)} (관리자 전용)">${iconCode} <code>${escapeHtml(label || filename)}</code></a>`;
  });

  const rawHtml = marked.parse(converted);
  if (typeof DOMPurify !== 'undefined') {
    return DOMPurify.sanitize(rawHtml, {
      ADD_TAGS: ['small', 'img', 'div', 'a', 'span', 'button', 'svg', 'path', 'line', 'polyline', 'rect', 'circle'],
      ADD_ATTR: ['src', 'alt', 'class', 'style', 'onclick', 'target', 'title', 'loading', 'href', 'data-file-path', 'width', 'height', 'viewBox', 'fill', 'stroke', 'stroke-width', 'stroke-linecap', 'stroke-linejoin', 'points', 'x1', 'y1', 'x2', 'y2', 'd', 'rx', 'ry', 'cx', 'cy', 'r']
    });
  }
  return rawHtml;
}

function openMediaModal(src, title, rawFilePath) {
  let modal = document.getElementById('globalMediaModal');
  if (!modal) {
    modal = document.createElement('div');
    modal.id = 'globalMediaModal';
    modal.className = 'global-media-modal';
    modal.innerHTML = `
      <div class="modal-media-overlay" onclick="closeMediaModal()"></div>
      <div class="modal-media-dialog">
        <div class="modal-media-bar">
          <span id="modalMediaTitle" class="modal-media-title">미디어 미리보기</span>
          <div class="modal-media-actions">
            <a id="modalMediaDownload" href="#" target="_blank" class="modal-action-btn" title="다운로드">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path><polyline points="7 10 12 15 17 10"></polyline><line x1="12" y1="15" x2="12" y2="3"></line></svg>
              <span>원본 저장</span>
            </a>
            <button id="modalMediaDeleteBtn" class="modal-action-btn danger" onclick="confirmDeleteCurrentMedia()" title="서버에서 파일 영구 삭제">
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
              <span>파일 삭제</span>
            </button>
            <button class="modal-close-icon-btn" onclick="closeMediaModal()" title="닫기 (ESC)">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>
            </button>
          </div>
        </div>
        <div class="modal-media-body">
          <img id="modalMediaImage" src="" alt="" />
        </div>
      </div>
    `;
    document.body.appendChild(modal);

    // Close on Escape key
    window.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') closeMediaModal();
    });
  }
  document.getElementById('modalMediaTitle').textContent = title || '이미지 미리보기';
  document.getElementById('modalMediaImage').src = src;
  document.getElementById('modalMediaDownload').href = src + (src.includes('?') ? '&download=1' : '?download=1');

  const targetPath = rawFilePath || (src.includes('path=') ? decodeURIComponent(src.split('path=')[1].split('&')[0]) : '');
  modal.setAttribute('data-current-path', targetPath);

  modal.classList.add('active');
}

async function confirmDeleteCurrentMedia() {
  const modal = document.getElementById('globalMediaModal');
  if (!modal) return;
  const filePath = modal.getAttribute('data-current-path');
  if (!filePath) return;
  await deleteFileByPath(filePath);
}

async function deleteFileByPath(filePath) {
  if (!filePath) return;
  const filename = filePath.split('/').pop();
  if (!confirm(`⚠️ 정말로 이 파일을 서버에서 영구 삭제하시겠습니까?\n\n파일명: ${filename}\n경로: ${filePath}`)) {
    return;
  }

  try {
    const res = await fetch('/api/file/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: filePath })
    });
    const data = await res.json();
    if (data.success) {
      alert('✅ 파일이 성공적으로 삭제되었습니다.');
      closeMediaModal();
      // Remove preview card from DOM
      document.querySelectorAll(`.media-preview-card[data-file-path="${CSS.escape(filePath)}"]`).forEach(el => el.remove());
      document.querySelectorAll(`span.artifact-badge-group`).forEach(group => {
        if (group.innerHTML.includes(encodeURIComponent(filePath)) || group.innerHTML.includes(filePath)) {
          group.remove();
        }
      });
    } else {
      alert(`❌ 삭제 실패: ${data.error || '알 수 없는 오류'}`);
    }
  } catch (e) {
    alert(`❌ 요청 오류: ${e.message}`);
  }
}

function closeMediaModal() {
  const modal = document.getElementById('globalMediaModal');
  if (modal) modal.classList.remove('active');
}
window.openMediaModal = openMediaModal;
window.closeMediaModal = closeMediaModal;
window.confirmDeleteCurrentMedia = confirmDeleteCurrentMedia;
window.deleteFileByPath = deleteFileByPath;

// Check Current User Auth State
async function checkAuth() {
  try {
    const res = await fetch('/api/me');
    const data = await res.json();
    if (data.authenticated && data.user) {
      currentUser = data.user;
    } else {
      currentUser = null;
    }
  } catch (e) {
    currentUser = null;
  }
}

// Mobile Drawer Toggle
function toggleSidebar() {
  if (sidebar.classList.contains('open')) {
    closeSidebar();
  } else {
    sidebar.classList.add('open');
    backdrop.classList.add('active');
  }
}

function closeSidebar() {
  sidebar.classList.remove('open');
  backdrop.classList.remove('active');
}

if (btnToggleSidebar) {
  btnToggleSidebar.addEventListener('click', (e) => {
    e.stopPropagation();
    toggleSidebar();
  });
}

if (backdrop) {
  backdrop.addEventListener('click', (e) => {
    if (e.target === backdrop) {
      closeSidebar();
    }
  });
}

// Navigation Switcher (Console vs Quota/Resources)
function switchView(viewName) {
  currentView = viewName;
  if (viewName === 'resources') {
    btnNavResources.classList.add('active');
    btnNavConsole.classList.remove('active');
    resourcesView.classList.add('active');
    consoleView.classList.remove('active');
    loadResources();
  } else {
    btnNavConsole.classList.add('active');
    btnNavResources.classList.remove('active');
    consoleView.classList.add('active');
    resourcesView.classList.remove('active');
    if (!selectedSessionId && sessions.length > 0) {
      const savedSessionId = localStorage.getItem(providerStorageKey('last_session'));
      const target = sessions.find(s => s.sessionId === savedSessionId) || sessions[0];
      selectSession(target.sessionId, false);
    } else if (selectedSessionId) {
      const timelineContainer = document.getElementById('chatTimelineContainer');
      if (timelineContainer) {
        requestAnimationFrame(() => {
          timelineContainer.scrollTop = timelineContainer.scrollHeight;
        });
      }
    }
  }
  if (window.location.hash !== '#' + viewName) {
    history.replaceState(null, '', '#' + viewName);
  }
}

if (btnNavConsole) btnNavConsole.addEventListener('click', () => switchView('console'));
if (btnNavResources) btnNavResources.addEventListener('click', () => switchView('resources'));

// Tab switching inside Console Workspace
document.querySelectorAll('.tab-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.tab-pane').forEach(p => p.classList.remove('active'));
    btn.classList.add('active');
    const tabId = btn.getAttribute('data-tab');
    const targetPane = document.getElementById('tab' + tabId.charAt(0).toUpperCase() + tabId.slice(1));
    if (targetPane) targetPane.classList.add('active');
  });
});

// Project Selection in Sidebar
function applyProjectChange(projId) {
  selectedProject = projId;
  localStorage.setItem(providerStorageKey('last_project'), selectedProject);

  if (projectSelect && projectSelect.value !== projId) projectSelect.value = projId;

  renderSessions();

  // If current selected session doesn't belong to the newly selected project, select first available
  const currentSess = sessions.find(s => s.sessionId === selectedSessionId);
  if (selectedProject && currentSess && (currentSess.projectId || 'ai-agent') !== selectedProject) {
    const match = sessions.find(s => (s.projectId || 'ai-agent') === selectedProject);
    if (match) selectSession(match.sessionId, false);
  }
}

if (projectSelect) {
  projectSelect.addEventListener('change', (e) => applyProjectChange(e.target.value));
}

// Load Projects List from /api/projects
async function loadProjects() {
  try {
    const res = await fetch('/api/projects');
    const data = await res.json();
    projects = data.projects || [];

    const badge = document.getElementById('projectCountBadge');
    if (badge) badge.textContent = projects.length + '개';

    let sidebarHtml = '<option value="">전체 프로젝트 (All)</option>';

    projects.forEach(p => {
      sidebarHtml += `<option value="${escapeHtml(p.id)}">${escapeHtml(p.name)}</option>`;
    });

    if (projectSelect) projectSelect.innerHTML = sidebarHtml;

    // Restore last selected project from localStorage
    const savedProj = localStorage.getItem(providerStorageKey('last_project'));
    if (savedProj && projects.some(p => p.id === savedProj)) {
      applyProjectChange(savedProj);
    }
  } catch (e) {
    console.error('Failed to load projects:', e);
  }
}

// Render Sessions List with project filter
function renderSessions() {
  if (!sessionsContainer) return;
  const searchInput = document.getElementById('sessionSearch');
  const query = (searchInput ? searchInput.value : '').toLowerCase().trim();

  const filtered = sessions.filter(s => {
    const proj = s.projectId || 'ai-agent';
    if (selectedProject && proj !== selectedProject) return false;
    if (!query) return true;
    const prompt = (s.prompt || s.title || '').toLowerCase();
    const sid = (s.sessionId || '').toLowerCase();
    return prompt.includes(query) || sid.includes(query);
  });

  let html = '';
  if (filtered.length === 0) {
    html += '<div class="empty-state" style="padding: 24px 0 16px 0; font-size: 12px; color: var(--muted-foreground);">세션이 없습니다</div>';
  } else {
    filtered.forEach(s => {
      const isSelected = s.sessionId === selectedSessionId;
      const statusClass = s.status || (s.isRunning ? 'running' : 'completed');
      const promptText = s.title || s.prompt || '(새 세션)';
      const toolsCount = Number.isFinite(s.toolCount) ? s.toolCount : (s.tools || []).length;
      const projName = s.projectId || 'ai-agent';

      html += `
        <div class="session-item ${isSelected ? 'active' : ''}" data-session-id="${escapeHtml(s.sessionId)}">
          <div class="session-item-header">
            <span class="session-title" title="${escapeHtml(promptText)}">${escapeHtml(promptText)}</span>
            <span class="status-badge ${statusClass}">${statusClass}</span>
          </div>
          <div class="session-meta">
            <span>${escapeHtml(projName)}</span>
            <span>도구 ${toolsCount}개</span>
          </div>
        </div>
      `;
    });
  }

  // Always append the dashed "+ 새 세션 추가" card at the bottom of the session list
  html += `
    <div class="session-item-new" id="btnCreateNewSessionCard" role="button" tabindex="0" title="새 세션 추가">
      <div class="session-new-inner">
        <svg class="session-new-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
          <line x1="12" y1="5" x2="12" y2="19"></line>
          <line x1="5" y1="12" x2="19" y2="12"></line>
        </svg>
        <span class="session-new-text">새 세션 추가</span>
      </div>
    </div>
  `;

  sessionsContainer.innerHTML = html;
}

function getSessionSummary(sid) {
  return sessions.find(s => s.sessionId === sid) || null;
}

function getSessionView(sid) {
  const summary = getSessionSummary(sid);
  const detail = sessionDetails.get(sid);
  if (!detail) return summary;
  return summary ? { ...detail, ...summary } : detail;
}

async function refreshSessionDetail(sid, force = false) {
  if (!sid || recentlyDeletedSessions.has(sid)) return null;
  const summary = getSessionSummary(sid);
  const currentDetail = sessionDetails.get(sid);
  if (!force && currentDetail && currentDetail._summaryRevision === summary?.revision) {
    return currentDetail;
  }
  if (sessionDetailRequests.has(sid)) return sessionDetailRequests.get(sid);

  const requestedRevision = summary?.revision || null;
  sessionDetailLastFetch.set(sid, Date.now());
  const request = fetch(`/api/session?sessionId=${encodeURIComponent(sid)}`)
    .then(async (res) => {
      const detail = await res.json();
      if (!res.ok || !detail?.sessionId) {
        throw new Error(detail?.error || '세션 상세 정보를 불러오지 못했습니다.');
      }
      detail._summaryRevision = requestedRevision;
      sessionDetails.set(sid, detail);
      if (selectedSessionId === sid) updateActiveSessionView(sid);

      const latestRevision = getSessionSummary(sid)?.revision || null;
      if (latestRevision !== requestedRevision) scheduleSessionDetailRefresh(sid);
      return detail;
    })
    .catch((err) => {
      console.error('Failed to load session detail:', err);
      return null;
    })
    .finally(() => sessionDetailRequests.delete(sid));

  sessionDetailRequests.set(sid, request);
  return request;
}

function scheduleSessionDetailRefresh(sid, force = false) {
  if (!sid || recentlyDeletedSessions.has(sid)) return;
  if (sessionDetailTimers.has(sid)) {
    if (!force) return;
    clearTimeout(sessionDetailTimers.get(sid));
  }
  const elapsed = Date.now() - (sessionDetailLastFetch.get(sid) || 0);
  const delay = force ? 0 : Math.max(0, SESSION_DETAIL_REFRESH_MS - elapsed);
  const timer = setTimeout(() => {
    sessionDetailTimers.delete(sid);
    refreshSessionDetail(sid, force);
  }, delay);
  sessionDetailTimers.set(sid, timer);
}

// Delegated click event for session selection and new session card
if (sessionsContainer) {
  sessionsContainer.addEventListener('click', (e) => {
    const newCard = e.target.closest('#btnCreateNewSessionCard');
    if (newCard) {
      openNewSessionModal();
      return;
    }

    const item = e.target.closest('.session-item');
    if (item) {
      const sid = item.getAttribute('data-session-id');
      if (sid) selectSession(sid, true);
    }
  });
}

// Select a session (isUserClick specifies if user explicitly clicked it)
function selectSession(sid, isUserClick = false) {
  selectedSessionId = sid;
  if (sid) {
    localStorage.setItem(providerStorageKey('last_session'), sid);
    const sess = sessions.find(s => s.sessionId === sid);
    if (sess && sess.projectId) {
      localStorage.setItem(providerStorageKey('last_project'), sess.projectId);
    }
    if (activeProvider === 'codex' && sess && (sess.threadId || sess.prompt) && typeof sess.model === 'string' && modelSelect && Array.from(modelSelect.options).some(opt => opt.value === sess.model)) {
      modelSelect.value = sess.model;
      localStorage.setItem(providerStorageKey('last_model'), sess.model);
    }
  }
  renderSessions();

  // ONLY close sidebar if user explicitly clicked an item
  if (isUserClick && window.innerWidth <= 768) {
    closeSidebar();
  }

  updateActiveSessionView(sid);
  scheduleSessionDetailRefresh(sid, true);
}

// Attach Copy Buttons to rendered Markdown code blocks
function attachCodeCopyButtons(container) {
  if (!container) return;
  const preElements = container.querySelectorAll('pre');
  preElements.forEach((pre) => {
    if (pre.querySelector('.code-copy-btn')) return;
    const code = pre.querySelector('code');
    if (!code) return;

    const btn = document.createElement('button');
    btn.className = 'code-copy-btn';
    btn.textContent = '복사';
    btn.setAttribute('title', '코드 클립보드에 복사');

    btn.addEventListener('click', async (e) => {
      e.stopPropagation();
      try {
        await navigator.clipboard.writeText(code.innerText);
        btn.textContent = '복사 완료';
        btn.style.color = '#059669';
        setTimeout(() => {
          btn.textContent = '복사';
          btn.style.color = '';
        }, 2000);
      } catch (err) {
        btn.textContent = '복사 실패';
      }
    });

    pre.appendChild(btn);
  });
}

// State tracking for opened turn inspector tabs
const openTurnInspectors = new Set();
const activeInspectorTabs = new Map();

function loadOlderTurns(sid) {
  const currentLimit = visibleTurnLimits.get(sid) || 20;
  visibleTurnLimits.set(sid, currentLimit + 20);
  if (selectedSessionId === sid) updateActiveSessionView(sid);
}

function toggleTurnInspector(idx, e) {
  if (e) e.stopPropagation();
  if (openTurnInspectors.has(idx)) {
    openTurnInspectors.delete(idx);
  } else {
    openTurnInspectors.add(idx);
    if (!activeInspectorTabs.has(idx)) {
      activeInspectorTabs.set(idx, 'think');
    }
  }
  if (selectedSessionId) {
    updateActiveSessionView(selectedSessionId);
  }
}

function switchInspectorTab(idx, tabName, e) {
  if (e) e.stopPropagation();
  activeInspectorTabs.set(idx, tabName);
  if (selectedSessionId) {
    updateActiveSessionView(selectedSessionId);
  }
}

let lastRenderedTimelineFingerprint = '';
let lastRenderedSessionId = null;

// Update the active session UI with chat thread & clickable inspectors
function updateActiveSessionView(sid) {
  const sess = getSessionView(sid);
  if (!sess) return;
  const hasDetail = sessionDetails.has(sid);

  const isSessionSwitch = (lastRenderedSessionId !== sid);
  lastRenderedSessionId = sid;

  const noSess = document.getElementById('noSessionSelected');
  const sessWs = document.getElementById('sessionWorkspace');
  if (noSess) noSess.style.display = 'none';
  if (sessWs) sessWs.style.display = 'flex';

  // Header info
  const titleEl = document.getElementById('activePromptTitle');
  const projChip = document.getElementById('activeProjectChip');
  const sessChip = document.getElementById('activeSessionChip');
  const statusBadge = document.getElementById('activeStatusBadge');

  if (titleEl) titleEl.textContent = sess.title || sess.prompt || '(프롬프트 없음)';
  if (projChip) projChip.textContent = (sess.projectId || 'ai-agent');
  if (sessChip) sessChip.textContent = 'ID: ' + sess.sessionId;
  if (statusBadge) {
    const st = sess.status || (sess.isRunning ? 'running' : 'completed');
    statusBadge.className = 'status-badge ' + st;
    statusBadge.textContent = st.toUpperCase();
  }

  // Compile all conversation turns (history + current turn)
  const turns = Array.isArray(sess.history) ? [...sess.history] : [];
  if (sess.prompt) {
    const isAlreadyInHistory = turns.length > 0 && turns[turns.length - 1].turnId === sess.turnId;
    if (!isAlreadyInHistory) {
      turns.push({
        turnId: sess.turnId || `turn_curr_${sess.sessionId}`,
        prompt: sess.prompt,
        files: sess.files || [],
        status: sess.status || (sess.isRunning ? 'running' : 'completed'),
        tools: sess.tools || [],
        thinking: sess.thinking || [],
        chat: sess.chat || [],
        finalResponse: sess.finalResponse || '',
        errorMessage: sess.errorMessage || '',
        usage: sess.usage,
        startedAt: sess.startedAt,
        updatedAt: sess.updatedAt,
        isRunning: sess.isRunning
      });
    }
  }

  const timelineContainer = document.getElementById('chatTimelineContainer');
  if (!timelineContainer) return;

  if (!hasDetail) {
    const loadingFingerprint = `${sid}:loading`;
    if (lastRenderedTimelineFingerprint !== loadingFingerprint) {
      timelineContainer.innerHTML = '<div class="empty-state">세션 대화를 불러오는 중입니다...</div>';
      lastRenderedTimelineFingerprint = loadingFingerprint;
    }
    return;
  }

  if (turns.length === 0) {
    timelineContainer.innerHTML = '<div class="empty-state">대화 내역이 없습니다.</div>';
    lastRenderedTimelineFingerprint = `${sid}:empty`;
    return;
  }

  const visibleLimit = visibleTurnLimits.get(sid) || 20;
  const hiddenTurnCount = Math.max(0, turns.length - visibleLimit);
  const visibleTurns = turns.slice(hiddenTurnCount);

  // Build fingerprint of current session state to avoid unnecessary DOM updates & scroll jumps
  const openInspectorsStr = Array.from(openTurnInspectors).sort().join(',');
  const activeTabsStr = Array.from(activeInspectorTabs.entries()).map(([k,v]) => `${k}:${v}`).sort().join(',');
  const turnsFingerprint = visibleTurns.map((t, visibleIdx) => {
    const idx = hiddenTurnCount + visibleIdx;
    return `${idx}_${t.turnId}_${t.status}_${(t.thinking||[]).length}_${(t.tools||[]).length}_${(t.chat||[]).length}_${(t.finalResponse||'').length}_${(t.errorMessage||'').length}_${t.usage?.total_tokens || 0}`;
  }
  ).join('|');
  const approvalFingerprint = sess.pendingApproval
    ? `${sess.pendingApproval.requestId}_${sess.pendingApproval.method}_${sess.pendingApproval.command}_${sess.pendingApproval.reason}`
    : '';
  const currentFingerprint = `${sid}|${sess.status}|${hiddenTurnCount}|${approvalFingerprint}|${openInspectorsStr}|${activeTabsStr}|${turnsFingerprint}`;

  if (currentFingerprint === lastRenderedTimelineFingerprint) {
    // DOM content is identical — avoid touching innerHTML to preserve user scroll smoothly!
    return;
  }

  lastRenderedTimelineFingerprint = currentFingerprint;

  // Save current scroll positions before DOM replace
  const savedTimelineScroll = timelineContainer.scrollTop;
  const isNearBottom = (timelineContainer.scrollHeight - timelineContainer.scrollTop - timelineContainer.clientHeight) < 60;

  const savedSubScrolls = new Map();
  timelineContainer.querySelectorAll('[data-scroll-id]').forEach(el => {
    const key = el.getAttribute('data-scroll-id');
    if (key) {
      savedSubScrolls.set(key, el.scrollTop);
    }
  });

  let chatHtml = hiddenTurnCount > 0 ? `
    <div style="display:flex; justify-content:center; padding: 8px 0 18px;">
      <button type="button" class="btn-secondary" onclick="loadOlderTurns('${escapeHtml(sid)}')">
        이전 대화 ${Math.min(20, hiddenTurnCount)}개 더 보기 · 총 ${hiddenTurnCount}개 숨김
      </button>
    </div>` : '';
  visibleTurns.forEach((turn, visibleIdx) => {
    const idx = hiddenTurnCount + visibleIdx;
    const isInspectorOpen = openTurnInspectors.has(idx);
    const activeTab = activeInspectorTabs.get(idx) || 'think';
    const turnStatus = turn.status || (turn.isRunning ? 'running' : 'completed');
    const userPrompt = (turn.prompt || '(질문 없음)').trim().replace(/\n{3,}/g, '\n\n');
    const thinkingCount = (turn.thinking || []).length;
    const toolsCount = (turn.tools || []).length;

    // Build raw assistant response
    let rawResponse = turn.finalResponse || (turn.chat || []).join('') || '';
    if (turn.status === 'error' && turn.errorMessage && rawResponse) {
      rawResponse += `\n\n> ⚠️ **작업 중 일부 오류가 발생했습니다**: \`${turn.errorMessage}\``;
    } else if (turn.status === 'error' && turn.errorMessage && !rawResponse) {
      rawResponse = `❌ **오류 발생**:\n\`\`\`\n${turn.errorMessage}\n\`\`\``;
    } else if (!rawResponse && turn.isRunning) {
      rawResponse = '*(답변을 생성하는 중입니다...)*';
    } else if (!rawResponse) {
      rawResponse = '*(응답 텍스트 없음)*';
    }

    let usageHtml = '';
    if (turn.usage && (turn.status === 'completed' || !turn.isRunning)) {
      const u = turn.usage;
      const inp = (u.input_tokens || 0).toLocaleString();
      const out = (u.output_tokens || 0).toLocaleString();
      const thk = (u.thinking_tokens || 0).toLocaleString();
      const cch = (u.cache_read_tokens || 0).toLocaleString();
      const tot = (u.total_tokens || ((u.input_tokens || 0) + (u.output_tokens || 0) + (u.thinking_tokens || 0))).toLocaleString();
      usageHtml = `<div class="chat-subtext-footer">🪙 사용 토큰: Total ${tot} (Input: ${inp} · Output: ${out} · Thinking: ${thk} · Cache Read: ${cch})</div>`;
    }

    // Attached files html in user bubble
    let filesHtml = '';
    if (turn.files && turn.files.length > 0) {
      filesHtml = '<div class="chat-files-list">';
      turn.files.forEach(f => {
        const fname = f.name || f.filename || '첨부파일';
        filesHtml += `<span class="chat-file-chip">📎 ${escapeHtml(fname)}</span>`;
      });
      filesHtml += '</div>';
    }

    let inspectorPanelHtml = '';
    if (isInspectorOpen) {
      let tabPaneContent = '';
      if (activeTab === 'resp') {
        tabPaneContent = `<div class="markdown-body" style="font-size: 13px;">${renderCustomMarkdown(rawResponse)}</div>`;
      } else if (activeTab === 'think') {
        const thinkingText = (turn.thinking || []).join('\n\n') || '(생각 과정 로그 없음)';
        tabPaneContent = `<div class="thinking-container" style="font-size: 12px;">${escapeHtml(thinkingText)}</div>`;
      } else if (activeTab === 'tools') {
        let toolsHtml = '';
        if ((turn.tools || []).length === 0) {
          toolsHtml = '<div style="color: var(--text-muted); font-size: 13px; padding: 10px 4px;">실행된 도구가 없습니다.</div>';
        } else if (activeProvider === 'codex') {
          toolsHtml = `<div class="codex-tool-sequence">${compressCodexTools(turn.tools).map(({ name, count }) =>
            `<span class="codex-tool-step">${escapeHtml(name)}(x${count})</span>`
          ).join('<span class="codex-tool-arrow" aria-hidden="true">→</span>')}</div>`;
        } else {
          (turn.tools || []).forEach((t, tIdx) => {
            toolsHtml += `<div class="tool-card"><div class="tool-card-main" title="${escapeHtml(t)}"><span class="tool-card-icon">🛠️</span><span class="tool-card-name">${escapeHtml(t)}</span></div><span class="tool-badge">#${tIdx + 1}</span></div>`;
          });
        }
        tabPaneContent = `<div class="tools-list">${toolsHtml}</div>`;
      } else if (activeTab === 'raw') {
        const displayTurn = activeProvider === 'codex'
          ? { ...turn, tools: (turn.tools || []).map(normalizeCodexToolName) }
          : turn;
        tabPaneContent = `<div class="thinking-container" style="font-size: 11.5px;">${escapeHtml(JSON.stringify(displayTurn, null, 2))}</div>`;
      }

      inspectorPanelHtml = `
        <div class="turn-inspector-panel" id="inspector_turn_${idx}">
          <div class="inspector-tabs">
            <button type="button" class="inspector-tab-btn ${activeTab === 'resp' ? 'active' : ''}" onclick="switchInspectorTab(${idx}, 'resp', event)">답변 (Response)</button>
            <button type="button" class="inspector-tab-btn ${activeTab === 'think' ? 'active' : ''}" onclick="switchInspectorTab(${idx}, 'think', event)">생각 (${thinkingCount})</button>
            <button type="button" class="inspector-tab-btn ${activeTab === 'tools' ? 'active' : ''}" onclick="switchInspectorTab(${idx}, 'tools', event)">도구 (${toolsCount})</button>
            <button type="button" class="inspector-tab-btn ${activeTab === 'raw' ? 'active' : ''}" onclick="switchInspectorTab(${idx}, 'raw', event)">Raw Data</button>
          </div>
          <div class="inspector-tab-content" data-scroll-id="inspector_tab_${idx}_${activeTab}">${tabPaneContent}</div>
        </div>`;
    }

    chatHtml += `
      <div class="chat-turn" data-turn-index="${idx}">
        <!-- User Message Bubble -->
        <div class="chat-message user">
          <div class="chat-avatar">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"></path>
              <circle cx="12" cy="7" r="4"></circle>
            </svg>
          </div>
          <div class="chat-content">
            <div class="chat-header">
              <span>사용자</span>
            </div>
            <div class="chat-bubble-body">
              <div class="user-bubble-text">${escapeHtml(userPrompt)}</div>
              ${filesHtml}
            </div>
          </div>
        </div>

        <!-- Assistant Message Bubble -->
        <div class="chat-message assistant">
          <div class="chat-avatar">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor">
              <path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>
            </svg>
          </div>
          <div class="chat-content">
            <div class="chat-header">
              <span>${activeProvider === 'codex' ? 'Codex Agent' : 'Antigravity Agent'}</span>
              <span class="status-badge ${turnStatus}">${turnStatus.toUpperCase()}</span>
            </div>
            <div class="chat-bubble-body markdown-body">
              ${renderCustomMarkdown(rawResponse)}
              ${usageHtml}
            </div>
            <div class="turn-inspector-bar">
              <button type="button" class="btn-inspect-turn ${isInspectorOpen ? 'active' : ''}" onclick="toggleTurnInspector(${idx}, event)">
                <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <circle cx="11" cy="11" r="8"></circle>
                  <line x1="21" y1="21" x2="16.65" y2="16.65"></line>
                </svg>
                <span>${isInspectorOpen ? '상세 분석 닫기' : `상세 분석 (생각 ${thinkingCount}건 · 도구 ${toolsCount}회 · Raw)`}</span>
              </button>
            </div>
            ${inspectorPanelHtml}
          </div>
        </div>
      </div>
    `;
  });

  if (activeProvider === 'codex' && sess.pendingApproval) {
    const approval = sess.pendingApproval;
    const detail = approval.reason || approval.command || approval.method || '추가 권한이 필요합니다.';
    chatHtml += `
      <div class="approval-card">
        <div class="approval-card-title">실행 승인이 필요합니다</div>
        <div class="approval-card-detail">${escapeHtml(detail)}</div>
        <div class="approval-card-actions">
          <button type="button" class="btn-primary" onclick="resolveCodexApproval('${escapeHtml(sess.sessionId)}', 'accept')">이번만 승인</button>
          <button type="button" class="btn-secondary" onclick="resolveCodexApproval('${escapeHtml(sess.sessionId)}', 'acceptForSession')">이 세션에서 승인</button>
          <button type="button" class="btn-danger" onclick="resolveCodexApproval('${escapeHtml(sess.sessionId)}', 'decline')">거부</button>
        </div>
      </div>
    `;
  }

  timelineContainer.innerHTML = chatHtml;
  timelineContainer.querySelectorAll('pre code:not([data-highlighted])').forEach((block) => hljs.highlightElement(block));
  attachCodeCopyButtons(timelineContainer);

  // Restore scroll positions seamlessly
  savedSubScrolls.forEach((val, key) => {
    const el = timelineContainer.querySelector(`[data-scroll-id="${key}"]`);
    if (el) el.scrollTop = val;
  });

  if (isSessionSwitch) {
    timelineContainer.scrollTop = timelineContainer.scrollHeight;
    requestAnimationFrame(() => {
      timelineContainer.scrollTop = timelineContainer.scrollHeight;
    });
    setTimeout(() => {
      timelineContainer.scrollTop = timelineContainer.scrollHeight;
    }, 80);
  } else if (isNearBottom || sess.isRunning) {
    timelineContainer.scrollTop = timelineContainer.scrollHeight;
  } else {
    timelineContainer.scrollTop = savedTimelineScroll;
  }
}

async function resolveCodexApproval(sessionId, decision) {
  try {
    const res = await fetch('/api/approval', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId, decision })
    });
    const data = await res.json();
    if (res.status === 401) {
      openLoginModal(data.error || '승인을 처리하려면 로그인이 필요합니다.');
      return;
    }
    if (!res.ok) throw new Error(data.error || '승인 처리에 실패했습니다.');
    scheduleSessionDetailRefresh(sessionId, true);
  } catch (err) {
    alert(err.message);
  }
}

// Set to prevent SSE race conditions on deleted sessions
const recentlyDeletedSessions = new Set();

async function deleteSessionById(sid) {
  if (!sid) return;
  recentlyDeletedSessions.add(sid);

  // Optimistically remove from local array immediately
  sessions = sessions.filter(s => s.sessionId !== sid);
  sessionDetails.delete(sid);
  if (sessionDetailTimers.has(sid)) clearTimeout(sessionDetailTimers.get(sid));
  sessionDetailTimers.delete(sid);
  renderSessions();

  if (selectedSessionId === sid) {
    if (sessions.length > 0) {
      selectSession(sessions[0].sessionId, false);
    } else {
      selectedSessionId = null;
      const noSess = document.getElementById('noSessionSelected');
      const sessWs = document.getElementById('sessionWorkspace');
      if (noSess) noSess.style.display = 'flex';
      if (sessWs) sessWs.style.display = 'none';
    }
  }

  try {
    const res = await fetch('/api/session/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId: sid })
    });
    const data = await res.json();
    if (res.status === 401) {
      recentlyDeletedSessions.delete(sid);
      openLoginModal(data.error || '세션을 삭제하려면 로그인이 필요합니다.');
      return;
    }
  } catch (err) {
    console.error('Delete session error:', err);
  }
}

// Connect Server-Sent Events (SSE)
function initSSE() {
  const evtSource = new EventSource(addProviderToUrl('/api/stream'));
  const pChip = document.getElementById('proxyStatusChip');

  evtSource.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      if (data.sessions) {
        sessions = data.sessions.filter(s => !recentlyDeletedSessions.has(s.sessionId));
        renderSessions();

        // Update active session view without touching or closing sidebar drawer!
        if (selectedSessionId && !recentlyDeletedSessions.has(selectedSessionId)) {
          updateActiveSessionView(selectedSessionId);
          const summary = getSessionSummary(selectedSessionId);
          const detail = sessionDetails.get(selectedSessionId);
          if (!detail || detail._summaryRevision !== summary?.revision) {
            scheduleSessionDetailRefresh(selectedSessionId);
          }
        } else if (sessions.length > 0 && currentView === 'console') {
          selectSession(sessions[0].sessionId, false);
        } else if (sessions.length === 0) {
          selectedSessionId = null;
          const noSess = document.getElementById('noSessionSelected');
          const sessWs = document.getElementById('sessionWorkspace');
          if (noSess) noSess.style.display = 'flex';
          if (sessWs) sessWs.style.display = 'none';
        }
      }
    } catch (e) {
      console.error('SSE parse error:', e);
    }
  };

  evtSource.onerror = () => {
    if (pChip) pChip.innerHTML = '<span class="status-dot offline"></span> Offline';
  };

  evtSource.onopen = () => {
    if (pChip) pChip.innerHTML = '<span class="status-dot online"></span> Online';
  };
}

// File Attachment State
let attachedFiles = [];

function formatFileSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

function renderAttachedFiles() {
  const bar = document.getElementById('attachedFilesBar');
  const list = document.getElementById('attachedFilesList');
  if (!bar || !list) return;

  if (attachedFiles.length === 0) {
    bar.style.display = 'none';
    list.innerHTML = '';
    return;
  }

  bar.style.display = 'flex';
  list.innerHTML = attachedFiles.map((f, idx) => `
    <div class="attached-file-chip">
      <span class="file-name" title="${escapeHtml(f.name)}">${escapeHtml(f.name)}</span>
      <span class="file-size">${formatFileSize(f.size)}</span>
      <button type="button" class="btn-remove-file" data-idx="${idx}" title="첨부 삭제">✕</button>
    </div>
  `).join('');

  list.querySelectorAll('.btn-remove-file').forEach(btn => {
    btn.addEventListener('click', (e) => {
      e.stopPropagation();
      const idx = parseInt(btn.getAttribute('data-idx'), 10);
      attachedFiles.splice(idx, 1);
      renderAttachedFiles();
    });
  });
}

async function addFiles(fileList) {
  for (const file of fileList) {
    if (file.size > 25 * 1024 * 1024) {
      alert(`파일 '${file.name}'이 25MB 크기 제한을 초과했습니다.`);
      continue;
    }
    const base64 = await readFileAsBase64(file);
    attachedFiles.push({
      name: file.name,
      size: file.size,
      type: file.type,
      content_base64: base64
    });
  }
  renderAttachedFiles();
}

function readFileAsBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const dataUrl = reader.result;
      const base64 = dataUrl.split(',')[1];
      resolve(base64);
    };
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });
}

const fileInput = document.getElementById('webFileInput');
const btnAttach = document.getElementById('btnAttachFile');

if (btnAttach && fileInput) {
  btnAttach.addEventListener('click', () => fileInput.click());
  fileInput.addEventListener('change', async () => {
    if (fileInput.files && fileInput.files.length > 0) {
      await addFiles(fileInput.files);
      fileInput.value = '';
    }
  });
}

// Drag and drop files onto Console
const dropZone = document.getElementById('consoleView');
if (dropZone) {
  dropZone.addEventListener('dragover', (e) => {
    e.preventDefault();
    e.stopPropagation();
  });
  dropZone.addEventListener('drop', async (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length > 0) {
      await addFiles(e.dataTransfer.files);
    }
  });
}

// Send New Prompt from Web Console (with attached files)
// =========================================
// Login Modal Logic
// =========================================
const loginModalBackdrop = document.getElementById('loginModalBackdrop');
const btnOpenLoginModal = document.getElementById('btnOpenLoginModal');
const btnSidebarLogin = document.getElementById('btnSidebarLogin');
const btnCloseLoginModal = document.getElementById('btnCloseLoginModal');
const modalOtpForm = document.getElementById('modalOtpForm');
const modalOtpCode = document.getElementById('modalOtpCode');
const modalErrorBox = document.getElementById('modalErrorBox');
const btnModalSubmit = document.getElementById('btnModalSubmit');

function openLoginModal(hintMsg) {
  if (!loginModalBackdrop) return;
  loginModalBackdrop.style.display = 'flex';
  if (modalErrorBox) {
    if (hintMsg) {
      modalErrorBox.textContent = hintMsg;
      modalErrorBox.style.display = 'block';
    } else {
      modalErrorBox.style.display = 'none';
      modalErrorBox.textContent = '';
    }
  }
  if (modalOtpCode) {
    modalOtpCode.value = '';
    setTimeout(() => modalOtpCode.focus(), 50);
  }
}

function closeLoginModal() {
  if (!loginModalBackdrop) return;
  loginModalBackdrop.style.display = 'none';
}

if (btnOpenLoginModal) btnOpenLoginModal.addEventListener('click', () => openLoginModal());
if (btnSidebarLogin) btnSidebarLogin.addEventListener('click', () => openLoginModal());
if (btnCloseLoginModal) btnCloseLoginModal.addEventListener('click', closeLoginModal);

if (loginModalBackdrop) {
  loginModalBackdrop.addEventListener('click', (e) => {
    if (e.target === loginModalBackdrop) closeLoginModal();
  });
}

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && loginModalBackdrop && loginModalBackdrop.style.display === 'flex') {
    closeLoginModal();
  }
});

if (modalOtpForm) {
  modalOtpForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const code = (modalOtpCode?.value || '').trim();
    if (!code || code.length !== 6) {
      if (modalErrorBox) {
        modalErrorBox.textContent = '6자리 숫자를 정확히 입력해 주세요.';
        modalErrorBox.style.display = 'block';
      }
      return;
    }

    if (btnModalSubmit) {
      btnModalSubmit.disabled = true;
      btnModalSubmit.textContent = '인증 확인 중...';
    }
    if (modalErrorBox) modalErrorBox.style.display = 'none';

    try {
      const res = await fetch('/auth/code-login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code })
      });
      const data = await res.json();
      if (data.success) {
        window.location.reload();
      } else {
        if (modalErrorBox) {
          modalErrorBox.textContent = data.error || '인증에 실패했습니다.';
          modalErrorBox.style.display = 'block';
        }
        if (btnModalSubmit) {
          btnModalSubmit.disabled = false;
          btnModalSubmit.textContent = '인증 및 로그인';
        }
        if (modalOtpCode) modalOtpCode.select();
      }
    } catch (err) {
      if (modalErrorBox) {
        modalErrorBox.textContent = '서버 통신 오류가 발생했습니다.';
        modalErrorBox.style.display = 'block';
      }
      if (btnModalSubmit) {
        btnModalSubmit.disabled = false;
        btnModalSubmit.textContent = '인증 및 로그인';
      }
    }
  });
}

// Model Selector Setup
const modelSelect = document.getElementById('webModelSelect');
if (modelSelect) {
  const savedModel = localStorage.getItem(providerStorageKey('last_model'));
  if (savedModel && Array.from(modelSelect.options).some(opt => opt.value === savedModel)) {
    modelSelect.value = savedModel;
  }
  modelSelect.addEventListener('change', () => {
    localStorage.setItem(providerStorageKey('last_model'), modelSelect.value);
  });
}

// New Session Modal Management
const newSessionModalBackdrop = document.getElementById('newSessionModalBackdrop');
const newSessionForm = document.getElementById('newSessionForm');
const newSessionProject = document.getElementById('newSessionProject');
const newSessionModel = document.getElementById('newSessionModel');
const newSessionPrompt = document.getElementById('newSessionPrompt');
const btnCloseNewSessionModal = document.getElementById('btnCloseNewSessionModal');
const newSessionErrorBox = document.getElementById('newSessionErrorBox');
const btnSubmitNewSession = document.getElementById('btnSubmitNewSession');

function openNewSessionModal() {
  if (!currentUser) {
    openLoginModal('새 세션을 시작하려면 먼저 로그인해 주세요.');
    return;
  }

  // Populate projects dropdown
  if (newSessionProject) {
    let projHtml = '';
    if (!projects || projects.length === 0) {
      projHtml = '<option value="ai-agent">ai-agent</option>';
    } else {
      projects.forEach(p => {
        const pId = typeof p === 'string' ? p : (p.id || p.name);
        const pName = typeof p === 'string' ? p : (p.name || p.id);
        projHtml += `<option value="${escapeHtml(pId)}">${escapeHtml(pName)}</option>`;
      });
    }
    newSessionProject.innerHTML = projHtml;
    if (selectedProject) {
      newSessionProject.value = selectedProject;
    }
  }

  // Select last used model
  if (newSessionModel) {
    const savedModel = localStorage.getItem(providerStorageKey('last_model'));
    if (savedModel && Array.from(newSessionModel.options).some(o => o.value === savedModel)) {
      newSessionModel.value = savedModel;
    }
  }

  if (newSessionPrompt) {
    newSessionPrompt.value = '';
  }

  if (newSessionErrorBox) {
    newSessionErrorBox.style.display = 'none';
    newSessionErrorBox.textContent = '';
  }

  if (btnSubmitNewSession) {
    btnSubmitNewSession.disabled = false;
    btnSubmitNewSession.innerHTML = '<span>새 세션 생성 및 시작</span>';
  }

  if (newSessionModalBackdrop) {
    newSessionModalBackdrop.style.display = 'flex';
  }

  setTimeout(() => {
    if (newSessionPrompt) newSessionPrompt.focus();
  }, 100);
}

function closeNewSessionModal() {
  if (newSessionModalBackdrop) {
    newSessionModalBackdrop.style.display = 'none';
  }
}

if (btnCloseNewSessionModal) {
  btnCloseNewSessionModal.addEventListener('click', closeNewSessionModal);
}

if (newSessionModalBackdrop) {
  newSessionModalBackdrop.addEventListener('click', (e) => {
    if (e.target === newSessionModalBackdrop) {
      closeNewSessionModal();
    }
  });
}

const btnCreateFirstSession = document.getElementById('btnCreateFirstSession');
if (btnCreateFirstSession) {
  btnCreateFirstSession.addEventListener('click', openNewSessionModal);
}

if (newSessionForm) {
  newSessionForm.addEventListener('submit', async (e) => {
    e.preventDefault();

    const proj = newSessionProject ? newSessionProject.value : (selectedProject || 'ai-agent');
    const model = newSessionModel ? newSessionModel.value : (activeProvider === 'codex' ? '' : 'gemini-3.8-flash-high');
    const prompt = newSessionPrompt ? newSessionPrompt.value.trim() : '';

    if (btnSubmitNewSession) {
      btnSubmitNewSession.disabled = true;
      btnSubmitNewSession.innerHTML = '<span>세션 생성 중...</span>';
    }
    if (newSessionErrorBox) {
      newSessionErrorBox.style.display = 'none';
    }

    try {
      const newSid = (activeProvider === 'codex' ? 'codex_sess_' : 'ag_sess_') + Date.now();
      localStorage.setItem(providerStorageKey('last_model'), model);
      if (modelSelect) modelSelect.value = model;

      if (prompt) {
        // Send initial prompt directly to start the session immediately
        const res = await fetch('/api/prompt', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            prompt,
            projectId: proj,
            sessionId: newSid,
            model,
            files: []
          })
        });
        const data = await res.json();
        if (res.status === 401) {
          closeNewSessionModal();
          openLoginModal(data.error || '로그인이 필요합니다.');
          return;
        }
        if (!res.ok) {
          if (newSessionErrorBox) {
            newSessionErrorBox.textContent = data.error || '세션 생성 실패';
            newSessionErrorBox.style.display = 'block';
          }
          if (btnSubmitNewSession) {
            btnSubmitNewSession.disabled = false;
            btnSubmitNewSession.innerHTML = '<span>새 세션 생성 및 시작</span>';
          }
          return;
        }
      } else {
        // Create empty session
        const res = await fetch('/api/session/create', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            projectId: proj,
            sessionId: newSid,
            model,
            title: '새 세션'
          })
        });
        const data = await res.json();
        if (res.status === 401) {
          closeNewSessionModal();
          openLoginModal(data.error || '로그인이 필요합니다.');
          return;
        }
        if (!res.ok) {
          if (newSessionErrorBox) {
            newSessionErrorBox.textContent = data.error || '세션 생성 실패';
            newSessionErrorBox.style.display = 'block';
          }
          if (btnSubmitNewSession) {
            btnSubmitNewSession.disabled = false;
            btnSubmitNewSession.innerHTML = '<span>새 세션 생성 및 시작</span>';
          }
          return;
        }
      }

      closeNewSessionModal();
      selectedSessionId = newSid;

      if (selectedProject && selectedProject !== proj) {
        applyProjectChange(proj);
      } else {
        renderSessions();
        selectSession(newSid, false);
      }

      // Close mobile drawer if open
      closeSidebar();

      // Focus prompt input for typing
      const bottomInput = document.getElementById('webPromptInput');
      if (bottomInput) {
        bottomInput.focus();
      }
    } catch (err) {
      console.error('Submit new session error:', err);
      if (newSessionErrorBox) {
        newSessionErrorBox.textContent = '통신 오류: ' + err.message;
        newSessionErrorBox.style.display = 'block';
      }
      if (btnSubmitNewSession) {
        btnSubmitNewSession.disabled = false;
        btnSubmitNewSession.innerHTML = '<span>새 세션 생성 및 시작</span>';
      }
    }
  });
}

// Send New Prompt from Web Console (with attached files)
async function sendPrompt() {
  const input = document.getElementById('webPromptInput');
  if (!input) return;
  const prompt = input.value.trim();
  if (!prompt && attachedFiles.length === 0) return;

  let currentSess = sessions.find(s => s.sessionId === selectedSessionId);
  let proj = (currentSess && currentSess.projectId) || selectedProject || (projects.length > 0 ? projects[0].id : 'ai-agent');
  let targetSid = selectedSessionId;

  if (!targetSid) {
    targetSid = (activeProvider === 'codex' ? 'codex_sess_' : 'ag_sess_') + Date.now();
    selectedSessionId = targetSid;
  }

  const sendingFiles = [...attachedFiles];
  const selectedModel = modelSelect ? modelSelect.value : (localStorage.getItem(providerStorageKey('last_model')) || (activeProvider === 'codex' ? '' : 'gemini-3.8-flash-high'));

  try {
    const res = await fetch('/api/prompt', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        prompt,
        projectId: proj,
        sessionId: targetSid,
        model: selectedModel,
        files: sendingFiles
      })
    });
    const data = await res.json();
    if (res.status === 401) {
      openLoginModal(data.error || '프롬프트를 전송하려면 먼저 로그인해 주세요.');
      return;
    }
    if (!res.ok) {
      alert('오류: ' + (data.error || '요청 처리에 실패했습니다.'));
      return;
    }

    attachedFiles = [];
    renderAttachedFiles();
    input.value = '';
  } catch (err) {
    alert('프롬프트 전송 실패: ' + err.message);
  }
}

const btnSend = document.getElementById('btnSendPrompt');
if (btnSend) btnSend.addEventListener('click', sendPrompt);

const promptInp = document.getElementById('webPromptInput');
if (promptInp) {
  promptInp.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') sendPrompt();
  });
  promptInp.addEventListener('paste', async (e) => {
    if (e.clipboardData && e.clipboardData.files && e.clipboardData.files.length > 0) {
      await addFiles(e.clipboardData.files);
    }
  });
}

// Cancel Session
const btnCancel = document.getElementById('btnCancelSession');
if (btnCancel) {
  btnCancel.addEventListener('click', async () => {
    if (!selectedSessionId) return;
    if (!confirm('현재 세션 작업을 중단하시겠습니까?')) return;
    try {
      const res = await fetch('/api/cancel', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sessionId: selectedSessionId })
      });
      const data = await res.json();
      if (res.status === 401) {
        openLoginModal(data.error || '작업을 중단하려면 로그인이 필요합니다.');
        return;
      }
    } catch (err) {
      console.error('Cancel error:', err);
    }
  });
}

const btnCompact = document.getElementById('btnCompactSession');
if (btnCompact) {
  btnCompact.addEventListener('click', async () => {
    if (!selectedSessionId) return;
    const sess = sessions.find(s => s.sessionId === selectedSessionId);
    const proj = (sess && sess.projectId) || selectedProject || 'ai-agent';

    if (!confirm(`'${proj}' 프로젝트의 세션 대화 맥락을 요약 압축(Compact)하시겠습니까?\n과거 누적된 토큰을 정리하고 핵심 맥락만 새 세션으로 깔끔하게 이식합니다.`)) return;

    btnCompact.disabled = true;
    btnCompact.textContent = '압축 중...';

    try {
      const res = await fetch('/api/compact', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sessionId: selectedSessionId, projectId: proj })
      });
      const data = await res.json();
      if (res.status === 401) {
        openLoginModal(data.error || '세션을 압축하려면 로그인이 필요합니다.');
        return;
      }
      if (data.status === 'compacted') {
        alert('세션 압축(Compact)이 성공적으로 완료되었습니다!');
      }
      await loadInitialData();
    } catch (err) {
      console.error('Compact error:', err);
      alert('압축 처리 중 오류가 발생했습니다.');
    } finally {
      btnCompact.disabled = false;
      btnCompact.textContent = 'Compact';
    }
  });
}

// Clear / Delete Session
const btnClear = document.getElementById('btnClearSession');
if (btnClear) {
  btnClear.addEventListener('click', async () => {
    if (!selectedSessionId) return;
    if (!confirm('이 세션을 목록에서 완전히 삭제하시겠습니까?')) return;
    await deleteSessionById(selectedSessionId);
  });
}

// Search filter listener
const searchInput = document.getElementById('sessionSearch');
if (searchInput) {
  searchInput.addEventListener('input', renderSessions);
}

// =========================================
// Resources & Quotas Logic
// =========================================

function formatTimeRemaining(targetIsoString) {
  if (!targetIsoString) return { text: '100% 완충', hours: 0, minutes: 0, seconds: 0, totalSec: 0 };
  const target = new Date(targetIsoString).getTime();
  const now = Date.now();
  const diffSec = Math.floor((target - now) / 1000);

  if (diffSec <= 0) {
    return { text: '지금 리셋됨', hours: 0, minutes: 0, seconds: 0, totalSec: 0 };
  }

  const hours = Math.floor(diffSec / 3600);
  const minutes = Math.floor((diffSec % 3600) / 60);
  const seconds = diffSec % 60;

  const pad = n => String(n).padStart(2, '0');
  return {
    text: `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`,
    humanText: `${hours}시간 ${minutes}분 ${seconds}초 남음`,
    hours, minutes, seconds, totalSec: diffSec
  };
}

function formatKstTime(isoString) {
  if (!isoString) return '-';
  try {
    const d = new Date(isoString);
    return d.toLocaleString('ko-KR', {
      timeZone: 'Asia/Seoul',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false
    }) + ' (KST)';
  } catch (e) {
    return isoString;
  }
}

function formatUptime(seconds) {
  if (!seconds) return '-';
  const days = Math.floor(seconds / 86400);
  const hrs = Math.floor((seconds % 86400) / 3600);
  const mins = Math.floor((seconds % 3600) / 60);
  if (days > 0) return `${days}일 ${hrs}시간 ${mins}분`;
  if (hrs > 0) return `${hrs}시간 ${mins}분`;
  return `${mins}분`;
}

function updateLiveCountdown() {
  if (!quotaData) return;
  const ag = activeProvider === 'codex' ? quotaData.codex : quotaData.antigravity;
  const liveCountdownEl = document.getElementById('liveResetCountdown');

  if (!ag || !ag.earliestResetTime) {
    if (liveCountdownEl) liveCountdownEl.textContent = '100% 충전됨';
  } else {
    const res = formatTimeRemaining(ag.earliestResetTime);
    if (liveCountdownEl) liveCountdownEl.textContent = res.text;
  }

  // Update per-pool countdown texts
  document.querySelectorAll('[data-pool-reset]').forEach(el => {
    const rTime = el.getAttribute('data-pool-reset');
    if (rTime) {
      const mRes = formatTimeRemaining(rTime);
      el.textContent = mRes.text;
    }
  });
}

async function loadResources() {
  if (btnRefreshResources) btnRefreshResources.classList.add('spinning');
  try {
    const res = await fetch('/api/resources');
    quotaData = await res.json();

    const nowStr = new Date().toLocaleTimeString('ko-KR', { hour12: false });
    const lastEl = document.getElementById('lastUpdatedChip');
    if (lastEl) lastEl.textContent = '마지막 갱신: ' + nowStr;

    renderResourcesUI();
  } catch (err) {
    console.error('Failed to load resources:', err);
  } finally {
    if (btnRefreshResources) btnRefreshResources.classList.remove('spinning');
  }
}

if (btnRefreshResources) {
  btnRefreshResources.addEventListener('click', loadResources);
}

function renderResourcesUI() {
  if (!quotaData) return;
  const isCodex = activeProvider === 'codex';
  const ag = (isCodex ? quotaData.codex : quotaData.antigravity) || {};
  const sys = quotaData.system || {};

  const resourcesTitle = document.getElementById('resourcesTitle');
  const resourcesSubtitle = document.getElementById('resourcesSubtitle');
  const planNameLabel = document.getElementById('planNameLabel');
  const resetNameLabel = document.getElementById('resetNameLabel');
  if (resourcesTitle) resourcesTitle.textContent = isCodex ? 'Codex 사용량 및 한도 현황' : 'Antigravity AI 자원 및 크레딧 현황';
  if (resourcesSubtitle) resourcesSubtitle.textContent = isCodex ? 'ChatGPT Codex의 5시간·주간 사용 한도와 리셋 타이머' : 'Google Antigravity 실시간 모델군 쿼터 풀, 크레딧 및 리셋 타이머';
  if (planNameLabel) planNameLabel.textContent = isCodex ? 'ChatGPT Codex 플랜' : 'Google Antigravity 플랜';
  if (resetNameLabel) resetNameLabel.textContent = isCodex ? '가장 빠른 Codex 한도 리셋' : '가장 빠른 모델 쿼터 리셋';

  // 1. Google AI Plan & Tier
  const tierName = (ag.userTier && ag.userTier.name) ? ag.userTier.name : (isCodex ? 'ChatGPT' : 'Google AI Pro');
  const planTierNameEl = document.getElementById('planTierName');
  const planTierBadgeEl = document.getElementById('planTierBadge');
  const planLimitsDetailEl = document.getElementById('planLimitsDetail');

  if (planTierNameEl) planTierNameEl.textContent = tierName;
  if (planTierBadgeEl) planTierBadgeEl.textContent = ((ag.planInfo && ag.planInfo.planName) ? ag.planInfo.planName : (isCodex ? 'CHATGPT' : 'PRO')).toUpperCase();

  const maxInputTokens = (ag.planInfo && ag.planInfo.maxNumChatInputTokens) ? Number(ag.planInfo.maxNumChatInputTokens).toLocaleString() : '16,384';
  if (planLimitsDetailEl) {
    planLimitsDetailEl.innerHTML = isCodex
      ? `사용 한도: <strong style="color:#58a6ff;">${(ag.quotaGroups || []).length}개 윈도우</strong> | 잔여율은 실시간 계정 기준`
      : `최대 컨텍스트: <strong style="color:#58a6ff;">${maxInputTokens} 토큰</strong> | 프리미엄: <strong>무제한</strong>`;
  }

  // 2. Earliest Reset Countdown
  const resetKstEl = document.getElementById('earliestResetTimeKst');
  const resetSubEl = document.getElementById('resetRelativeSubtitle');
  const liveCountdownEl = document.getElementById('liveResetCountdown');
  const resetCreditBadge = document.getElementById('resetCreditBadge');
  if (resetCreditBadge) {
    const count = Number(ag.rateLimitResetCredits?.availableCount);
    const showCredits = isCodex && Number.isFinite(count) && count >= 0;
    resetCreditBadge.hidden = !showCredits;
    if (showCredits) {
      resetCreditBadge.textContent = String(Math.floor(count));
      resetCreditBadge.setAttribute('aria-label', `사용 가능한 banked reset ${Math.floor(count)}개`);
      resetCreditBadge.title = `사용 가능한 banked reset ${Math.floor(count)}개`;
    }
  }

  const anyDepleted = (ag.quotaGroups || []).some(g => (g.remainingPercent !== undefined ? g.remainingPercent : 100) < 100);

  if (ag.earliestResetTime && anyDepleted) {
    if (resetKstEl) resetKstEl.textContent = formatKstTime(ag.earliestResetTime);
    const rem = formatTimeRemaining(ag.earliestResetTime);
    if (liveCountdownEl) {
      liveCountdownEl.style.display = 'inline-flex';
      liveCountdownEl.textContent = rem.text;
    }
    if (resetSubEl) resetSubEl.textContent = rem.humanText + ' 후 100% 쿼터 충전';
  } else {
    if (resetKstEl) resetKstEl.textContent = '모든 모델 100% 충전됨';
    if (liveCountdownEl) {
      liveCountdownEl.textContent = '100% 완충';
    }
    if (resetSubEl) resetSubEl.textContent = '현재 모든 쿼터가 최대 상태입니다';
  }

  // 3. Render Pooled Quota Groups (Gemini Family & Claude/GPT Family)
  const quotaGroups = ag.quotaGroups || [];
  const modelCountChipEl = document.getElementById('modelCountChip');
  if (modelCountChipEl) {
    modelCountChipEl.textContent = `${quotaGroups.length}개 쿼터 풀 (${(ag.models || []).length}개 모델)`;
  }

  const container = document.getElementById('quotaGroupsContainer');
  if (container) {
    if (quotaGroups.length === 0) {
      container.innerHTML = '<div class="empty-state" style="padding: 30px 0;">활성화된 쿼터 풀 정보를 불러올 수 없습니다.</div>';
    } else {
      let gHtml = '';
      quotaGroups.forEach(g => {
        const remPercent = g.remainingPercent !== undefined ? g.remainingPercent : 100;
        let colorClass = 'green';
        let textColor = '#059669';
        if (remPercent < 20) { colorClass = 'red'; textColor = '#dc2626'; }
        else if (remPercent < 50) { colorClass = 'yellow'; textColor = '#d97706'; }

        const resetText = g.resetTime ? formatTimeRemaining(g.resetTime).text : '100% 완충';
        const kstReset = g.resetTime ? formatKstTime(g.resetTime) : '';

        const modelsHtml = isCodex ? '' : (g.modelList || []).map(m => `<span class="model-pill">${escapeHtml(m)}</span>`).join('');

        // Only show countdown pill if quota is depleted (< 100%)
        const timerHtml = (remPercent < 100 && g.resetTime)
          ? `<span class="countdown-pill" data-pool-reset="${escapeHtml(g.resetTime || '')}" title="${escapeHtml(kstReset)}">${resetText}</span>`
          : ``;

        gHtml += `
          <div class="quota-group-card">
            <div class="quota-group-top">
              <div class="quota-group-info">
                <div class="quota-group-icon ${isCodex ? 'codex-quota-icon' : ''}">${isCodex ? CODEX_QUOTA_ICON : (g.iconSvg || '')}</div>
                <div class="quota-group-text">
                  <div style="display: flex; align-items: center; gap: 8px; flex-wrap: wrap;">
                    <span class="quota-group-title">${escapeHtml(g.title)}</span>
                    <span class="tag-badge fast">${escapeHtml(g.family)}</span>
                  </div>
                  <span class="quota-group-sub">${escapeHtml(g.subtitle || '')}</span>
                </div>
              </div>

              <div class="quota-group-metrics">
                <span class="quota-fraction-value" style="color: ${textColor};">
                  ${remPercent.toFixed(1)}% <span style="font-size: 12px; font-weight: 500; color: var(--text-muted);">잔여 쿼터</span>
                </span>
                ${timerHtml}
              </div>
            </div>

            <div class="progress-track large">
              <div class="progress-fill ${colorClass}" style="width: ${remPercent}%;"></div>
            </div>

            ${isCodex ? '' : `<div class="quota-models-box">
              <div class="quota-models-label">적용 대상 모델 (${(g.modelList || []).length}개)</div>
              <div class="quota-models-tags">
                ${modelsHtml}
              </div>
            </div>`}
          </div>
        `;
      });
      container.innerHTML = gHtml;
    }
  }
}

// Helper: Escape HTML special characters
function escapeHtml(str) {
  if (!str) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

// Start Live Countdown Timer Interval (1s)
if (!countdownInterval) {
  countdownInterval = setInterval(updateLiveCountdown, 1000);
}

// Periodic Resource auto-refresh (every 20s if viewing resources)
setInterval(() => {
  if (currentView === 'resources') {
    loadResources();
  }
}, 20000);

// Context Menu & Session Actions
let contextMenuSessionId = null;
const sessionContextMenu = document.getElementById('sessionContextMenu');
const ctxRenameSession = document.getElementById('ctxRenameSession');
const ctxDeleteSession = document.getElementById('ctxDeleteSession');
const renameModalBackdrop = document.getElementById('renameModalBackdrop');
const renameSessionForm = document.getElementById('renameSessionForm');
const renameSessionId = document.getElementById('renameSessionId');
const renameSessionInput = document.getElementById('renameSessionInput');
const btnCloseRenameModal = document.getElementById('btnCloseRenameModal');
const btnCancelRename = document.getElementById('btnCancelRename');

function hideContextMenu() {
  if (sessionContextMenu) {
    sessionContextMenu.style.display = 'none';
  }
}

function showContextMenu(x, y, sessionId) {
  if (!sessionContextMenu) return;
  contextMenuSessionId = sessionId;

  sessionContextMenu.style.display = 'block';
  const menuWidth = 160;
  const menuHeight = 80;
  const maxX = window.innerWidth - menuWidth - 10;
  const maxY = window.innerHeight - menuHeight - 10;

  sessionContextMenu.style.left = `${Math.min(x, maxX)}px`;
  sessionContextMenu.style.top = `${Math.min(y, maxY)}px`;
}

// Right click on session item in sidebar
if (sessionsContainer) {
  sessionsContainer.addEventListener('contextmenu', (e) => {
    // Only logged in user can access context menu
    if (!currentUser) return;

    const item = e.target.closest('.session-item');
    if (item) {
      e.preventDefault();
      const sid = item.getAttribute('data-session-id');
      if (sid) {
        showContextMenu(e.clientX, e.clientY, sid);
      }
    }
  });
}

// Global dismiss context menu
window.addEventListener('click', (e) => {
  if (sessionContextMenu && !sessionContextMenu.contains(e.target)) {
    hideContextMenu();
  }
});
window.addEventListener('scroll', hideContextMenu, true);
window.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    hideContextMenu();
    if (renameModalBackdrop) renameModalBackdrop.style.display = 'none';
  }
});

// Open rename modal helper
function openRenameModal(sid) {
  if (!sid) sid = selectedSessionId || contextMenuSessionId;
  if (!sid) return;

  const sess = sessions.find(s => s.sessionId === sid);
  const curTitle = sess ? (sess.title || sess.prompt || '') : '';

  if (renameSessionId) renameSessionId.value = sid;
  if (renameSessionInput) {
    renameSessionInput.value = curTitle;
    setTimeout(() => {
      renameSessionInput.focus();
      renameSessionInput.select();
    }, 100);
  }
  if (renameModalBackdrop) {
    renameModalBackdrop.style.display = 'flex';
  }
}

// Rename session trigger from context menu
if (ctxRenameSession) {
  ctxRenameSession.addEventListener('click', () => {
    const sid = contextMenuSessionId;
    hideContextMenu();
    if (sid) openRenameModal(sid);
  });
}

// Rename session trigger from main header button
const btnRenameSession = document.getElementById('btnRenameSession');
if (btnRenameSession) {
  btnRenameSession.addEventListener('click', () => {
    openRenameModal(selectedSessionId);
  });
}

// Rename session trigger from title click
const activePromptTitle = document.getElementById('activePromptTitle');
if (activePromptTitle) {
  activePromptTitle.addEventListener('click', () => {
    openRenameModal(selectedSessionId);
  });
}

// Close rename modal
if (btnCloseRenameModal) {
  btnCloseRenameModal.addEventListener('click', () => {
    if (renameModalBackdrop) renameModalBackdrop.style.display = 'none';
  });
}
if (btnCancelRename) {
  btnCancelRename.addEventListener('click', () => {
    if (renameModalBackdrop) renameModalBackdrop.style.display = 'none';
  });
}

// Submit rename form
if (renameSessionForm) {
  renameSessionForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const sid = renameSessionId ? renameSessionId.value : (contextMenuSessionId || selectedSessionId);
    const newTitle = renameSessionInput ? renameSessionInput.value.trim() : '';
    if (!sid || !newTitle) return;

    try {
      const res = await fetch('/api/session/rename', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sessionId: sid, title: newTitle })
      });
      const data = await res.json();
      if (data.status === 'renamed' || data.success || data.title) {
        const sess = sessions.find(s => s.sessionId === sid);
        if (sess) {
          sess.title = newTitle;
          sess.isCustomTitle = true;
        }
        renderSessions();
        if (selectedSessionId === sid) {
          updateActiveSessionView(sid);
        }
      }
    } catch (err) {
      console.error('Failed to rename session:', err);
    } finally {
      if (renameModalBackdrop) renameModalBackdrop.style.display = 'none';
    }
  });
}

// Delete session trigger
if (ctxDeleteSession) {
  ctxDeleteSession.addEventListener('click', async () => {
    const sid = contextMenuSessionId;
    hideContextMenu();
    if (!sid) return;
    if (!confirm('이 세션을 목록에서 완전히 삭제하시겠습니까?')) return;
    await deleteSessionById(sid);
  });
}

async function loadInitialData() {
  try {
    const response = await fetch('/api/sessions?summary=1');
    const data = await response.json();
    sessions = (data.sessions || []).filter(s => !recentlyDeletedSessions.has(s.sessionId));
    renderSessions();
    if (sessions.length === 0) return;

    const savedSessionId = localStorage.getItem(providerStorageKey('last_session'));
    const savedProjectId = localStorage.getItem(providerStorageKey('last_project'));
    let targetSession = savedSessionId ? sessions.find(s => s.sessionId === savedSessionId) : null;

    if (!targetSession && savedProjectId) {
      const projSessions = sessions.filter(s => (s.projectId || 'ai-agent') === savedProjectId);
      if (projSessions.length > 0) {
        targetSession = projSessions.reduce((latest, curr) => {
          const latestTime = latest.updatedAt || latest.startedAt || 0;
          const currentTime = curr.updatedAt || curr.startedAt || 0;
          return currentTime > latestTime ? curr : latest;
        }, projSessions[0]);
      }
    }

    if (!targetSession) {
      targetSession = sessions.reduce((latest, curr) => {
        const latestTime = latest.updatedAt || latest.startedAt || 0;
        const currentTime = curr.updatedAt || curr.startedAt || 0;
        return currentTime > latestTime ? curr : latest;
      }, sessions[0]);
    }

    if (targetSession.projectId && projectSelect) {
      selectedProject = targetSession.projectId;
      projectSelect.value = selectedProject;
    }
    selectSession(targetSession.sessionId, false);
  } catch (err) {
    console.error('Failed to load sessions:', err);
  }
}

// Initialize on Load
checkAuth();
loadProjects();
loadInitialData();

// Check URL Hash for initial view
if (window.location.hash === '#resources' || window.location.pathname.startsWith('/resources') || window.location.pathname.startsWith('/quota')) {
  switchView('resources');
}

// Dynamic Mobile Viewport & Virtual Keyboard Handler
function setupMobileKeyboardAdjustment() {
  const promptInput = document.getElementById('webPromptInput');
  const appRoot = document.querySelector('.app-root');

  function updateViewportHeight() {
    if (window.visualViewport) {
      const vh = window.visualViewport.height;
      document.documentElement.style.setProperty('--app-height', `${vh}px`);
      if (appRoot) {
        appRoot.style.height = `${vh}px`;
      }
      if (promptInput && document.activeElement === promptInput) {
        promptInput.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
      }
    }
  }

  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', updateViewportHeight);
    window.visualViewport.addEventListener('scroll', updateViewportHeight);
    updateViewportHeight();
  }

  if (promptInput) {
    promptInput.addEventListener('focus', () => {
      setTimeout(() => {
        updateViewportHeight();
        promptInput.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
      }, 150);
    });
    promptInput.addEventListener('blur', () => {
      setTimeout(() => {
        updateViewportHeight();
      }, 150);
    });
  }
}
setupMobileKeyboardAdjustment();

initSSE();
