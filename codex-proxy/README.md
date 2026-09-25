# Codex Proxy

Local HTTP bridge for the Discord bot and shared Antigravity/Codex web console.
It keeps one `codex app-server` process connected over stdio JSON-RPC and exposes
the same REST/streaming contract as `antigravity-proxy` on port `8091`.

```bash
python3 main.py
curl http://127.0.0.1:8091/health
```

The host must already be authenticated with `codex login`. For the user-level
service used by this repository:

```bash
systemctl --user link ./codex-proxy.service
systemctl --user enable --now codex-proxy.service
```

The bridge supports chat streaming, persistent sessions, model selection,
reasoning effort, attachments, cancel, approval, compact, rename, and delete.
By default, Codex runs in YOLO mode with `approvalPolicy=never` and
`sandbox=danger-full-access`; unexpected approval requests are accepted
automatically instead of being surfaced to Discord or the web console.

Environment variables:

- `CODEX_PROXY_HOST` (default `127.0.0.1`)
- `CODEX_PROXY_PORT` (default `8091`)
- `CODEX_BIN` (default `codex`)
- `CODEX_WORKSPACE_BASE` (default `/mnt/antigravity_workspaces`)
- `CODEX_PROXY_STATE` (default `/tmp/codex-proxy-sessions.json`)
- `CODEX_YOLO` (default `true`; set to `false` to restore interactive approval with `workspace-write`)
