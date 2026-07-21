# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

## Corrections & additions to AGENTS.md

- `internal/api/modules/amp/` no longer exists — ignore that entry in AGENTS.md.
- `AGENTS.md` must never be modified: a CI guard (`.github/workflows/agents-md-guard.yml`) automatically closes any PR that touches it. Put repo guidance updates in this file instead.
- PRs opened against `main` are automatically retargeted to `dev` by CI — base feature branches on `dev`.
- The `internal/translator/` restriction in AGENTS.md is enforced by CI (`pr-path-guard.yml` fails any PR changing that path).

## Request flow (big picture)

A request passes through four layers; changes usually touch only one:

1. **Inbound protocol handlers** — `sdk/api/handlers/{openai,claude,gemini}` parse the client's wire format. `internal/api/server.go` (Gin) wires routes to them. `BaseAPIHandler.ExecuteModel/ExecuteModelStream` (`sdk/api/handlers/model_execution.go`) is the entry into execution.
2. **Auth scheduling** — `sdk/cliproxy/auth` (`conductor.go`) picks a credential: round-robin, cooldown/backoff on failures, model aliasing, force-mapping. Credential records live under `auth-dir` and are hot-reloaded by `internal/watcher`.
3. **Format translation** — `sdk/translator` is the format registry/pipeline (formats: `openai`, `openai-response`, `claude`, `gemini`, `codex`, `antigravity`, `interactions`). Concrete translators live in `internal/translator/<target-provider>/<source-format>/` and self-register via blank imports in `internal/translator/init.go`.
4. **Provider executors** — `internal/runtime/executor/*_executor.go` make the upstream calls (one file per provider; Codex and xAI also have WebSocket variants).

`sdk/cliproxy/service.go` (`Service`, built via `builder.go`) composes all of the above; `cmd/server/main.go` is a thin CLI wrapper around it. Storage-backend env vars (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`) are read in `cmd/server/main.go` and select implementations from `internal/store/`.

## Other entrypoints

- `cmd/log-uploader/` — standalone log uploader (config: `log-uploader.example.yaml`, run via `diag-uploader.sh`)
- `cmd/fetch_codex_models`, `cmd/fetch_antigravity_models`, `cmd/validate_codex_models` — model catalog utilities; CI refreshes catalogs with `.github/scripts/refresh-model-catalogs.sh`
