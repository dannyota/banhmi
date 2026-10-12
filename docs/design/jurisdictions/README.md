# Jurisdictions

banhmi uses one codebase to build a separate legal corpus for each country. Each corpus has its own
PostgreSQL database and public domain. The production read path serves all six domains from one MCP
process; the pipeline still processes one jurisdiction at a time. See the
[`PLAYBOOK.md`](PLAYBOOK.md) for the shared model and [`PLAN.md`](../../../PLAN.md) for current corpus
and evaluation metrics.

## Registry

| Country | Code | Codename | Endpoint | Database | Corpus language | Source IDs | Status | Design |
|---|---|---|---|---|---|---|---|---|
| Vietnam | `vn` | `banhmi` | `banhmi.danny.vn/mcp` | `banhmi` | Vietnamese | `vbpl`, `congbao`, `vanban`, `sbv_hanoi` | **LIVE** since 2026-06-01 | [sources.md](../sources.md) |
| Malaysia | `my` | `laksa` | `laksa.danny.vn/mcp` | `laksa` | English | `agclom`, `bnm`, `sc` | **LIVE** since 2026-06-22 | [MALAYSIA.md](MALAYSIA.md) |
| Indonesia | `id` | `rendang` | `rendang.danny.vn/mcp` | `rendang` | Indonesian | `bpk`, `bi`, `ojkweb`; `ojk` when proxy-enabled | **LIVE**; revived 2026-07-12 | [INDONESIA.md](INDONESIA.md) |
| Singapore | `sg` | `kaya` | `kaya.danny.vn/mcp` | `kaya` | English | `sso`, `mas`, `pdpc`, `csa` | **LIVE** since 2026-07-17 | [SINGAPORE.md](SINGAPORE.md) |
| Thailand | `th` | `tomyum` | `tomyum.danny.vn/mcp` | `tomyum` | Thai | `ocs`, `bot`, `etda`, `sec` | **LIVE** since 2026-07-17 | [THAILAND.md](THAILAND.md) |
| Cambodia | `kh` | `amok` | `amok.danny.vn/mcp` | `amok` | English | `cdc`, `serc`, `odc`, `nbc` | **LIVE** since 2026-07-18 | [CAMBODIA.md](CAMBODIA.md) |

## Conventions

- The codename is a short ASCII name based on a national dish. It identifies the public domain,
  default database, and MCP server identity.
- Country codes are lower-case ISO 3166-1 alpha-2 values. `vn` is the default when no jurisdiction is
  configured. Application startup rejects unknown codes.
- Status values are **PROPOSED**, **BUILDING**, and **LIVE**. A proposed source becomes verified only
  after a read-only spike proves its discovery, metadata, download, access-control, and legal-status
  contracts.
- Country docs contain only country-specific facts. Shared behavior belongs in
  [`PLAYBOOK.md`](PLAYBOOK.md) or the core documents under [`docs/design/`](../).
