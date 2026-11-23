# 发布指南

[项目首页](<../README.md>) · [开发指南](<DEVELOPMENT.md>) · [精简更新记录](<CHANGELOG.md>)

项目仓库：[orignn/Mihomo-Lite](https://github.com/orignn/Mihomo-Lite)；完整包发布位置：[Releases](https://github.com/orignn/Mihomo-Lite/releases)。**目前仓库提供源码，本轮不上传完整安装包。** 下文是维护者的本地构建与未来完整包发布流程，不表示 Releases 已有可下载资产。

当前启动器源码版本为 **v0.6.18**，以 [版本常量](<../cmd/mihomolite/types.go>) 为准。相同语义版本可能对应不同构建，须用 SHA-256 区分；不能仅凭版本号认定两个 EXE 或 ZIP 相同。

## 1. 区分源码构建与正式工作区

| 场景 | 命令 | 影响 |
| --- | --- | --- |
| 干净源码克隆，已安装 Go/Node 并准备依赖缓存 | `.\tools\build-release.ps1` | 默认回归与缓存构建；不需要运行组件或 Geo，不更新正式入口 |
| 已准备完整公开资源的正式工作区，明确采用新构建 | `.\tools\build-release.ps1 -Publish` | 进程/本地 Geo 预检、回归、构建、提升正式入口、白名单打包及集中检查 |
| 已采用并有完整受控构建身份的正式入口，只打包 | `.\tools\build-release.ps1 -PackageOnly` | 不重复构建/回归；从正式入口打包并核验 |

入口见 [build-release.ps1](<../tools/build-release.ps1>)。`-Publish` 是本地维护操作，**不会创建 GitHub Release，也不会提交、推送或上传文件**。不要把它当作干净克隆的一键安装命令；缺少运行组件、公开数据库或完整发布输入时，不能生成完整包。

正式包应在与日常安装分开的专用工作区准备。不得复制用户配置、订阅或私有数据库来补齐输入，也不应在日常运行区执行资源准备。Go 与 Node 的安装、离线依赖缓存准备详见 [开发指南](<DEVELOPMENT.md>)。

## 2. 正式包资源白名单

只使用可核验的官方公开资源，记录上游仓库、固定发布版本/ID、资产名、大小及 SHA-256；下载失败、缺少可信摘要、摘要不符或资源来源不清时停止发布。

| 资源 | 官方来源 | 专用正式工作区中的输入 |
| --- | --- | --- |
| 启动器 | 本仓库源码，经 [build.ps1](<../build.ps1>) 受控构建与提升 | `MihomoLite.exe` |
| Windows x64 mihomo 内核 | [MetaCubeX/mihomo Releases](https://github.com/MetaCubeX/mihomo/releases) | `runtime/mihomo.exe` |
| Zashboard 静态分发 | [Zephyruso/zashboard Releases](https://github.com/Zephyruso/zashboard/releases) | `data/ui/`，须有 `index.html` |
| MetaDB | [MetaCubeX/meta-rules-dat Releases](https://github.com/MetaCubeX/meta-rules-dat/releases) 的兼容公开资产 | `data/geoip.metadb` |
| GeoSite / GeoIP 双 DAT | 同一 [MetaCubeX/meta-rules-dat Release](https://github.com/MetaCubeX/meta-rules-dat/releases) 及同批校验资产 | `.cache/downloads/` 内已核验的公开批次 |
| 示例与公开说明 | 本仓库已审核内容 | [example.yaml](<../example.yaml>)、[README](<../README.md>) 和本目录三份公开指南/更新记录 |

现有 [package.ps1](<../tools/package.ps1>) 从指定工作区读取内核、面板和 MetaDB，再将经校验下载批次中的双 DAT 放入包内。这只是输入路径约定，不代表这些路径里的任意旧文件都可公开：**不得从用户运行区、旧包解压目录或私有测试环境取数据库**。面板目录也只能放官方静态分发，不能混入日志、缓存或个人资料。

## 3. 双 DAT 本地批次

新完整包必须同时包含非空的 `data/geosite.dat` 和 `data/geoip.dat`，并保留 `data/geoip.metadb`；MetaDB 不能替代任一 DAT。

[Geo 准备入口](<../tools/prepare-geodata.ps1>) 默认只选择并核验本地资源，不联网：

~~~powershell
.\tools\prepare-geodata.ps1
~~~

只有需要首次准备或更新资源时，才明确联网：

~~~powershell
.\tools\prepare-geodata.ps1 -Refresh
~~~

`-Refresh` 从固定官方上游下载同一 Release 的四份资产：

~~~text
geosite.dat
geosite.dat.sha256sum
geoip.dat
geoip.dat.sha256sum
~~~

脚本检查 Release/资产身份、API SHA-256 digest、文件大小及两份 sidecar 摘要，下载后复查固定 Release 的资产是否变化，成功后记录公开来源并生成新的独占批次；失败不覆盖历史有效批次，也不更新用户运行数据库。

[本地选择库](<../tools/release-geodata.ps1>) 在自动选择时要求公开上游来源记录：优先有效的 `release-geodata`，其次最近有效的 `release-geodata-*` 批次，每次重新核验摘要。文件不跨批次拼接，日期旧不触发自动联网。

`-GeoDataRoot` 可显式指定工作区相对下载批次；指定无效时直接失败，不自动换源。显式路径仍须由维护者准备为官方可核验的公开批次，不能用自造 sidecar 代替公开来源证据。普通构建、打包和本地选择均不联网；无有效本地资源时先单独准备，不自动退回用户运行区。

## 4. 本地采用与打包

仅在正式工作区资源齐备、源码和版本资源一致、明确需要采用新构建时操作。先从托盘正常退出启动器，并确认启动器/内核都已退出；不强杀进程、不覆盖运行文件。

~~~powershell
# 本地默认回归、构建、采用、打包和集中核验：
.\tools\build-release.ps1 -Publish
# 如 Node 不在 PATH：
# .\tools\build-release.ps1 -Publish -NodePath 'C:\path\to\node.exe'
~~~

该流程重新受控构建，不直接提升隔离测试 EXE。默认回归失败、跳过整套测试或构建元信息不符时不能提升；旧入口和旧发布产物按受控流程保留，禁止覆盖同名不同来源的包。

如正式入口已采用，且源码、工具与构建身份未变化，只需打包一次：

~~~powershell
.\tools\build-release.ps1 -PackageOnly
~~~

这不是补做应用验收，不能把“打包检查通过”写成“应用测试通过”。若维护脚本已变化使旧构建来源校验不符，重新执行受控构建，不改旧清单来绕过检查。

包名格式：`MihomoLite-v<version>-<启动器SHA-256前12位>-win-x64.zip`。同版本的包仍按摘要区分；禁止直接压缩整个工作区或整个运行数据目录。

## 5. 核验与 GitHub 公开边界

统一发布/只打包入口会集中执行 [只读约束检查器](<../tools/check-workspace.ps1>)；如需要独立检查，运行：

~~~powershell
.\tools\check-workspace.ps1 -RequireGeoData -VerifyPayload
~~~

检查正式入口、源码版本资源、本地根/发布清单、ZIP 内启动器及载荷 SHA-256、摘要清单与双 DAT 规则。缺文件、空数据库、摘要不一致或载荷超出白名单时停止，不靠语义版本相同放行；不要手工删改 ZIP 后继续沿用旧摘要。

发布前还需单独审核：

- 源码与 ZIP 是不同公开对象。源码提交不包含运行组件、用户数据、缓存、归档或本地构建身份；完整包只上传审核后的白名单资产及其摘要。
- 包内说明采用 [README](<../README.md>)、[开发指南](<DEVELOPMENT.md>)、[发布指南](<RELEASE.md>)、[精简更新记录](<CHANGELOG.md>)，公开文档相互链接应可用；原本地维护/采用/诊断历史不进入公开包。源码导航链接在 GitHub 仓库中查看，完整包不必附带源码。
- 不包含真实订阅 URL、完整用户 YAML、配置列表、自定义规则、API 密钥、代理恢复记录、备份、日志、浏览器资料或私有数据库。
- 发布说明列出启动器/组件版本、资产 SHA-256、实际执行的测试与跳过项。`sourceVerified` 只表示受控构建对应源码，不表示所有真实节点、Windows 系统代理、重启/断电及原生窗口场景已验收。
- 当前本地已有的历史包不会因文档或流程维护而自动重打；历史身份记录不追溯改写。此次源码提交不采用新版、不重打或上传完整安装包。

**本地核验成功不等于完成 GitHub 发布。** 完整安装包将于后续单独发布后从 [Releases](https://github.com/orignn/Mihomo-Lite/releases) 下载；若尚无完整包资产，用户只能获取源码，不应把 GitHub 自动生成的 Source code 压缩包当作安装包。

## 6. 许可证

本项目未指定主许可证，未提供项目级 LICENSE；不擅自假定 MIT、GPL 或其他许可。第三方内核、面板、依赖和 Geo 数据按各自条款使用，完整包分发时保留所需的第三方许可材料。源码公开本身不授予任意再分发授权，参见 [首页许可证说明](<../README.md#第三方组件与许可证>)。
