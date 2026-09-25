#!/usr/bin/env python3
import asyncio
import codecs
import json
import os
import re
import signal
import ssl
import subprocess
import sys
import time
import urllib.request
from typing import Dict, Any, List, Optional

WORKSPACE_BASE = "/mnt/antigravity_workspaces"
CONV_MAP_FILE = "/tmp/antigravity_conv_map.json"
SESSIONS_CACHE_FILE = "/tmp/antigravity_sessions_cache.json"

agy_conversation_map: Dict[str, str] = {}
agy_session_model_map: Dict[str, str] = {}  # session_id -> last used model_id
running_processes: Dict[str, asyncio.subprocess.Process] = {}
active_sessions: Dict[str, Dict[str, Any]] = {}

cached_ls_info = None
last_ls_check = 0

def get_language_server_info():
    global cached_ls_info, last_ls_check
    now = time.time()
    if cached_ls_info and (now - last_ls_check < 15):
        return cached_ls_info

    try:
        ps_out = subprocess.check_output(["ps", "-ef"], timeout=3).decode("utf-8", errors="ignore")
        csrf_token = None
        pid = None
        for line in ps_out.splitlines():
            if "language_server" in line and "--csrf_token" in line:
                m = re.search(r"--csrf_token\s+([a-zA-Z0-9-]+)", line)
                if m:
                    csrf_token = m.group(1)
                parts = line.strip().split()
                if len(parts) >= 2:
                    pid = parts[1]
                break

        if not csrf_token or not pid:
            return cached_ls_info

        ss_out = subprocess.check_output(["ss", "-tlpn"], timeout=3).decode("utf-8", errors="ignore")
        ports = []
        for line in ss_out.splitlines():
            if f"pid={pid}" in line:
                m = re.search(r":(\d+)\s+", line)
                if m:
                    ports.append(int(m.group(1)))

        if ports:
            cached_ls_info = {"pid": pid, "csrfToken": csrf_token, "ports": ports}
            last_ls_check = now
        return cached_ls_info
    except Exception as e:
        log_debug(f"get_language_server_info error: {e}")
        return cached_ls_info

cached_quota = None
last_quota_time = 0

def get_language_server_quota():
    global cached_quota, last_quota_time
    now = time.time()
    if cached_quota and (now - last_quota_time < 3):
        return cached_quota

    ls_info = get_language_server_info()
    if not ls_info or not ls_info.get("ports"):
        return cached_quota or {"success": False, "error": "Antigravity Language Server not detected"}

    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE

    for port in ls_info["ports"]:
        try:
            req = urllib.request.Request(
                f"https://127.0.0.1:{port}/exa.language_server_pb.LanguageServerService/GetUserStatus",
                data=b"{}",
                headers={
                    "Content-Type": "application/json",
                    "X-Codeium-Csrf-Token": ls_info["csrfToken"]
                },
                method="POST"
            )
            with urllib.request.urlopen(req, context=ctx, timeout=2.5) as resp:
                data = json.loads(resp.read().decode("utf-8"))

            if data and "userStatus" in data:
                u = data["userStatus"]
                plan_status = u.get("planStatus", {})
                plan_info = plan_status.get("planInfo", {})
                raw_models = u.get("cascadeModelConfigData", {}).get("clientModelConfigs", [])

                models = []
                for m in raw_models:
                    q = m.get("quotaInfo") or {}
                    rem_frac = q.get("remainingFraction")
                    if not isinstance(rem_frac, (int, float)):
                        # If resetTime exists, quota was consumed → 0.0; otherwise assume full
                        rem_frac = 0.0 if q.get("resetTime") else 1.0
                    models.append({
                        "modelId": m.get("modelId", ""),
                        "label": m.get("label") or m.get("modelId") or "Unknown Model",
                        "remainingFraction": rem_frac,
                        "remainingPercent": round(rem_frac * 100.0, 1),
                        "resetTime": q.get("resetTime")
                    })

                gemini_models = [m for m in models if m["modelId"].startswith("gemini") or "gemini" in m["label"].lower()]
                premium_models = [m for m in models if not (m["modelId"].startswith("gemini") or "gemini" in m["label"].lower())]

                quota_groups = []
                if gemini_models:
                    rep = gemini_models[0]
                    quota_groups.append({
                        "id": "gemini_pool",
                        "family": "Google Gemini",
                        "remainingPercent": rep["remainingPercent"],
                        "resetTime": rep["resetTime"]
                    })
                if premium_models:
                    rep = premium_models[0]
                    quota_groups.append({
                        "id": "claude_gpt_pool",
                        "family": "Claude & GPT-OSS",
                        "remainingPercent": rep["remainingPercent"],
                        "resetTime": rep["resetTime"]
                    })

                avail_prompt = plan_status.get("availablePromptCredits", 500)
                month_prompt = plan_info.get("monthlyPromptCredits", 50000)
                avail_flow = plan_status.get("availableFlowCredits", 100)
                month_flow = plan_info.get("monthlyFlowCredits", 150000)

                cached_quota = {
                    "success": True,
                    "userTier": u.get("userTier") or {"name": "Google AI Pro"},
                    "planInfo": plan_info,
                    "promptCredits": {
                        "available": avail_prompt,
                        "monthly": month_prompt,
                        "percent": round(float(avail_prompt) / max(float(month_prompt), 1.0) * 100.0, 1)
                    },
                    "flowCredits": {
                        "available": avail_flow,
                        "monthly": month_flow,
                        "percent": round(float(avail_flow) / max(float(month_flow), 1.0) * 100.0, 1)
                    },
                    "quotaGroups": quota_groups
                }
                last_quota_time = now
                return cached_quota
        except Exception as e:
            continue

    return cached_quota or {"success": False, "error": "Could not communicate with language server"}

def log_debug(msg: str):
    sys.stderr.write(f"[PROXY DEBUG] {msg}\n")
    sys.stderr.flush()

def map_tool_name(raw_name: str) -> str:
    if not raw_name:
        return ""
    if raw_name in ["run_command", "shell", "exec"]:
        return "shell"
    return raw_name

def normalize_usage(u: Any) -> Dict[str, int]:
    if not isinstance(u, dict):
        return {}
    input_tokens = int(u.get("input_tokens") or u.get("prompt_tokens") or u.get("input") or 0)
    output_tokens = int(u.get("output_tokens") or u.get("completion_tokens") or u.get("output") or 0)
    thinking_tokens = int(u.get("thinking_tokens") or u.get("thought_tokens") or u.get("reasoning_tokens") or u.get("thinking") or 0)
    cache_read_tokens = int(u.get("cache_read_tokens") or u.get("cache_read_input_tokens") or u.get("cached_tokens") or u.get("cache_read") or 0)
    total_tokens = int(u.get("total_tokens") or u.get("total") or (input_tokens + output_tokens + thinking_tokens))
    return {
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "thinking_tokens": thinking_tokens,
        "cache_read_tokens": cache_read_tokens,
        "total_tokens": total_tokens
    }

def load_conv_map():
    global agy_conversation_map
    if os.path.exists(CONV_MAP_FILE):
        try:
            with open(CONV_MAP_FILE, "r", encoding="utf-8") as f:
                agy_conversation_map = json.load(f)
            log_debug(f"Loaded {len(agy_conversation_map)} conversation mappings from {CONV_MAP_FILE}")
        except Exception as e:
            log_debug(f"Failed to load conv map: {e}")

def save_conv_map():
    try:
        temp_file = f"{CONV_MAP_FILE}.tmp"
        with open(temp_file, "w", encoding="utf-8") as f:
            json.dump(agy_conversation_map, f, ensure_ascii=False, indent=2)
        os.replace(temp_file, CONV_MAP_FILE)
    except Exception as e:
        log_debug(f"Failed to save conv map: {e}")

def clean_response_text(val: Any) -> str:
    if not val:
        return ""
    if isinstance(val, dict):
        extracted = (
            val.get("response") or
            val.get("content") or
            val.get("text") or
            val.get("output") or
            val.get("message")
        )
        if extracted is not None:
            return clean_response_text(extracted)
        if val.get("error"):
            return str(val.get("error"))
        return ""

    if isinstance(val, str):
        trimmed = val.strip()
        if (trimmed.startswith("{") or trimmed.startswith('{"')) and ("response" in trimmed):
            if trimmed.startswith('{"') and trimmed.endswith('}'):
                try:
                    obj = json.loads(trimmed)
                    if isinstance(obj, dict):
                        return clean_response_text(obj)
                except Exception:
                    pass
            m = re.search(r"['\"]response['\"]\s*:\s*(['\"])((?:\\.|[^\\])*?)\1", trimmed)
            if m:
                raw_extracted = m.group(2)
                raw_extracted = re.sub(r'\\u([0-9a-fA-F]{4})', lambda mm: chr(int(mm.group(1), 16)), raw_extracted)
                raw_extracted = raw_extracted.replace('\\n', '\n').replace('\\r', '\r').replace('\\t', '\t')
                raw_extracted = raw_extracted.replace("\\'", "'").replace('\\"', '"').replace('\\\\', '\\')
                return raw_extracted
        return val
    return str(val)

def load_sessions_cache():
    global active_sessions
    if os.path.exists(SESSIONS_CACHE_FILE):
        try:
            with open(SESSIONS_CACHE_FILE, "r", encoding="utf-8") as f:
                active_sessions = json.load(f)
            for sess_id, s_data in active_sessions.items():
                if isinstance(s_data, dict):
                    if s_data.get("model"):
                        agy_session_model_map[sess_id] = s_data["model"]
                    if s_data.get("finalResponse"):
                        s_data["finalResponse"] = clean_response_text(s_data["finalResponse"])
            log_debug(f"Loaded {len(active_sessions)} sessions from {SESSIONS_CACHE_FILE}")
        except Exception as e:
            log_debug(f"Failed to load sessions cache: {e}")

def save_sessions_cache():
    try:
        temp_file = f"{SESSIONS_CACHE_FILE}.tmp"
        with open(temp_file, "w", encoding="utf-8") as f:
            json.dump(active_sessions, f, ensure_ascii=False, indent=2)
        os.replace(temp_file, SESSIONS_CACHE_FILE)
    except Exception as e:
        log_debug(f"Failed to save sessions cache: {e}")

load_conv_map()
load_sessions_cache()

def get_workspace_dir(project_id: str) -> str:
    if not project_id:
        project_id = "default"
    safe_project_id = "".join(c for c in str(project_id) if c.isalnum() or c in ("-", "_")).strip() or "default"
    target_dir = os.path.join(WORKSPACE_BASE, safe_project_id)
    os.makedirs(target_dir, exist_ok=True)
    return target_dir

session_locks: Dict[str, asyncio.Lock] = {}

def get_session_lock(session_id: str) -> asyncio.Lock:
    if session_id not in session_locks:
        session_locks[session_id] = asyncio.Lock()
    return session_locks[session_id]

async def handle_chat_stream(reader: asyncio.StreamReader, writer: asyncio.StreamWriter, req_body: Dict[str, Any]):
    global running_processes, active_sessions
    session_id = "".join(c for c in str(req_body.get("sessionId", "")).strip() if c.isalnum() or c in ("-", "_", ":")) or "default"
    project_id = "".join(c for c in str(req_body.get("projectId", "")).strip() if c.isalnum() or c in ("-", "_")) or "default"
    model_id = str(req_body.get("model", "")).strip() or "gemini-3.8-flash-high"
    prompt = str(req_body.get("prompt", "")).strip()
    turn_id = "".join(c for c in str(req_body.get("turnId", "")).strip() if c.isalnum() or c in ("-", "_", ":")) or f"turn_{int(time.time()*1000)}"
    files_list = list(req_body.get("files", [])) if isinstance(req_body.get("files"), list) else []

    log_debug(f"Handling chat stream request: session_id={session_id}, turn_id={turn_id}, project_id={project_id}, model={model_id}, prompt={prompt[:40]}")

    # Send Chunked HTTP Response Header immediately so client connection stays open
    writer.write(b"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n")
    await writer.drain()

    async def send_chunk(obj: Dict[str, Any]):
        ctype = obj.get("type", "")
        coutput = str(obj.get("output", ""))
        ctool = str(obj.get("tool", ""))

        if session_id in active_sessions:
            s = active_sessions[session_id]
            s["updatedAt"] = time.time()
            if ctype == "tool" and ctool:
                s["tools"].append(ctool)
            elif ctype == "thinking" and coutput:
                s["thinking"].append(coutput)
            elif ctype == "chat" and coutput:
                s["chat"].append(coutput)
            elif ctype == "result" and coutput:
                s["finalResponse"] = clean_response_text(coutput)
                s["errorMessage"] = ""
                s["status"] = "completed"
                s["isRunning"] = False
            elif ctype == "completed":
                s["status"] = "completed"
                s["isRunning"] = False
                if not s["finalResponse"] and s["chat"]:
                    s["finalResponse"] = "".join(s["chat"])
                if s["finalResponse"]:
                    s["errorMessage"] = ""
            elif ctype == "fatal_error" and coutput:
                s["errorMessage"] = coutput
                s["status"] = "error"
                s["isRunning"] = False
            elif ctype == "error" and coutput:
                s["errorMessage"] = coutput
                # Retain running status while process is alive
            elif ctype == "cancelled":
                s["status"] = "cancelled"
                s["isRunning"] = False
            elif ctype == "usage" and coutput:
                try:
                    raw_u = json.loads(coutput)
                    norm = normalize_usage(raw_u)
                    if norm.get("total_tokens", 0) > 0 or norm.get("input_tokens", 0) > 0:
                        s["usage"] = norm
                except Exception:
                    pass
            save_sessions_cache()

        try:
            chunk_bytes = (json.dumps(obj, ensure_ascii=False) + "\n").encode("utf-8")
            chunk_header = f"{len(chunk_bytes):X}\r\n".encode("utf-8")
            writer.write(chunk_header + chunk_bytes + b"\r\n")
            await writer.drain()
        except Exception as e:
            log_debug(f"Failed to write chunk (client may be restarting): {e}")

    session_lock = get_session_lock(session_id)
    if session_lock.locked():
        log_debug(f"Session {session_id} is currently busy. Queueing incoming turn {turn_id}")
        await send_chunk({
            "sessionId": session_id,
            "turnId": turn_id,
            "type": "info",
            "output": "⏳ 이전 작업이 진행 중입니다. 현재 요청이 큐(대기열)에 등록되어 순차적으로 처리됩니다."
        })

    try:
        async with session_lock:
            existing_history = []
            custom_title = ""
            is_custom_title = False
            if session_id in active_sessions:
                old_s = active_sessions[session_id]
                custom_title = old_s.get("title", "")
                is_custom_title = old_s.get("isCustomTitle", False)
                existing_history = list(old_s.get("history", []))
                if old_s.get("prompt"):
                    prev_turn = {
                        "turnId": old_s.get("turnId", f"turn_{int(old_s.get('startedAt', time.time())*1000)}"),
                        "prompt": old_s.get("prompt", ""),
                        "files": old_s.get("files", []),
                        "status": old_s.get("status", "completed"),
                        "tools": list(old_s.get("tools", [])),
                        "thinking": list(old_s.get("thinking", [])),
                        "chat": list(old_s.get("chat", [])),
                        "finalResponse": old_s.get("finalResponse", ""),
                        "errorMessage": old_s.get("errorMessage", ""),
                        "usage": old_s.get("usage"),
                        "startedAt": old_s.get("startedAt", time.time()),
                        "updatedAt": old_s.get("updatedAt", time.time())
                    }
                    existing_history.append(prev_turn)

            # Initialize / Reset session state for this turn
            final_title = custom_title if (is_custom_title or custom_title) else prompt
            sess_dict = {
                "sessionId": session_id,
                "projectId": project_id,
                "model": model_id,
                "title": final_title,
                "isCustomTitle": is_custom_title or bool(custom_title),
                "prompt": prompt,
                "files": files_list,
                "turnId": turn_id,
                "status": "running",
                "tools": [],
                "thinking": [],
                "chat": [],
                "finalResponse": "",
                "errorMessage": "",
                "startedAt": time.time(),
                "updatedAt": time.time(),
                "isRunning": True,
                "history": existing_history
            }
            active_sessions[session_id] = sess_dict
            save_sessions_cache()

            workspace_dir = get_workspace_dir(project_id)
            candidate_paths = [
                "/home/fedora/.local/bin/agy",
                "/usr/local/bin/agy",
                "/usr/bin/agy",
                "agy"
            ]
            agy_bin = "agy"
            for p in candidate_paths:
                if os.path.exists(p) and os.access(p, os.X_OK):
                    agy_bin = p
                    break

            file_headers = []
            if files_list:
                uploads_dir = os.path.join(workspace_dir, "uploads")
                os.makedirs(uploads_dir, exist_ok=True)
                for f in files_list:
                    fname = os.path.basename(f.get("name") or f.get("filename") or f"file_{int(time.time())}")
                    fpath = f.get("path")
                    fcontent_b64 = f.get("content_base64")

                    if fcontent_b64:
                        import base64
                        saved_path = os.path.join(uploads_dir, fname)
                        try:
                            with open(saved_path, "wb") as fb:
                                fb.write(base64.b64decode(fcontent_b64))
                            fpath = saved_path
                        except Exception as ex:
                            log_debug(f"Failed to decode base64 file {fname}: {ex}")
                    elif f.get("url") and not fpath:
                        import urllib.request
                        saved_path = os.path.join(uploads_dir, fname)
                        try:
                            req = urllib.request.Request(f["url"], headers={"User-Agent": "AntigravityProxy"})
                            with urllib.request.urlopen(req) as resp:
                                with open(saved_path, "wb") as fb:
                                    fb.write(resp.read())
                            fpath = saved_path
                            log_debug(f"Downloaded attachment {fname} from URL to {saved_path}")
                        except Exception as ex:
                            log_debug(f"Failed to download attachment {fname} from URL: {ex}")

                    if fpath:
                        file_headers.append(f"[첨부 파일: {fname} | 경로: {fpath}]")
            system_hint = (
                "[System Guidelines - Service Restart & Continuations]\n"
                "1. Proxy Self-Restart: If you edit the proxy code and need to restart it, NEVER run synchronous `pkill python3` (it kills yourself instantly before replying). ALWAYS use a delayed background restart so your response finishes cleanly: `nohup bash -c 'sleep 2 && systemctl --user restart antigravity-proxy.service' > /dev/null 2>&1 &`.\n"
                "2. Long-Running Processes: Always run web servers and daemons (Node.js, Express, etc.) asynchronously using nohup (e.g. `nohup node app.js > /tmp/server.log 2>&1 &`).\n"
                "3. Context Preservation: All past conversation context and file changes are permanently preserved. When asked to continue, immediately proceed with the previous task without starting over.\n"
                "4. User Clarification & Interactive Check-in (단독 임의 진행 금지): 새로운 기능 추가, 모델/외부 API 선정, 아키텍처 및 주요 라이브러리 결정 등 설계 및 구현 방향에 선택지가 있거나 중요한 결정이 필요할 때는 AI가 임의로 판단하여 구현까지 전부 진행하지 마라. 가능한 대안이나 아이디어를 명확히 제시한 후 거기서 멈추고 반드시 사용자에게 먼저 물어보아라 (\"이제 어떻게 진행할까요?\", \"어떤 모델/방식으로 적용할까요?\"). 사용자의 명시적인 컨펌과 선택을 받은 후에 다음 작업을 진행하라.\n\n"
            )
            if file_headers:
                effective_prompt = system_hint + "\n".join(file_headers) + "\n\n" + prompt
            else:
                effective_prompt = system_hint + prompt

            cmd = [
                "stdbuf", "-oL", "-eL",
                agy_bin,
                "--print", effective_prompt,
                "--mode", "accept-edits",
                "--output-format", "stream-json",
                "--dangerously-skip-permissions",
                "--print-timeout", "10m"
            ]
            if workspace_dir:
                cmd.extend(["--add-dir", workspace_dir])

            if project_id:
                cmd.extend(["--project", project_id])

            if model_id:
                cmd.extend(["--model", model_id])

            # Re-use conversation UUID if mapped (skip if model changed to honor new model selection)
            if session_id and session_id != "new":
                prev_model = agy_session_model_map.get(session_id, "")
                if not prev_model and session_id in active_sessions and isinstance(active_sessions[session_id], dict):
                    prev_model = active_sessions[session_id].get("model", "")
                model_changed = prev_model and model_id and prev_model != model_id
                if session_id in agy_conversation_map and not model_changed:
                    target_conv_id = agy_conversation_map[session_id]
                    cmd.extend(["--conversation", target_conv_id])
                    log_debug(f"Resuming mapped agy conversation UUID: {target_conv_id} for session {session_id}")
                elif model_changed:
                    log_debug(f"Model changed from {prev_model} to {model_id} for session {session_id}, starting new conversation")
                    agy_conversation_map.pop(session_id, None)
                    save_conv_map()
            agy_session_model_map[session_id] = model_id
            if session_id and session_id in active_sessions and isinstance(active_sessions[session_id], dict):
                active_sessions[session_id]["model"] = model_id
                save_sessions_cache()

            env = os.environ.copy()
            env["HOME"] = "/home/fedora"
            env["USER"] = "fedora"
            env["LOGNAME"] = "fedora"
            env["XDG_CONFIG_HOME"] = "/home/fedora/.config"
            env["PYTHONUNBUFFERED"] = "1"

            log_debug(f"Executing agy for session {session_id} in {workspace_dir}: {cmd}")

            proc = await asyncio.create_subprocess_exec(
                *cmd,
                cwd=workspace_dir,
                env=env,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                preexec_fn=os.setsid
            )
            running_processes[session_id] = proc
            log_debug(f"agy process started successfully with stdbuf, PID={proc.pid}")

            async def read_stdout():
                decoder = codecs.getincrementaldecoder('utf-8')(errors='replace')
                buffer = ""
                while True:
                    chunk = await proc.stdout.read(4096)
                    if not chunk:
                        log_debug("STDOUT reached EOF")
                        break
                    text = decoder.decode(chunk, final=False)
                    buffer += text
                    while "\n" in buffer:
                        line, buffer = buffer.split("\n", 1)
                        raw_str = line.strip()
                        if not raw_str:
                            continue

                        log_debug(f"STDOUT raw line: {raw_str}")

                        try:
                            event_data = json.loads(raw_str)
                            if isinstance(event_data, dict):
                                real_uuid = (
                                    event_data.get("conversation_id") or
                                    (event_data.get("init", {}).get("conversation_id") if isinstance(event_data.get("init"), dict) else None) or
                                    (event_data.get("step_update", {}).get("conversation_id") if isinstance(event_data.get("step_update"), dict) else None)
                                )
                                if real_uuid and session_id and session_id not in agy_conversation_map:
                                    agy_conversation_map[session_id] = str(real_uuid).strip()
                                    save_conv_map()
                                    log_debug(f"Persisted session mapping: {session_id} -> {real_uuid}")

                                step_obj = event_data.get("step_update") if isinstance(event_data.get("step_update"), dict) else {}
                                tool_info = step_obj.get("tool_info") if isinstance(step_obj.get("tool_info"), dict) else {}

                                # 0. Error detection
                                error_msg = (
                                    event_data.get("error") or
                                    step_obj.get("error") or
                                    tool_info.get("error")
                                )
                                if not error_msg and isinstance(tool_info.get("output"), str):
                                    out_str = tool_info["output"]
                                    if "Error" in out_str and ("not installed" in out_str or "failed" in out_str.lower()):
                                        error_msg = out_str

                                if error_msg:
                                    err_str = str(error_msg)
                                    if "context canceled" in err_str or "manage_task" in err_str or "Step was canceled" in err_str or "hardcoded system protection boundary rule" in err_str:
                                        error_msg = None

                                if error_msg:
                                    await send_chunk({
                                        "sessionId": session_id,
                                        "type": "error",
                                        "tool": "",
                                        "output": str(error_msg)
                                    })

                                # 1. Tool execution detection
                                tool_name = (
                                    event_data.get("tool_name") or
                                    event_data.get("tool") or
                                    (step_obj.get("tool_name") if step_obj else None)
                                )
                                if tool_name:
                                    mapped_name = map_tool_name(str(tool_name))
                                    log_debug(f"Captured tool event: {mapped_name}")
                                    await send_chunk({
                                        "sessionId": session_id,
                                        "type": "tool",
                                        "tool": mapped_name,
                                        "output": ""
                                    })

                                # 2. Thinking logs
                                thought_text = (
                                    event_data.get("thought") or
                                    event_data.get("thinking") or
                                    event_data.get("reasoning_content") or
                                    step_obj.get("thought") or
                                    step_obj.get("thinking") or
                                    (step_obj.get("content") if step_obj.get("step_type") in ["thought", "thinking"] else None)
                                )
                                if thought_text:
                                    await send_chunk({
                                        "sessionId": session_id,
                                        "type": "thinking",
                                        "tool": "",
                                        "output": str(thought_text)
                                    })

                                # 3. Live response streaming
                                chunk_text = (
                                    event_data.get("content") or
                                    event_data.get("text") or
                                    event_data.get("chunk") or
                                    (step_obj.get("content") if step_obj.get("step_type") in ["text", "chunk", "message"] else None)
                                )
                                if chunk_text:
                                    await send_chunk({
                                        "sessionId": session_id,
                                        "type": "chat",
                                        "tool": "",
                                        "output": str(chunk_text)
                                    })

                                # 4. Final result
                                res_val = event_data.get("result") or event_data.get("response")
                                if res_val:
                                    res_str = clean_response_text(res_val)
                                    if res_str:
                                        await send_chunk({
                                            "sessionId": session_id,
                                            "type": "result",
                                            "tool": "",
                                            "output": res_str
                                        })

                                # 5. Usage metrics
                                usage_obj = (
                                    event_data.get("usage") or
                                    step_obj.get("usage") or
                                    (res_val.get("usage") if isinstance(res_val, dict) else None)
                                )
                                if usage_obj:
                                    await send_chunk({
                                        "sessionId": session_id,
                                        "type": "usage",
                                        "tool": "",
                                        "output": json.dumps(usage_obj)
                                    })

                        except json.JSONDecodeError:
                            if raw_str.startswith("{") and raw_str.endswith("}"):
                                pass
                            else:
                                if not any(raw_str.startswith(p) for p in ["[stdbuf", "agy", "WARNING", "INFO"]):
                                    await send_chunk({
                                        "sessionId": session_id,
                                        "type": "chat",
                                        "tool": "",
                                        "output": raw_str + "\n"
                                    })
                        except Exception as parse_e:
                            log_debug(f"JSON parse error in STDOUT reader: {parse_e}")

            async def read_stderr():
                decoder = codecs.getincrementaldecoder('utf-8')(errors='replace')
                buffer = ""
                while True:
                    chunk = await proc.stderr.read(4096)
                    if not chunk:
                        log_debug("STDERR reached EOF")
                        break
                    text = decoder.decode(chunk, final=False)
                    buffer += text
                    while "\n" in buffer:
                        line, buffer = buffer.split("\n", 1)
                        err_str = line.strip()
                        if err_str:
                            log_debug(f"STDERR raw line: {err_str}")

            await asyncio.gather(read_stdout(), read_stderr())
            await proc.wait()

            log_debug(f"agy process exited with code {proc.returncode}")

            has_response = False
            if session_id in active_sessions:
                s = active_sessions[session_id]
                has_response = bool(s.get("finalResponse") or s.get("chat"))

            if proc.returncode != 0 and proc.returncode != -9 and not has_response:
                if session_id in active_sessions:
                    active_sessions[session_id]["status"] = "error"
                    active_sessions[session_id]["isRunning"] = False
                    save_sessions_cache()
                await send_chunk({
                    "sessionId": session_id,
                    "type": "fatal_error",
                    "tool": "",
                    "output": f"agy process terminated unexpectedly (exit code {proc.returncode})"
                })
            else:
                if session_id in active_sessions:
                    s = active_sessions[session_id]
                    s["status"] = "completed"
                    s["isRunning"] = False
                    if not s.get("finalResponse") and s.get("chat"):
                        s["finalResponse"] = "".join(s["chat"])
                    save_sessions_cache()

                await send_chunk({
                    "sessionId": session_id,
                    "type": "completed",
                    "tool": "",
                    "output": ""
                })

    except Exception as e:
        log_debug(f"Exception in handle_chat_stream: {e}")
        if session_id in active_sessions:
            active_sessions[session_id]["status"] = "error"
            active_sessions[session_id]["isRunning"] = False
            save_sessions_cache()
        await send_chunk({
            "sessionId": session_id,
            "type": "fatal_error",
            "tool": "",
            "output": str(e)
        })
    finally:
        running_processes.pop(session_id, None)
        try:
            writer.write(b"0\r\n\r\n")
            await writer.drain()
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass

async def handle_client(reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
    try:
        request_line = await reader.readline()
        if not request_line:
            writer.close()
            return

        parts = request_line.decode('utf-8', errors='replace').split()
        if len(parts) < 2:
            writer.close()
            return

        method, full_path = parts[0], parts[1]
        path = full_path.split("?", 1)[0]
        query_str = full_path.split("?", 1)[1] if "?" in full_path else ""

        content_length = 0
        is_chunked = False
        while True:
            hline = await reader.readline()
            if not hline or hline in (b"\r\n", b"\n"):
                break
            header_str = hline.decode('utf-8', errors='replace')
            if ":" in header_str:
                k, v = header_str.split(":", 1)
                hk = k.strip().lower()
                hv = v.strip()
                if hk == "content-length":
                    try:
                        content_length = int(hv)
                    except ValueError:
                        content_length = 0
                elif hk == "transfer-encoding" and "chunked" in hv.lower():
                    is_chunked = True

        body_data = b""
        if content_length > 0:
            body_data = await reader.readexactly(content_length)
        elif is_chunked:
            while True:
                chunk_len_line = await reader.readline()
                if not chunk_len_line:
                    break
                chunk_len_str = chunk_len_line.decode('utf-8', errors='replace').strip()
                if not chunk_len_str:
                    continue
                try:
                    chunk_len = int(chunk_len_str, 16)
                except ValueError:
                    break
                if chunk_len == 0:
                    await reader.readline()
                    break
                chunk = await reader.readexactly(chunk_len)
                body_data += chunk
                await reader.readline()

        req_json = {}
        if body_data:
            try:
                req_json = json.loads(body_data.decode('utf-8'))
            except Exception:
                pass

        if path == "/health":
            resp_body = json.dumps({"status": "ok", "service": "antigravity-proxy-http"}).encode('utf-8')
            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        if path in ("/api/quota", "/quota"):
            q_res = get_language_server_quota()
            resp_body = json.dumps(q_res, ensure_ascii=False).encode('utf-8')
            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        if path == "/api/projects":
            projects = []
            if os.path.exists(WORKSPACE_BASE):
                try:
                    for name in os.listdir(WORKSPACE_BASE):
                        full = os.path.join(WORKSPACE_BASE, name)
                        if os.path.isdir(full):
                            projects.append(name)
                except Exception as e:
                    log_debug(f"Error listing projects: {e}")
            resp_body = json.dumps({"projects": sorted(projects)}, ensure_ascii=False).encode('utf-8')
            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        # Query single session status for bot recovery
        if method == "GET" and (path == "/api/session" or path.startswith("/api/session/")):
            target_sid = ""
            if path.startswith("/api/session/"):
                target_sid = path[len("/api/session/"):]
            elif query_str:
                for param in query_str.split("&"):
                    if param.startswith("sessionId="):
                        target_sid = param.split("=", 1)[1]
                        break

            target_sid = target_sid.strip()
            sess = active_sessions.get(target_sid)
            if sess:
                resp_body = json.dumps(sess, ensure_ascii=False).encode('utf-8')
                header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
                writer.write(header + resp_body)
                await writer.drain()
                writer.close()
                return
            else:
                resp_body = json.dumps({"status": "not_found", "sessionId": target_sid}).encode('utf-8')
                header = f"HTTP/1.1 404 Not Found\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
                writer.write(header + resp_body)
                await writer.drain()
                writer.close()
                return

        # List all sessions
        if method == "GET" and path == "/api/sessions":
            all_list = list(active_sessions.values())
            resp_body = json.dumps({"sessions": all_list}, ensure_ascii=False).encode('utf-8')
            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        if method == "POST" and path == "/api/chat":
            await handle_chat_stream(reader, writer, req_json)
            return

        if method == "POST" and path == "/api/compact":
            session_id = "".join(c for c in str(req_json.get("sessionId", "")).strip() if c.isalnum() or c in ("-", "_", ":"))
            project_id = "".join(c for c in str(req_json.get("projectId", "")).strip() if c.isalnum() or c in ("-", "_")) or "default"
            workspace_dir = get_workspace_dir(project_id)
            candidate_paths = ["/home/fedora/.local/bin/agy", "/usr/local/bin/agy", "/usr/bin/agy", "agy"]
            agy_bin = "agy"
            for p in candidate_paths:
                if os.path.exists(p) and os.access(p, os.X_OK):
                    agy_bin = p
                    break

            log_debug(f"Handling compact request for session {session_id}, project {project_id}")

            # 1. Ask current conversation to generate a high-density summary
            compact_prompt = (
                "[System Command: CONTEXT COMPRESSION & COMPACT]\n"
                "Please generate a comprehensive, high-density summary of all work done in this project so far:\n"
                "1. Key goals achieved and architecture decisions made\n"
                "2. Files modified/created and their current roles\n"
                "3. Known issues, current state, and pending next steps\n"
                "Format cleanly in Markdown under '# 🗜️ Project Compact Context'."
            )
            cmd1 = [agy_bin, "--print", compact_prompt, "--output-format", "json", "--mode", "accept-edits", "--dangerously-skip-permissions", "--add-dir", workspace_dir]
            if session_id in agy_conversation_map:
                cmd1.extend(["--conversation", agy_conversation_map[session_id]])

            env = os.environ.copy()
            env["HOME"] = "/home/fedora"
            env["USER"] = "fedora"

            summary_text = ""
            try:
                proc1 = await asyncio.create_subprocess_exec(*cmd1, cwd=workspace_dir, env=env, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
                stdout1, _ = await proc1.communicate()
                if stdout1:
                    try:
                        res1 = json.loads(stdout1.decode('utf-8'))
                        summary_text = res1.get("response") or res1.get("result", {}).get("response", "")
                    except Exception:
                        summary_text = stdout1.decode('utf-8').strip()
            except Exception as e1:
                log_debug(f"Failed to generate compact summary: {e1}")

            if not summary_text:
                summary_text = f"## 🗜️ Project Compact Context ({project_id})\n- Context compressed at {time.strftime('%Y-%m-%d %H:%M:%S')}.\n- Project workspace: `{workspace_dir}`"

            # 2. Reset conversation mapping and start a new clean session seeded with the compact summary
            if session_id in agy_conversation_map:
                agy_conversation_map.pop(session_id, None)
                save_conv_map()

            init_prompt = f"[System Context: Previous Project History Summary]\n{summary_text}\n\n[System Instruction: You have loaded the compressed context. Acknowledge briefly that you are ready.]"
            cmd2 = [agy_bin, "--print", init_prompt, "--output-format", "json", "--mode", "accept-edits", "--dangerously-skip-permissions", "--add-dir", workspace_dir, "--project", project_id]
            try:
                proc2 = await asyncio.create_subprocess_exec(*cmd2, cwd=workspace_dir, env=env, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
                stdout2, _ = await proc2.communicate()
                if stdout2:
                    try:
                        res2 = json.loads(stdout2.decode('utf-8'))
                        new_uuid = res2.get("conversation_id") or res2.get("result", {}).get("conversation_id")
                        if new_uuid:
                            agy_conversation_map[session_id] = str(new_uuid).strip()
                            save_conv_map()
                            log_debug(f"Compact successful. New session mapped: {session_id} -> {new_uuid}")
                    except Exception:
                        pass
            except Exception as e2:
                log_debug(f"Failed to seed new compact session: {e2}")

            # 3. Update session cache
            if session_id in active_sessions:
                s = active_sessions[session_id]
                s["status"] = "completed"
                s["finalResponse"] = summary_text
                s["history"] = [{
                    "turnId": f"turn_compact_{int(time.time()*1000)}",
                    "prompt": "/compact (대화 맥락 압축 및 요약)",
                    "status": "completed",
                    "finalResponse": summary_text,
                    "startedAt": time.time(),
                    "updatedAt": time.time()
                }]
                save_sessions_cache()

            resp_body = json.dumps({"status": "compacted", "sessionId": session_id, "summary": summary_text}, ensure_ascii=False).encode('utf-8')
            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        if method == "POST" and path == "/api/cancel":
            session_id = str(req_json.get("sessionId", "")).strip()
            if session_id in running_processes:
                proc = running_processes.pop(session_id, None)
                if proc:
                    try:
                        pgid = os.getpgid(proc.pid)
                        os.killpg(pgid, signal.SIGTERM)
                    except Exception:
                        try:
                            proc.terminate()
                        except Exception:
                            pass
            if session_id in active_sessions:
                active_sessions[session_id]["status"] = "cancelled"
                active_sessions[session_id]["isRunning"] = False
                save_sessions_cache()
            resp_body = json.dumps({"status": "cancelled"}).encode('utf-8')
            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        if method == "POST" and path == "/api/session/rename":
            session_id = str(req_json.get("sessionId") or req_json.get("session_id") or req_json.get("id") or "").strip()
            new_title = str(req_json.get("title", "")).strip()
            found_key = None
            if session_id in active_sessions:
                found_key = session_id
            else:
                for k in active_sessions:
                    if k.lower() == session_id.lower():
                        found_key = k
                        break

            if found_key and new_title:
                active_sessions[found_key]["title"] = new_title
                active_sessions[found_key]["isCustomTitle"] = True
                save_sessions_cache()
                log_debug(f"Renamed session {found_key} -> '{new_title}'")
                resp_body = json.dumps({"status": "renamed", "sessionId": found_key, "title": new_title}, ensure_ascii=False).encode('utf-8')
            elif new_title and session_id:
                active_sessions[session_id] = {
                    "sessionId": session_id,
                    "projectId": "default",
                    "model": "gemini-3.8-flash-high",
                    "title": new_title,
                    "isCustomTitle": True,
                    "prompt": "",
                    "files": [],
                    "turnId": "",
                    "status": "idle",
                    "tools": [],
                    "thinking": [],
                    "chat": [],
                    "finalResponse": "",
                    "errorMessage": "",
                    "startedAt": time.time(),
                    "updatedAt": time.time(),
                    "history": [],
                    "isRunning": False
                }
                save_sessions_cache()
                resp_body = json.dumps({"status": "renamed", "sessionId": session_id, "title": new_title}, ensure_ascii=False).encode('utf-8')
            else:
                resp_body = json.dumps({"status": "error", "message": "Session not found or title empty"}).encode('utf-8')

            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        if method == "POST" and path == "/api/session/create":
            p_id = "".join(c for c in str(req_json.get("projectId", "ai-agent")).strip() if c.isalnum() or c in ("-", "_")) or "ai-agent"
            s_id = "".join(c for c in str(req_json.get("sessionId", "")).strip() if c.isalnum() or c in ("-", "_", ":"))
            if not s_id:
                s_id = f"ag_sess_{int(time.time()*1000)}"
            m_id = str(req_json.get("model", "")).strip() or "gemini-3.8-flash-high"
            custom_title = str(req_json.get("title", "")).strip() or "새 세션"

            active_sessions[s_id] = {
                "sessionId": s_id,
                "projectId": p_id,
                "model": m_id,
                "title": custom_title,
                "prompt": "",
                "files": [],
                "turnId": "",
                "status": "idle",
                "tools": [],
                "thinking": [],
                "chat": [],
                "finalResponse": "",
                "errorMessage": "",
                "startedAt": time.time(),
                "updatedAt": time.time(),
                "history": [],
                "isRunning": False
            }
            save_sessions_cache()
            resp_body = json.dumps({"success": True, "sessionId": s_id, "session": active_sessions[s_id]}, ensure_ascii=False).encode('utf-8')
            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        if method == "POST" and (path == "/api/session/delete" or path == "/api/clear"):
            session_id = str(req_json.get("sessionId") or req_json.get("session_id") or req_json.get("id") or "").strip()
            if session_id in running_processes:
                proc = running_processes.pop(session_id, None)
                if proc:
                    try:
                        pgid = os.getpgid(proc.pid)
                        os.killpg(pgid, signal.SIGTERM)
                    except Exception:
                        try:
                            proc.terminate()
                        except Exception:
                            pass
            if session_id in agy_conversation_map:
                agy_conversation_map.pop(session_id, None)
                save_conv_map()
                log_debug(f"Cleared session mapping for {session_id}")

            # Pop by exact key or case-insensitive match
            matching_keys = [k for k in list(active_sessions.keys()) if k == session_id or (session_id and k.lower() == session_id.lower())]
            for k in matching_keys:
                active_sessions.pop(k, None)
            save_sessions_cache()

            resp_body = json.dumps({"status": "deleted", "sessionId": session_id}).encode('utf-8')
            header = f"HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {len(resp_body)}\r\n\r\n".encode('utf-8')
            writer.write(header + resp_body)
            await writer.drain()
            writer.close()
            return

        writer.write(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
        await writer.drain()
        writer.close()
    except Exception as e:
        log_debug(f"HTTP handler error: {e}")
        try:
            writer.close()
        except Exception:
            pass

async def main():
    port = int(os.environ.get("PORT", 8090))
    server = await asyncio.start_server(handle_client, "0.0.0.0", port)
    log_debug(f"HTTP Streaming REST Server listening on 0.0.0.0:{port}")
    async with server:
        await server.serve_forever()

if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass
