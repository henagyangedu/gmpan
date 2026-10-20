# 第三方组件说明

GmPan 自身的许可见 `LICENSE`。第三方组件不受 GmPan 的企业授权、赞助或收费规则替代，各自版权和许可证继续有效。

## 图形部署测试包

本地完整测试包可包含下列独立软件。请保留其原始许可证及声明；不能把完整依赖包当作已经取得商业再分发许可的产品直接对外发布。

| 组件 | 用途与来源 | 许可说明 |
| --- | --- | --- |
| MySQL Community Server 8.4 LTS | [Oracle 官方下载](https://dev.mysql.com/downloads/mysql/8.4.html) | GPLv2；原始许可证随官方 ZIP 保留。源码取得方式见 [MySQL 源码](https://dev.mysql.com/downloads/mysql/)。采用官方软件包时选择 Source Code 取得匹配版本。 |
| ClamAV 与病毒库 | [ClamAV 官方下载](https://www.clamav.net/downloads)、[源码](https://github.com/Cisco-Talos/clamav) | 按随包 `COPYING`、`COPYING.txt` 和各依赖声明执行；病毒库有独立的更新与使用要求。 |
| Microsoft Visual C++ x64 Runtime | [Microsoft 官方下载和许可说明](https://learn.microsoft.com/en-us/cpp/windows/latest-supported-vc-redist) | Microsoft 许可；安装及再分发须满足其适用条款。运行库安装程序保持原样。 |

MySQL 和 ClamAV 通过独立进程/本机连接运行，不会被改成 GmPan 自有组件。程序升级和病毒库更新分别维护。

计划公开发布包含 MySQL、ClamAV 或 Microsoft 运行库的完整安装包前，发行方应核实适用许可、完整对应源码及提供方式，以及 MySQL 商业捆绑规则。相关参考：[Oracle OEM/ISV 许可说明](https://www.mysql.com/about/legal/licensing/oem/)、[Microsoft 再分发说明](https://learn.microsoft.com/en-us/cpp/windows/redistributing-visual-cpp-files)。本项目目前提供的本地完整包用于部署测试，未据此宣称已取得额外商业再分发授权。

## 应用依赖

Go 和前端依赖的准确版本以 `go.mod`、`go.sum`、`web/package-lock.json` 为准。核心依赖包括：Go 标准库（BSD 风格）、go-sql-driver/mysql（MPL-2.0）、golang.org/x/crypto、golang.org/x/sys（BSD 风格）、filippo.io/edwards25519（BSD-3-Clause）、Vue、Element Plus 及其图标（MIT）。再分发时一并保留相应上游版权与许可证。
