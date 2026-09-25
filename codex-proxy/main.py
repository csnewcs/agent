#!/usr/bin/env python3
"""HTTP bridge between the Discord/web clients and `codex app-server`.

The public HTTP contract intentionally mirrors antigravity-proxy so both
providers can share the same Go and browser presentation layers.
"""

from __future__ import annotations

import asyncio
import base64
import json
import mimetypes
import os
import queue
import re
import signal
import subprocess
import threading
import time
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


HOST = os.getenv("CODEX_PROXY_HOST", "127.0.0.1")
PORT = int(os.getenv("CODEX_PROXY_PORT", "8091"))
WORKSPACE_BASE = Path(os.getenv("CODEX_WORKSPACE_BASE", "/mnt/antigravity_workspaces"))
STATE_FILE = Path(os.getenv("CODEX_PROXY_STATE", "/tmp/codex-proxy-sessions.json"))
CODEX_BIN = os.getenv("CODEX_BIN", "codex")
CODEX_YOLO = os.getenv("CODEX_YOLO", "true").strip().lower() not in ("0", "false", "no", "off")
CODEX_APPROVAL_POLICY = "never" if CODEX_YOLO else "on-request"
CODEX_SANDBOX_MODE = "danger-full-access" if CODEX_YOLO else "workspace-write"


def safe_id(value: Any, fallback: str) -> str:
    cleaned = re.sub(r"[^a-zA-Z0-9_:-]", "", str(value or "").strip())
    return cleaned or fallback


def safe_project(value: Any) -> str:
    cleaned = re.sub(r"[^a-zA-Z0-9_-]", "", str(value or "").strip())
    return cleaned or "ai-agent"


def workspace_for(project_id: str) -> Path:
    base = WORKSPACE_BASE.absolute()
    candidate = (base / safe_project(project_id)).absolute()
    if os.path.commonpath((str(base), str(candidate))) != str(base):
        raise ValueError("invalid project path")
    candidate.mkdir(parents=True, exist_ok=True)
    return candidate


@dataclass
class Session:
    session_id: str
    project_id: str
    thread_id: str = ""
    turn_id: str = ""
    model: str = ""
    effort: str = ""
    prompt: str = ""
    title: str = ""
    status: str = "idle"
    tools: list[str] = field(default_factory=list)
    thinking: list[str] = field(default_factory=list)
    chat: list[str] = field(default_factory=list)
    final_response: str = ""
    last_unphased_message: str = field(default="", repr=False)
    error_message: str = ""
    usage: dict[str, int] = field(default_factory=dict)
    started_at: float = field(default_factory=time.time)
    updated_at: float = field(default_factory=time.time)
    history: list[dict[str, Any]] = field(default_factory=list)
    pending_approval: dict[str, Any] | None = None
    subscribers: list[queue.Queue] = field(default_factory=list, repr=False)
    seen_file_changes: set[str] = field(default_factory=set, repr=False)

    def public(self) -> dict[str, Any]:
        return {
            "sessionId": self.session_id,
            "projectId": self.project_id,
            "threadId": self.thread_id,
            "turnId": self.turn_id,
            "model": self.model,
            "effort": self.effort,
            "prompt": self.prompt,
            "title": self.title or self.prompt,
            "status": self.status,
            "tools": self.tools,
            "thinking": self.thinking,
            "chat": self.chat,
            "finalResponse": self.final_response,
            "errorMessage": self.error_message,
            "isRunning": self.status in ("running", "waiting_approval"),
            "usage": self.usage,
            "startedAt": self.started_at,
            "updatedAt": self.updated_at,
            "history": self.history,
            "pendingApproval": self.pending_approval,
        }


sessions: dict[str, Session] = {}
sessions_lock = threading.RLock()


def load_state() -> None:
    try:
        raw = json.loads(STATE_FILE.read_text(encoding="utf-8"))
    except Exception:
        return
    for item in raw if isinstance(raw, list) else []:
        try:
            sess = Session(
                session_id=item["sessionId"],
                project_id=item.get("projectId", "ai-agent"),
                thread_id=item.get("threadId", ""),
                turn_id=item.get("turnId", ""),
                model=item.get("model", ""),
                effort=item.get("effort", ""),
                prompt=item.get("prompt", ""),
                title=item.get("title", ""),
                status="interrupted" if item.get("isRunning") else item.get("status", "idle"),
                tools=list(item.get("tools", [])),
                thinking=list(item.get("thinking", [])),
                chat=list(item.get("chat", [])),
                final_response=item.get("finalResponse", ""),
                error_message=item.get("errorMessage", ""),
                usage=dict(item.get("usage", {})),
                started_at=float(item.get("startedAt", time.time())),
                updated_at=float(item.get("updatedAt", time.time())),
                history=list(item.get("history", [])),
            )
            sessions[sess.session_id] = sess
        except Exception:
            continue


def save_state() -> None:
    with sessions_lock:
        payload = [s.public() for s in sessions.values()]
    try:
        STATE_FILE.parent.mkdir(parents=True, exist_ok=True)
        tmp = STATE_FILE.with_suffix(".tmp")
        tmp.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
        tmp.replace(STATE_FILE)
    except Exception:
        pass


class CodexAppServer:
    def __init__(self) -> None:
        self.proc: subprocess.Popen[str] | None = None
        self.write_lock = threading.Lock()
        self.pending: dict[int, queue.Queue] = {}
        self.pending_lock = threading.Lock()
        self.next_id = 1
        self.reader: threading.Thread | None = None
        self.stderr_reader: threading.Thread | None = None

    def start(self) -> None:
        if self.proc and self.proc.poll() is None:
            return
        self.proc = subprocess.Popen(
            [CODEX_BIN, "app-server"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
        )
        self.reader = threading.Thread(target=self._read_loop, daemon=True)
        self.reader.start()
        self.stderr_reader = threading.Thread(target=self._stderr_loop, daemon=True)
        self.stderr_reader.start()
        self.rpc(
            "initialize",
            {
                "clientInfo": {
                    "name": "csnewcs_ai_agent",
                    "title": "AI Agent Discord and Web",
                    "version": "1.0.0",
                },
                "capabilities": {"experimentalApi": False},
            },
            timeout=15,
        )
        self.notify("initialized", {})

    def stop(self) -> None:
        if self.proc and self.proc.poll() is None:
            self.proc.terminate()

    def _stderr_loop(self) -> None:
        assert self.proc and self.proc.stderr
        for line in self.proc.stderr:
            if line.strip():
                print(f"[codex-app-server] {line.rstrip()}", flush=True)

    def _read_loop(self) -> None:
        assert self.proc and self.proc.stdout
        for line in self.proc.stdout:
            try:
                message = json.loads(line)
            except Exception:
                continue
            if "id" in message and ("result" in message or "error" in message):
                with self.pending_lock:
                    waiter = self.pending.pop(message["id"], None)
                if waiter:
                    waiter.put(message)
                continue
            if "id" in message and "method" in message:
                self._handle_server_request(message)
                continue
            if "method" in message:
                self._handle_notification(message["method"], message.get("params") or {})

    def send(self, message: dict[str, Any]) -> None:
        self.start_if_needed()
        assert self.proc and self.proc.stdin
        with self.write_lock:
            self.proc.stdin.write(json.dumps(message, ensure_ascii=False) + "\n")
            self.proc.stdin.flush()

    def start_if_needed(self) -> None:
        if not self.proc or self.proc.poll() is not None:
            self.start()

    def rpc(self, method: str, params: dict[str, Any], timeout: float = 30) -> dict[str, Any]:
        if method != "initialize":
            self.start_if_needed()
        with self.pending_lock:
            request_id = self.next_id
            self.next_id += 1
            waiter: queue.Queue = queue.Queue(maxsize=1)
            self.pending[request_id] = waiter
        self.send({"method": method, "id": request_id, "params": params})
        try:
            response = waiter.get(timeout=timeout)
        except queue.Empty as exc:
            with self.pending_lock:
                self.pending.pop(request_id, None)
            raise TimeoutError(f"Codex request timed out: {method}") from exc
        if response.get("error"):
            raise RuntimeError(response["error"].get("message", str(response["error"])))
        return response.get("result") or {}

    def notify(self, method: str, params: dict[str, Any]) -> None:
        self.send({"method": method, "params": params})

    def resolve_approval(self, approval: dict[str, Any], decision: str) -> None:
        request_id = approval["requestId"]
        if approval.get("method") == "item/permissions/requestApproval":
            granted = approval.get("permissions") if decision in ("accept", "acceptForSession") else {}
            result = {
                "permissions": granted or {},
                "scope": "session" if decision == "acceptForSession" else "turn",
            }
        else:
            result = {"decision": decision}
        self.send({"id": request_id, "result": result})

    def _find_session(self, thread_id: str) -> Session | None:
        with sessions_lock:
            return next((s for s in sessions.values() if s.thread_id == thread_id), None)

    def _emit(self, sess: Session, event_type: str, output: str = "", tool: str = "", **extra: Any) -> None:
        event = {"sessionId": sess.session_id, "type": event_type, "output": output, "tool": tool, **extra}
        dead: list[queue.Queue] = []
        for subscriber in list(sess.subscribers):
            try:
                subscriber.put_nowait(event)
            except Exception:
                dead.append(subscriber)
        for subscriber in dead:
            if subscriber in sess.subscribers:
                sess.subscribers.remove(subscriber)

    def _handle_server_request(self, message: dict[str, Any]) -> None:
        method = message.get("method", "")
        params = message.get("params") or {}
        thread_id = params.get("threadId", "")
        sess = self._find_session(thread_id)
        if not sess:
            self.send({"id": message["id"], "error": {"code": -32000, "message": "Unknown Codex thread"}})
            return
        if method == "item/tool/requestUserInput":
            answers = {str(question.get("id", "")): {"answers": []} for question in params.get("questions", [])}
            self.send({"id": message["id"], "result": {"answers": answers}})
            self._emit(sess, "info", "Codex user-input request was returned without an answer.")
            return
        if method == "mcpServer/elicitation/request":
            self.send({"id": message["id"], "result": {"action": "decline"}})
            self._emit(sess, "info", "MCP elicitation request was declined.")
            return
        approval_methods = {
            "item/commandExecution/requestApproval",
            "item/fileChange/requestApproval",
            "item/permissions/requestApproval",
        }
        if method not in approval_methods:
            self.send({"id": message["id"], "error": {"code": -32601, "message": f"Unsupported client request: {method}"}})
            return
        approval = {
            "requestId": message["id"],
            "method": method,
            "itemId": params.get("itemId", ""),
            "reason": params.get("reason", ""),
            "command": params.get("command", ""),
            "cwd": params.get("cwd", ""),
            "permissions": params.get("permissions"),
        }
        if CODEX_YOLO:
            self.resolve_approval(approval, "acceptForSession")
            text = approval["reason"] or approval["command"] or method
            self._emit(sess, "info", f"YOLO 모드에서 자동 승인됨: {text}")
            return
        sess.pending_approval = approval
        sess.status = "waiting_approval"
        sess.updated_at = time.time()
        text = approval["reason"] or approval["command"] or method
        self._emit(sess, "permission_request", str(text), requestId=message["id"], approvalMethod=method)
        save_state()

    def _handle_notification(self, method: str, params: dict[str, Any]) -> None:
        thread_id = params.get("threadId", "")
        sess = self._find_session(thread_id)
        if not sess:
            return
        sess.updated_at = time.time()
        if method == "turn/started":
            turn = params.get("turn") or {}
            sess.turn_id = turn.get("id", sess.turn_id)
            sess.status = "running"
            self._emit(sess, "info", "Codex 작업을 시작했습니다.")
        elif method == "item/agentMessage/delta":
            delta = str(params.get("delta", ""))
            if delta:
                sess.chat.append(delta)
                self._emit(sess, "chat", delta)
        elif method == "item/reasoning/summaryTextDelta":
            delta = str(params.get("delta", ""))
            if delta:
                sess.thinking.append(delta)
                self._emit(sess, "thinking", delta)
        elif method in ("item/started", "item/completed"):
            item = params.get("item") or {}
            item_type = item.get("type", "")
            if item_type == "commandExecution":
                command = item.get("command", "")
                if isinstance(command, list):
                    command = " ".join(map(str, command))
                is_new_tool = bool(command and command not in sess.tools)
                if is_new_tool:
                    sess.tools.append(str(command))
                    self._emit(sess, "tool", item.get("aggregatedOutput", ""), tool=str(command))
            elif item_type == "fileChange":
                item_id = str(item.get("id", ""))
                if (item_id and item_id not in sess.seen_file_changes) or (not item_id and method == "item/started"):
                    if item_id:
                        sess.seen_file_changes.add(item_id)
                    sess.tools.append("apply_patch")
                    self._emit(sess, "tool", "파일 변경", tool="apply_patch")
            elif item_type == "mcpToolCall":
                tool = str(item.get("tool", item.get("server", "MCP")))
                if tool not in sess.tools:
                    sess.tools.append(tool)
                self._emit(sess, "tool", "", tool=tool)
            elif item_type == "agentMessage" and method == "item/completed":
                text = str(item.get("text", ""))
                if text:
                    if item.get("phase") == "final_answer":
                        sess.final_response = text
                    elif item.get("phase") is None:
                        # Older providers may omit phase; use their last completed
                        # message only if no explicitly marked final answer arrives.
                        sess.last_unphased_message = text
        elif method == "thread/tokenUsage/updated":
            usage = ((params.get("tokenUsage") or {}).get("total") or {})
            sess.usage = {
                "input_tokens": int(usage.get("inputTokens", 0)),
                "output_tokens": int(usage.get("outputTokens", 0)),
                "thinking_tokens": int(usage.get("reasoningOutputTokens", 0)),
                "cache_read_tokens": int(usage.get("cachedInputTokens", 0)),
                "total_tokens": int(usage.get("totalTokens", 0)),
            }
            self._emit(sess, "usage", json.dumps(sess.usage))
        elif method == "serverRequest/resolved":
            sess.pending_approval = None
            if sess.status == "waiting_approval":
                sess.status = "running"
        elif method == "turn/completed":
            turn = params.get("turn") or {}
            status = str(turn.get("status", "completed"))
            sess.status = "cancelled" if status in ("interrupted", "cancelled") else ("error" if status == "failed" else "completed")
            error = turn.get("error")
            if error:
                sess.error_message = str(error.get("message", error)) if isinstance(error, dict) else str(error)
            if not sess.final_response:
                sess.final_response = sess.last_unphased_message or "".join(sess.chat).strip()
            event_type = "fatal_error" if sess.status == "error" else ("cancelled" if sess.status == "cancelled" else "completed")
            self._emit(sess, event_type, sess.final_response if event_type == "completed" else sess.error_message)
        elif method == "error":
            error = params.get("error") or params
            sess.error_message = str(error.get("message", error)) if isinstance(error, dict) else str(error)
            self._emit(sess, "error", sess.error_message)
        save_state()


app = CodexAppServer()


def codex_quota_payload() -> dict[str, Any]:
    result = app.rpc("account/rateLimits/read", {})
    rate_limits = result.get("rateLimits") or {}
    buckets = result.get("rateLimitsByLimitId") or {}
    if not buckets and rate_limits:
        buckets = {str(rate_limits.get("limitId") or "codex"): rate_limits}

    quota_groups: list[dict[str, Any]] = []
    reset_times: list[str] = []
    plan_type = str(rate_limits.get("planType") or "").strip()
    for limit_id, bucket in buckets.items():
        limit_name = str(bucket.get("limitName") or limit_id or "Codex")
        if not plan_type:
            plan_type = str(bucket.get("planType") or "").strip()
        for window_name in ("primary", "secondary"):
            window = bucket.get(window_name)
            if not isinstance(window, dict):
                continue
            used_percent = max(0.0, min(100.0, float(window.get("usedPercent", 0))))
            duration_mins = int(window.get("windowDurationMins", 0) or 0)
            resets_at = int(window.get("resetsAt", 0) or 0)
            reset_time = datetime.fromtimestamp(resets_at, timezone.utc).isoformat().replace("+00:00", "Z") if resets_at else ""
            if reset_time:
                reset_times.append(reset_time)
            if duration_mins == 300:
                title = "5시간 사용 한도"
            elif duration_mins == 10080:
                title = "주간 사용 한도"
            elif duration_mins and duration_mins % 1440 == 0:
                title = f"{duration_mins // 1440}일 사용 한도"
            elif duration_mins and duration_mins % 60 == 0:
                title = f"{duration_mins // 60}시간 사용 한도"
            else:
                title = f"{duration_mins}분 사용 한도" if duration_mins else f"{window_name.title()} 한도"
            quota_groups.append({
                "id": f"{limit_id}_{window_name}",
                "title": title,
                "subtitle": f"{limit_name} · {used_percent:.1f}% 사용",
                "family": "Codex",
                "remainingPercent": round(100.0 - used_percent, 1),
                "usedPercent": round(used_percent, 1),
                "resetTime": reset_time,
                "windowDurationMins": duration_mins,
                "modelList": [str(bucket.get("normalModelSlug") or "Codex")],
            })

    credits = rate_limits.get("credits") or {}
    reset_credits = result.get("rateLimitResetCredits") or {}
    display_plan = f"ChatGPT {plan_type.title()}" if plan_type else "ChatGPT"
    return {
        "success": True,
        "provider": "codex",
        "codex": {
            "userTier": {"name": display_plan},
            "planInfo": {"planName": plan_type or "ChatGPT"},
            "quotaGroups": quota_groups,
            "models": sorted({model for group in quota_groups for model in group["modelList"]}),
            "earliestResetTime": min(reset_times) if reset_times else "",
            "ordinaryUsageAllowed": bool(result.get("ordinaryUsageAllowed", True)),
            "credits": {
                "hasCredits": bool(credits.get("hasCredits", False)),
                "unlimited": bool(credits.get("unlimited", False)),
                "balance": str(credits.get("balance", "0")),
            },
            "rateLimitResetCredits": {"availableCount": int(reset_credits.get("availableCount", 0) or 0)},
        },
    }


def start_turn(data: dict[str, Any], subscriber: queue.Queue | None = None) -> Session:
    session_id = safe_id(data.get("sessionId"), "codex_default")
    prompt = str(data.get("prompt", "")).strip()
    with sessions_lock:
        sess = sessions.get(session_id)
        project_id = safe_project(data.get("projectId") or (sess.project_id if sess else None))
        model = str(data["model"]).strip() if "model" in data else (sess.model if sess else "")
        effort = str(data["effort"]).strip() if "effort" in data else (sess.effort if sess else "")
        cwd = str(workspace_for(project_id))
        if not sess:
            sess = Session(session_id=session_id, project_id=project_id)
            sessions[session_id] = sess
        if sess.prompt:
            sess.history.append({
                "turnId": sess.turn_id,
                "prompt": sess.prompt,
                "status": sess.status,
                "tools": list(sess.tools),
                "thinking": list(sess.thinking),
                "chat": list(sess.chat),
                "finalResponse": sess.final_response,
                "errorMessage": sess.error_message,
                "usage": dict(sess.usage),
                "startedAt": sess.started_at,
                "updatedAt": sess.updated_at,
            })
        sess.project_id = project_id
        sess.prompt = prompt
        sess.title = sess.title or prompt[:80]
        sess.model = model
        sess.effort = effort
        sess.status = "running"
        sess.tools = []
        sess.seen_file_changes.clear()
        sess.thinking = []
        sess.chat = []
        sess.final_response = ""
        sess.last_unphased_message = ""
        sess.error_message = ""
        sess.usage = {}
        sess.started_at = time.time()
        sess.updated_at = sess.started_at
        sess.pending_approval = None
        if subscriber is not None:
            sess.subscribers.append(subscriber)

    if sess.thread_id:
        try:
            app.rpc("thread/resume", {
                "threadId": sess.thread_id,
                "cwd": cwd,
                "approvalPolicy": CODEX_APPROVAL_POLICY,
                "sandbox": CODEX_SANDBOX_MODE,
            })
        except Exception:
            sess.thread_id = ""
    if not sess.thread_id:
        params: dict[str, Any] = {
            "cwd": cwd,
            "approvalPolicy": CODEX_APPROVAL_POLICY,
            "sandbox": CODEX_SANDBOX_MODE,
            "threadSource": "exec",
        }
        if model:
            params["model"] = model
        result = app.rpc("thread/start", params)
        sess.thread_id = (result.get("thread") or {}).get("id", "")
    inputs: list[dict[str, Any]] = [{"type": "text", "text": prompt}]
    uploads = workspace_for(project_id) / "uploads"
    uploads.mkdir(parents=True, exist_ok=True)
    for attachment in data.get("files") or []:
        name = Path(str(attachment.get("name") or "attachment")).name
        target = (uploads / name).resolve()
        if uploads.resolve() not in target.parents:
            continue
        content = attachment.get("content_base64")
        source_path = attachment.get("path")
        url = attachment.get("url")
        if content:
            target.write_bytes(base64.b64decode(content))
        elif source_path and Path(source_path).is_file():
            target.write_bytes(Path(source_path).read_bytes())
        elif url:
            request = urllib.request.Request(str(url), headers={"User-Agent": "CodexProxy/1.0"})
            with urllib.request.urlopen(request, timeout=20) as response:
                target.write_bytes(response.read())
        else:
            continue
        mime = mimetypes.guess_type(target.name)[0] or ""
        if mime.startswith("image/"):
            inputs.append({"type": "localImage", "path": str(target)})
        else:
            inputs[0]["text"] += f"\n\nAttached file: {target}"
    turn_params: dict[str, Any] = {"threadId": sess.thread_id, "input": inputs, "cwd": cwd}
    if model:
        turn_params["model"] = model
    if effort:
        turn_params["effort"] = effort
    result = app.rpc("turn/start", turn_params)
    sess.turn_id = (result.get("turn") or {}).get("id", "")
    save_state()
    return sess


class Handler(BaseHTTPRequestHandler):
    server_version = "CodexProxy/1.0"

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"[codex-proxy] {self.address_string()} {fmt % args}", flush=True)

    def json_body(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", "0") or "0")
        if length <= 0:
            return {}
        try:
            return json.loads(self.rfile.read(length).decode("utf-8"))
        except Exception:
            return {}

    def send_json(self, payload: Any, status: int = 200) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        query = urllib.parse.parse_qs(parsed.query)
        if parsed.path == "/health":
            return self.send_json({
                "status": "ok",
                "service": "codex-proxy",
                "appServer": app.proc is not None and app.proc.poll() is None,
                "yolo": CODEX_YOLO,
                "approvalPolicy": CODEX_APPROVAL_POLICY,
                "sandbox": CODEX_SANDBOX_MODE,
            })
        if parsed.path == "/api/projects":
            projects = sorted(p.name for p in WORKSPACE_BASE.iterdir() if p.is_dir()) if WORKSPACE_BASE.exists() else []
            return self.send_json({"projects": projects})
        if parsed.path == "/api/models":
            try:
                models = app.rpc("model/list", {"limit": 100}).get("data", [])
                return self.send_json({"success": True, "provider": "codex", "models": models})
            except Exception as exc:
                return self.send_json({"success": False, "error": str(exc)}, 502)
        if parsed.path in ("/api/resources", "/api/quota"):
            try:
                return self.send_json(codex_quota_payload())
            except Exception as exc:
                return self.send_json({"success": False, "provider": "codex", "error": str(exc)}, 502)
        if parsed.path == "/api/sessions":
            with sessions_lock:
                data = sorted((s.public() for s in sessions.values()), key=lambda x: x.get("updatedAt", 0), reverse=True)
            return self.send_json({"sessions": data})
        if parsed.path.startswith("/api/session"):
            sid = query.get("sessionId", [""])[0] or parsed.path.removeprefix("/api/session/")
            with sessions_lock:
                sess = sessions.get(sid)
            return self.send_json(sess.public() if sess else {"status": "not_found", "sessionId": sid}, 200 if sess else 404)
        return self.send_json({"error": "not found"}, 404)

    def do_POST(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        data = self.json_body()
        try:
            if parsed.path == "/api/chat":
                subscriber: queue.Queue = queue.Queue(maxsize=512)
                try:
                    sess = start_turn(data, subscriber)
                except Exception:
                    with sessions_lock:
                        for existing in sessions.values():
                            if subscriber in existing.subscribers:
                                existing.subscribers.remove(subscriber)
                    raise
                self.send_response(200)
                self.send_header("Content-Type", "application/x-ndjson; charset=utf-8")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Connection", "close")
                self.end_headers()
                self.wfile.flush()
                try:
                    while True:
                        try:
                            event = subscriber.get(timeout=20)
                        except queue.Empty:
                            event = {"sessionId": sess.session_id, "type": "ping", "output": ""}
                        self.wfile.write((json.dumps(event, ensure_ascii=False) + "\n").encode("utf-8"))
                        self.wfile.flush()
                        if event.get("type") in ("completed", "cancelled", "fatal_error"):
                            break
                except (BrokenPipeError, ConnectionResetError):
                    pass
                finally:
                    if subscriber in sess.subscribers:
                        sess.subscribers.remove(subscriber)
                return
            sid = safe_id(data.get("sessionId"), "")
            with sessions_lock:
                sess = sessions.get(sid)
            if parsed.path == "/api/cancel":
                if not sess or not sess.thread_id or not sess.turn_id:
                    return self.send_json({"status": "not_found"}, 404)
                app.rpc("turn/interrupt", {"threadId": sess.thread_id, "turnId": sess.turn_id})
                return self.send_json({"status": "cancelled", "sessionId": sid})
            if parsed.path == "/api/approval":
                if not sess or not sess.pending_approval:
                    return self.send_json({"status": "not_found"}, 404)
                decision = str(data.get("decision", "decline"))
                if decision not in ("accept", "acceptForSession", "decline", "cancel"):
                    decision = "decline"
                app.resolve_approval(sess.pending_approval, decision)
                sess.pending_approval = None
                sess.status = "running"
                save_state()
                return self.send_json({"status": "resolved", "decision": decision})
            if parsed.path == "/api/compact":
                if not sess or not sess.thread_id:
                    return self.send_json({"status": "not_found"}, 404)
                app.rpc("thread/compact/start", {"threadId": sess.thread_id})
                return self.send_json({"status": "compacting", "sessionId": sid})
            if parsed.path in ("/api/clear", "/api/session/delete"):
                if sess and sess.thread_id:
                    try:
                        app.rpc("thread/delete", {"threadId": sess.thread_id})
                    except Exception:
                        pass
                with sessions_lock:
                    sessions.pop(sid, None)
                save_state()
                return self.send_json({"status": "deleted", "sessionId": sid})
            if parsed.path == "/api/session/rename":
                if not sess or not sess.thread_id:
                    return self.send_json({"status": "not_found"}, 404)
                title = str(data.get("title", "")).strip()[:200]
                app.rpc("thread/name/set", {"threadId": sess.thread_id, "name": title})
                sess.title = title
                save_state()
                return self.send_json({"status": "renamed", "sessionId": sid, "title": title})
            if parsed.path == "/api/session/create":
                project_id = safe_project(data.get("projectId"))
                sid = safe_id(data.get("sessionId"), f"codex_{int(time.time() * 1000)}")
                with sessions_lock:
                    sessions[sid] = Session(session_id=sid, project_id=project_id, title=str(data.get("title", "")))
                save_state()
                return self.send_json({"success": True, "sessionId": sid, "projectId": project_id})
            return self.send_json({"error": "not found"}, 404)
        except Exception as exc:
            return self.send_json({"error": str(exc)}, 500)


def main() -> None:
    load_state()
    app.start()
    server = ThreadingHTTPServer((HOST, PORT), Handler)
    print(f"[codex-proxy] listening on http://{HOST}:{PORT}", flush=True)

    def shutdown(*_: Any) -> None:
        save_state()
        app.stop()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    try:
        server.serve_forever()
    finally:
        save_state()
        app.stop()
        server.server_close()


if __name__ == "__main__":
    main()
