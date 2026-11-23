# 开发指南

Mihomo Lite 是 Windows x64 本机代理启动器，当前源码版本为 **v0.6.18**。本指南用于干净源码克隆、离线受控构建与开发验证；已有正式工作区的采用、打包流程见 [发布指南](<RELEASE.md>)。

[项目首页](<../README.md>) · [GitHub 仓库](https://github.com/orignn/Mihomo-Lite) · [精简更新记录](<CHANGELOG.md>)

## 1. 环境与源码范围

- **Windows x64**、**PowerShell 5.1 或更新版本**。
- **Go 1.23 或更新版本**，预先安装并放入 PATH；模块要求见 [go.mod](<../go.mod>)。受控构建使用本地工具链，不自动下载 Go。
- **Node.js 18 或更新版本**，用于默认前端回归；可在 PATH 中提供，或用 `-NodePath` 指定可执行文件。脚本不安装 Node，找不到 Node 时会失败，不会静默跳过测试。
- 管理页面为嵌入 Go 源码的原生 HTML/CSS/JavaScript，无需 npm 安装或前端打包。页面入口见 [index.go](<../cmd/mihomolite/index.go>)。

~~~powershell
git clone https://github.com/orignn/Mihomo-Lite.git
Set-Location Mihomo-Lite
go version
node --version
~~~

源码只包含启动器、测试、源图标和工具，不包含 `runtime/`、`data/` 中的 mihomo 内核、Zashboard、MetaDB 或两份 DAT。默认构建不需要这些运行资源；编译成功的启动器 EXE 不是完整安装包。不要从其他人的安装目录复制配置或数据库来补齐源码。

## 2. 首次准备 Go 依赖（明确联网）

[受控构建脚本](<../build.ps1>) 固定使用项目内的 Go 缓存，并设置 `GOPROXY=off`、`GOSUMDB=off`、`GOTOOLCHAIN=local`。仅把依赖下载到 Go 的系统默认缓存，并不能保证受控构建可用。

在源码根目录的新 PowerShell 会话中，显式联网准备同一缓存：

~~~powershell
$env:GOCACHE = "$PWD\.cache\toolchain\gobuild"
$env:GOMODCACHE = "$PWD\.cache\toolchain\gomod"
$env:GOPATH = "$PWD\.cache\toolchain\gopath"
$env:GOENV = "off"
$env:GOWORK = "off"
$env:GOTOOLCHAIN = "local"
$env:GOFLAGS = ""
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
$env:GOPROXY = "https://proxy.golang.org,direct"
$env:GOSUMDB = "sum.golang.org"
go mod download
~~~

模块版本与完整性信息由 [go.mod](<../go.mod>) 和 [go.sum](<../go.sum>) 约束。可按网络环境使用可信 Go 代理，但不要为绕过失败随意删改依赖锁定。缺少模块时，离线构建会直接报错；重新执行上述明确联网准备，或事先迁入可信的同版本依赖缓存，不要把联网下载伪称离线构建的一部分。

这一步只准备编译依赖，不下载运行内核、面板或 Geo 数据。

## 3. 默认构建（干净克隆可用）

在源码根目录运行 [统一构建入口](<../tools/build-release.ps1>)：

~~~powershell
.\tools\build-release.ps1
# Node 未加入 PATH 时，指定已经安装的 Node：
.\tools\build-release.ps1 -NodePath 'C:\path\to\node.exe'
~~~

也可独立执行底层入口：

~~~powershell
.\build.ps1
~~~

默认流程：

1. 固定 Windows/amd64、禁用 CGO 与外部工作区配置，清理继承的应用集成测试开关。
2. 执行一轮 `go test -json ./cmd/mihomolite`，再执行默认 Node 前端回归；保留正常 Go 测试缓存。
3. 使用 `-trimpath` 和 Windows GUI 构建参数编译，检查构建元信息及 EXE 版本资源与源码版本一致。
4. 将 EXE、源码摘要、构建清单与日志写入 `.cache/build/<build-id>/`，隔离测试和流程证据写入 `.cache/tests/`。

**默认不覆盖根目录正式入口、不打包、不准备 Geo 数据，也不启动日常程序修改真实系统代理。** 不应为统计 PASS/SKIP 数量再重复跑整套测试；同一轮统计已记录在构建清单中。

底层脚本的 `-SkipTests` 只适合明确的编译研究，产物不能提升为正式入口；它也不能与 `-Promote` 同用。不要用跳过测试掩盖 Node 或依赖缺失。

## 4. 源码导航

| 内容 | 入口 |
| --- | --- |
| 应用启动与本机 HTTP 管理 | [main.go](<../cmd/mihomolite/main.go>) |
| 版本常量与设置类型 | [types.go](<../cmd/mihomolite/types.go>) |
| 本机管理页面 | [index.go](<../cmd/mihomolite/index.go>) |
| 独立个性化设置 | [overrides.go](<../cmd/mihomolite/overrides.go>) |
| 分享链接解析 | [sharelink.go](<../cmd/mihomolite/sharelink.go>) |
| 内核、面板组件维护 | [component_updates.go](<../cmd/mihomolite/component_updates.go>) |
| 受控构建与本地发布编排 | [build.ps1](<../build.ps1>)、[build-release.ps1](<../tools/build-release.ps1>) |
| 白名单打包与只读约束检查 | [package.ps1](<../tools/package.ps1>)、[check-workspace.ps1](<../tools/check-workspace.ps1>) |

~~~text
cmd/                 启动器源码、嵌入页面与资源
assets/              源图标
tests/               可复用回归和隔离测试代码
tools/               维护脚本
docs/                公开说明
.cache/build/        临时编译结果
.cache/tests/        隔离测试与浏览器证据
.cache/toolchain/    Go 编译、模块与 GOPATH 缓存
.cache/downloads/    明确准备的公开资源
.cache/package/      本地打包暂存
~~~

生成的面板文件不是启动器页面源码，不应移进源码目录。源码提交不包含缓存、安装组件、发布产物、私有测试数据或本机设置。

## 5. 测试与安全边界

- 默认回归是 Go 单元/合成测试和 Node 页面逻辑测试，不等于完整 Windows 实机验收。依赖官方 mihomo 静态校验的配置/覆写场景需要专用测试工作区中的内核资源；源码克隆缺少该资源时明确 SKIP，不伪造校验成功。其余不依赖内核的回归照常执行。
- SKIP 表示相应场景未在该轮执行。真实浏览器、原生窗口、官方内核集成、真实重启/断电及系统代理场景须另行授权，在隔离目录中准备资源；不得为了消除 SKIP 使用日常数据或开启真实注册表测试。
- 仅使用合成配置或明确可公开的样例。订阅地址、凭据、备份、日志和最终 YAML 预览均可能敏感，不得写入仓库、截图或 Issue。
- 不强杀、覆盖或移动运行中的启动器/内核；不修改用户数据、运行组件、日志和本机设置。
- 保持 Lite 的本机监听与权限边界：不支持 TUN、LAN 共享或额外入站监听，不把异常会话恢复宣传为全局断网保护。
- 当前版本由 [types.go](<../cmd/mihomolite/types.go>) 定义。应用功能变化才按项目约定同步版本和资源；纯文档维护不提升版本。

可选的私有订阅专项见 [unified_proxy_core_test.go](<../cmd/mihomolite/unified_proxy_core_test.go>)：仅显式设置 `MIHOMOLITE_UNIFIED_PRIVATE_ROOT` 时读取隔离根目录中的 `inputs/fixture-01.private.yaml` 至 `inputs/fixture-04.private.yaml`。该根目录必须位于 `.cache/tests/unified-proxy/` 下的独立批次；未设置时跳过，默认受控构建还会清理继承的专项开关。所需内核和校验资源也必须单独准备，真实配置及私有缓存不随源码发布。本轮不执行该 opt-in 专项。

报告中分别写明实际执行的命令、结果、跳过项与未覆盖范围；没有执行就不能写“测试通过”。本指南提供执行方法，不代表本次文档维护重新运行了应用测试。

## 6. 进入正式发布前

干净克隆默认只构建启动器。`-Publish` 是**已准备好的正式工作区**的本地采用与打包流程：需要官方公开运行组件、经过校验的本地 Geo 批次及完整发布输入，并会改变正式入口与本地清单；它不上传 GitHub。准备与核验要求见 [发布指南](<RELEASE.md>)。

项目目前未指定主许可证；公开源码不等于任意再分发授权。第三方组件、依赖与 Geo 数据各自遵守对应条款，参见 [首页许可证说明](<../README.md#第三方组件与许可证>)。
