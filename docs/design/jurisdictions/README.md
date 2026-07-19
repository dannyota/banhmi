# Jurisdictions — index

banhmi is one codebase serving **one corpus per country** — separate database, MCP service, and domain
per jurisdiction. How to add a country: [`PLAYBOOK.md`](PLAYBOOK.md). Roadmap and build order:
[`PLAN.md`](../../../PLAN.md).

## Registry

| Country | Codename | Domain | DB | Language | Sources | Status | Design doc |
|---|---|---|---|---|---|---|---|
| 🇻🇳 Vietnam | `banhmi` | banhmi.danny.vn | `banhmi` | Vietnamese | vbpl · congbao · vanban · sbv_hanoi | **LIVE** | [SOURCES.md](../SOURCES.md) (VN is the reference jurisdiction) |
| 🇲🇾 Malaysia | `laksa` | laksa.danny.vn | `laksa` | English | agclom · bnm · sc | **LIVE** | [MALAYSIA.md](MALAYSIA.md) |
| 🇮🇩 Indonesia | `rendang` | `rendang.danny.vn` | RDS `rendang` | Indonesian | bpk · bi · ojk · ojkweb | **Live** (revived 2026-07-12; corpus rebuilt 2026-07-13) | [INDONESIA.md](INDONESIA.md) |
| 🇸🇬 Singapore | `kaya` | kaya.danny.vn | `kaya` | English | sso · mas · pdpc · csa | **LIVE** (2026-07-17) | [SINGAPORE.md](SINGAPORE.md) |
| 🇹🇭 Thailand | `tomyum` | tomyum.danny.vn | `tomyum` | Thai | ocs · bot · etda · sec | **LIVE** (2026-07-17) | [THAILAND.md](THAILAND.md) |
| 🇰🇭 Cambodia | `amok` | amok.danny.vn | `amok` | English | nbc · serc · cdc · odc | **LIVE** (2026-07-18) | [CAMBODIA.md](CAMBODIA.md) |

## Conventions

- **Codename = national dish** — short, ASCII, distinctly national. It names the domain
  (`<codename>.danny.vn`), the Postgres database, the ECS container, and the MCP server identity.
- **Status values:** PROPOSED (design doc only) → BUILDING → LIVE. A source list is **candidate**
  until a live verification spike proves the fetch contract (the MY bar — see PLAYBOOK).
- **One doc per country**, only country-specifics inside; everything shared lives in
  [`PLAYBOOK.md`](PLAYBOOK.md) or the core design docs in [`docs/design/`](../).
