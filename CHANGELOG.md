# Changelog

All notable changes to Daimon are tracked here.

## [0.1.0] - 2026-07-11

### Added

- Added a Gateway-managed, disabled-by-default admin HTTP subsystem with public `GET /health` and bearer-protected, read-only `GET /api/sessions`.
- Added native CGO packaging and release jobs for Linux and macOS on amd64 and arm64, publishing `daimon_<os>_<arch>.tar.gz` archives and `checksums.txt`.
- Added hermetic evaluation, incremental lint, native-package, Docker Compose, and non-root container runtime checks.

### Changed

- Changed the admin default bind address to `127.0.0.1:8080`; enabling it now requires `server.token`, intended to be injected as `${DAIMON_ADMIN_TOKEN}`.
- Moved admin startup and shutdown under the Gateway lifecycle so bind errors fail startup and shutdown is graceful and bounded.
- Corrected Docker Compose service/config names and persisted Daimon state under the non-root user's `/home/daimon/.daimon` directory.
- Replaced GoReleaser cross-compilation with native per-platform CGO builds.

### Fixed

- Propagated post-tool hook failures while continuing remaining handlers.
- Rejected invalid tar permission modes before creating archive entries or parent directories.

### Security

- Added bearer authentication for private admin routes, constant-time token comparison, finite HTTP timeouts, loopback-by-default binding, and generic internal-error responses.
- Hardened archive extraction against invalid permission modes and kept the container runtime/state directory owned by the non-root `daimon` user.

### Known Limitations

- Daimon `0.1.0` is a single-user, local-first alpha; multi-tenant and exposed public-server deployments are not supported targets.
- Autonomous Heart, Sleep, and SelfOps behavior is disabled by default and requires explicit configuration.
- Episodes persist checkpoints and terminal outcomes, but an interrupted Episode cannot resume durably from its exact midpoint.

## Unreleased

### Removed

- Removed the web dashboard subsystem entirely: the `internal/dashboard` package (HTTP/WebSocket server, event bus, agent state tracker, evolution bridge, embedded Preact frontend), the Preact app under `web/`, the `dashboard` Feature Registry entry, and the `dashboard:` config block (`DashboardConfig`). The standalone Vue Studio prototype (`web/studio/`), always-on health server, OpenTelemetry observability, and cognitive metrics are unaffected.
- Removed the Prometheus `/metrics` endpoint, which was served only by the dashboard server. No custom collectors were registered on it.
- Removed the Makefile `web` frontend build target; `make build` no longer builds an embedded frontend.
- Removed the TUI dashboard-URL header display (`SetDashboardURL`).
- Removed the Knowledge Base and Knowledge Graph subsystems: deleted the `internal/knowledge` package (document ingestion, chunking, BM25+vector hybrid retrieval, reranker) and the `internal/knowledge/graph` package (entity/relation extraction, graph traversal, decay, sync).
- Removed the `knowledge`, `knowledge_graph`, and `reranker` features from the Feature Registry, the `knowledge:` and `graph:` config sections, and the `ironclaw_knowledge_query` MCP tool.
- The memory unified retriever now fuses only memory-store and procedural sources; `FusionWeights` drops `KnowledgeWeight` and `GraphWeight`.
- Dropped the `kb_*` and `kg_*` tables via migration `024_drop_knowledge_tables.sql`; removed migrations `004_knowledge_base.sql`, `005_knowledge_graph.sql`, and `011_temporal_graph.sql`.
- Removed the `knowledge` eval dimension and suite (11 dimensions remain).

### Fixed

- Fixed Knowledge Base embedding initialization so it honors `memory.embedding_base_url`. Memory, Codebase Index, and Knowledge Base now use the same OpenAI-compatible embedding endpoint configuration path.

### Changed

- Consolidated the project overview into an English `README.md` and aligned Chinese `README_zh.md`, including the current architecture, CLI surface, configuration, and documentation map.
- Removed historical task briefs, execution reports, handover snapshots, architecture review notes, implementation trackers, and temporary Superpowers plans/specs; retained the blueprint and authoritative as-built/operational documentation.
- The agent observability emitter is now owned directly by the Gateway (`gw.emitter`) instead of the removed dashboard subsystem; it degrades to a no-op discard emitter when no consumer (e.g. the TUI status bar) is attached.
- Deleted the stale documentation set under `docs/` and rewrote the project documentation from current source.
- Replaced the root README, Chinese README, code health report, contribution guides, security guide, code of conduct, optimization roadmap, Claude handoff notes, and example README.
- Added a new numbered documentation tree covering architecture, Gateway lifecycle, CLI/config/userdir, Agent runtime, tools/security hooks, Memory/Knowledge/Graph, channels/observability, store/session/task ledger/scheduler, evolution, frontend apps, developer workflows, and package inventory.

### Verification

- `make build-bin`
- `make vet`
- `make test` (CGO, `fts5` tag, race detector — all packages pass)
- `npm ci && npm run build` in `web/studio/`

## Historical Notes

Previous documentation contained many historical feature plans and references to removed or renamed modules. Those files were intentionally replaced with a source-derived documentation set. OpenSpec archives and agent/skill workflow assets remain in place because they are operational or specification artifacts rather than public architecture documentation.
