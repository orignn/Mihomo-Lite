# 第三方组件与发布许可说明

本页说明 Mihomo Lite 源码仓库与完整 Windows 安装包中涉及第三方组件时的许可与来源边界。它是分发政策说明，不是法律意见，也不构成对所有历史贡献者权利链或可复现性的全面审计。

## 本项目自身

仓库当前没有声明项目级主许可证（LICENSE）。这意味着不能推定他人可以任意复制、修改或再分发本仓库中由维护者提供的源码；也没有在此擅自选择 MIT、GPL 或其他许可证。维护者发布自己构建的程序不受影响。如果后续由权利人确定主许可证，应以仓库新增的许可证文件为准。

## 完整安装包包含什么

GitHub Actions 生成的完整 ZIP 只从固定、可核验的官方公开来源准备以下组件，并逐个校验 SHA-256：

| 组件 | 官方来源 | 许可/归属依据 |
| --- | --- | --- |
| mihomo 内核（Windows x64） | [MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo) 固定 Release | GNU GPL v3；保留原文 |
| Zashboard 静态面板 | [Zephyruso/zashboard](https://github.com/Zephyruso/zashboard) 固定 Release | MIT；保留原始版权声明 |
| GeoIP MetaDB、GeoSite/GeoIP DAT | [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat) 同一固定 Release 批次 | 发布方许可证 + 其 README 列明的上游数据来源与归属 |
| GeoLite 数据库元素 | MaxMind GeoLite2 | Creative Commons Attribution-ShareAlike 4.0，须保留 MaxMind 归属 |

完整包内固定包含 THIRD_PARTY_NOTICES.md 与 LICENSES/ 目录，收录上述组件的原始许可证、版权声明、来源说明和 Go 依赖模块随附的 LICENSE/NOTICE 全文。LICENSES/manifest.json 是这些文件的机器可读索引；发布检查会逐条核对索引、ZIP 载荷和摘要清单。

### MaxMind GeoLite 归属与更新义务

本产品包含由 MaxMind 创建的 GeoLite 数据（<https://www.maxmind.com>）。须保留该归属声明及 CC BY-SA 4.0 许可全文。

MaxMind GeoLite EULA 还要求：在新版数据库发布后及时改用新版，并在新版发布后三十（30）天内停止使用、销毁旧版数据库/数据。因此：

- 维护者需要定期重建并发布使用新数据的新版本；
- 旧版本的完整安装包不应被当作永久合规的历史资产长期分发；
- 如保留旧 Release，应仅保留源码/说明，或按上游要求处理旧数据；
- 该要求不阻止通过 GitHub Releases 正常分发当前版本。

### Geo 数据的来源与限制

MetaCubeX/meta-rules-dat 在 README 中注明其数据来自多个公开项目（例如 geoip 相关项目、geosite 规则集合等），这些上游各有自己的许可与署名要求。上游生成流程未固定全部输入引用，因此：

- 本项目只做“原样复制官方发布资产，并记录固定 Release/Tag/资产摘要”；
- 不声称构建完全可复现，也不声称已完整审计所有上游贡献者权利链；
- 随包保留发布方声明、已知上游归属和许可证全文；
- 如某个数据源条款被确认与本分发方式冲突，应移除或替换该来源并重新生成包。

### GPL 对应源码

mihomo 以 GPL v3 发布。发布完整包时，GitHub Release 同时附带与所用 Release 标签一致的固定提交源码归档（Source-core-<commit>.tar.gz 等），并在包内 LICENSES/SOURCE_OFFER.md 中记录仓库、提交、URL、大小与 SHA-256。源码归档是独立 Release 资产，不放入安装 ZIP。

## 不包含什么

完整包不包含用户订阅、配置、规则、密钥、日志、缓存、浏览器资料、运行快照或本地数据库。发布流程拒绝任何已知私有路径，并在打包、检查和发布阶段多次校验。

## 报告问题

如果认为某个组件的归属或许可声明不完整，请通过仓库 Issues 提供具体组件、版本与依据链接；维护者会核实后更新说明或替换来源。
