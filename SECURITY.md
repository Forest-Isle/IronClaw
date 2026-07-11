# Security

Daimon can execute tools, read and write files, call HTTP endpoints, run external MCP servers, and delegate work to sub-agents. Treat every runtime deployment as a privileged local agent and configure approvals, sandboxing, and network policy intentionally.

## Security Model

Tool execution flows through this chain when Gateway initializes all security components:

```mermaid
flowchart LR
    Call[Tool call] --> Permission[PermissionInterceptor]
    Permission --> Hook[HookInterceptor]
    Hook --> UserHooks[User hook scripts]
    UserHooks --> Sandbox[SandboxInterceptor]
    Sandbox --> Verify[VerifyInterceptor]
    Verify --> Audit[AuditInterceptor]
    Audit --> Tool[Tool implementation]
```

The chain is configured in `internal/gateway/init_tools.go`.

## Permissions

Permission rules live under `permissions` in YAML config. Actions are:

- `none`: allow silently.
- `notify`: allow and notify/log.
- `approve`: require user approval.
- `deny`: block.

Legacy aliases are accepted: `allow` maps to `none`, and `ask` maps to `approve`.

Rules are evaluated top-to-bottom. If no rule matches, destructive tool capabilities force approval; otherwise the configured default action applies.

## Sandbox

Sandbox behavior is controlled by `sandbox` config:

- File policy can restrict allowed directories and read-only directories.
- Network policy supports blacklist and whitelist modes.
- Bash can run on host or through a Docker session backend.
- HTTP redirects are checked against the network policy when sandbox network policy is active.

The `sandbox` feature defaults to enabled in the Feature Registry but auto-detects Docker availability. The example config sets `sandbox.enabled: false`, which overrides the feature default.

## Secrets

- Prefer environment expansion with `${VAR}` in YAML rather than hard-coded tokens.
- The admin surface is disabled by default and binds to `127.0.0.1:8080` by default. If enabled, `server.token: "${DAIMON_ADMIN_TOKEN}"` is required; inject it through the environment and never commit the token.
- MCP server responses are passed through redaction before returning to the agent.
- Do not commit local `configs/daimon.yaml`, database files, tool result caches, or generated trajectory exports containing private prompts.

## Admin HTTP Surface

- The Gateway owns the admin server lifecycle; a bind failure aborts startup and shutdown is bounded.
- `GET /health` is public and returns only basic process health.
- `/api/*` requires an exact `Authorization: Bearer <token>` match. The current private endpoint, `GET /api/sessions`, is read-only and returns at most 50 session summaries.
- Loopback is the safe default. Binding to a non-loopback address expands the trusted network boundary and should be paired with host/container firewall policy and TLS termination where appropriate.

## Reporting

For private vulnerability reports, use the repository maintainer contact configured in the GitHub project or open a private advisory if available. Include:

- Affected commit or version.
- Configuration required to trigger the issue.
- Minimal reproduction.
- Whether the issue needs external network, Docker, MCP, the admin token, or channel credentials.

## Maintainer Checklist

- New tools must declare capabilities through `Capabilities()` when side effects, network, or parallel safety matter.
- New `/api/*` routes must remain covered by admin bearer-token auth if they expose private runtime data.
- New config fields that affect security must be included in `configs/daimon.example.yaml`.
- Gateway wiring changes must preserve interceptor order unless intentionally redesigned and documented.
