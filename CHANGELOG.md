# Changelog

All notable changes, newest first. Format follows Keep a Changelog loosely;
dates are UTC.

## [Unreleased]

### Fixed

- Triage scoring is O(tokens) per asset: rarity min/max precomputed once in
  `NewEngine` instead of rescanned per `Score` call (~660x faster at N=3000:
  1.75ms → 2.6µs per call). `BenchmarkScore` guards the hot path.
- `NewEngine` clustering is a single ClusterKey-bucketed pass; the old
  code ran two nested O(N·R) passes and the first pass's result was
  discarded. Buckets sharing one key but differing in exact status/SimHash
  now get suffixed sub-cluster IDs (`<key>#2`) instead of being merged.
- DAG executor: handler errors are logged at failure time and retained
  (returned joined) instead of dropped by a bounded channel; removed the
  false "errgroup semantics" claim (no errgroup dependency).
- SQLite `Flush` failures in scan now log a warning (resumability
  guarantee is load-bearing); `MarkDone` documented as infallible
  (in-memory buffer).
- Graph `PropagateScore` looks up the FROM shard directly instead of
  scanning all 32 shards per BFS node.
- Nuclei tag denylist (`intrusive,dos,oast`, `--nuclei-exclude-tags`):
  live-fire showed the `exposure,misconfig` allowlist alone admitting
  intrusive/oast templates against a live target.

### Added

- `rarefy eval --labels labels.jsonl`: precision/recall/F1 for the triage
  scorer vs a naive nuclei-findings baseline, with unlabeled-coverage
  reporting.
- CI workflow (vet + `go test -race` on push/PR); `-race` can't run on
  Windows dev boxes without cgo, so it lives in CI.
- `internal/mockollama`: deterministic Ollama stand-in for CI LLM-path
  tests (no GPU needed).

### Changed

- `--profile` help text states exactly what each profile does; aggressive
  is currently threads-only (headless crawling tracked as an issue).
- Renamed project Argus → Rarefy (`github.com/raghavraut/rarefy`).

## [1.0.0] - 2026-09-06

Initial public release: campaign-aware TF-IDF triage, sharded evidence
graph with score propagation, bounded Ollama semantic triage with graceful
degradation, bounty-safe nuclei Top-N post-scan, `scan`/`filter`/`export`/
`ui`/`db` CLI, embedded web dashboard, SQLite resume store, corpus dumps
for offline tuning.
