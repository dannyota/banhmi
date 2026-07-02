# Jurisdictions — index

banhmi is one codebase serving **one corpus per country** — separate database, MCP service, and domain
per jurisdiction. How to add a country: [`PLAYBOOK.md`](PLAYBOOK.md). Roadmap and build order:
[`PLAN.md`](../../../PLAN.md).

## Registry

| Country | Codename | Domain | DB | Language | Sources | Status | Design doc |
|---|---|---|---|---|---|---|---|
| 🇻🇳 Vietnam | `banhmi` | banhmi.danny.vn | `banhmi` | Vietnamese | vbpl · congbao · vanban · sbv_hanoi | **LIVE** | [SOURCES.md](../SOURCES.md) (VN is the reference jurisdiction) |
| 🇲🇾 Malaysia | `laksa` | laksa.danny.vn | `laksa` | English | agclom · bnm · sc | **LIVE** | [MALAYSIA.md](MALAYSIA.md) |
| 🇮🇩 Indonesia | `rendang`* | rendang.danny.vn* | `rendang`* | Indonesian | ojk · bi · peraturan.go.id (candidates) | PROPOSED | [INDONESIA.md](INDONESIA.md) |
| 🇹🇭 Thailand | `tomyum`* | tomyum.danny.vn* | `tomyum`* | Thai | bot · krisdika · ratchakitcha (candidates) | PROPOSED | [THAILAND.md](THAILAND.md) |
| 🇸🇬 Singapore | `kaya`* | kaya.danny.vn* | `kaya`* | English | mas · sso · pdpc (candidates) | PROPOSED | [SINGAPORE.md](SINGAPORE.md) |

\* Proposed codename/domain — food-themed like *bánh mì*/*laksa*; **pending maintainer sign-off**.

## Conventions

- **Codename = national dish** — short, ASCII, distinctly national. It names the domain
  (`<codename>.danny.vn`), the Postgres database, the Cloud Run service, and the MCP server identity.
- **Status values:** PROPOSED (design doc only) → BUILDING → LIVE. A source list is **candidate**
  until a live verification spike proves the fetch contract (the MY bar — see PLAYBOOK).
- **One doc per country**, only country-specifics inside; everything shared lives in
  [`PLAYBOOK.md`](PLAYBOOK.md) or the core design docs in [`docs/design/`](../).
