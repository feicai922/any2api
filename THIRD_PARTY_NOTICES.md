# Third-Party Notices

This repository bundles source code from five upstream projects under `upstreams/`. The top-level
gateway and management panel do not relicense those projects. Each upstream directory keeps its own
license file and notices.

| Directory | Upstream | License | Bundled snapshot |
|---|---|---|---|
| `upstreams/workbuddy` | https://github.com/linguo2625469/workbuddy2api-panel | MIT | `6b31db6d045e985042620c819c3a938002e7b0b1` |
| `upstreams/qoder` | https://github.com/Zhengyuuuui/qoder2api | GPL-3.0 (see `upstreams/qoder/LICENSE`) | `4789816e8596fc18d503df677f3e02bf48d86fdc` |
| `upstreams/opencode` | https://github.com/TiaraBasori/opencode2api | MIT | `2be4813d10790360ed6623f92bedd2159b2bd133` |
| `upstreams/trae` | https://github.com/autumnsentiment/Trae2api-cn | MIT | `2f2dc1a24ea514207f0091cfd7a8e428d9571b24` |
| `upstreams/gcli` | https://github.com/su-kaka/gcli2api | CNC-1.0 (non-commercial terms apply) | `d7e97ec1bbba25e8864539bb89adbd6ae226fd83` |

## Important licensing note

The repositories are aggregated for convenient local deployment and management; they are not merged
into one licensed work. In particular, `upstreams/qoder` is GPL-3.0 and `upstreams/gcli` uses
CNC-1.0, which includes non-commercial restrictions. Review each upstream license and the upstream
service terms before redistribution or commercial use. No top-level license is granted for the
combined repository.

Local runtime data, account credentials, cookies, OAuth tokens, logs, databases, virtual environments,
and real configuration files are intentionally excluded from this repository.
