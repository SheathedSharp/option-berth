# Third-party notices

本项目维护的代码适用根目录 [MIT](LICENSE)。第三方材料保留各自许可，不因放进这个仓库而改为 MIT。完整固定版本源码及原始许可文件见 [third_party 源码清单](third_party/README.md)；范围与使用方法见[中文说明](docs/third-party.zh.md) / [English](docs/third-party.md)。

## 站点资产：GSAP

`site/assets/gsap.min.js` 是 GSAP 3.15.1 的原样副本，随站点发布，已与固定上游快照中的 `dist/gsap.min.js` 核对。它适用自己的标准“不收费”许可，不是本项目的 MIT，也不应擅自标为 OSI 开源许可。原版权及许可文件头保持原样。

官方许可：<https://gsap.com/standard-license>。本仓库保留[许可正文](third_party/licenses/gsap-standard-license.txt)及[网页原始快照](third_party/licenses/gsap-standard-license.html)及其哈希，抓取日期为 2026-11-15 Asia/Shanghai；这是当时的官方页面，不冒充带版本号的历史法律文本。GSAP 完整上游源码也在清单中，不能因为上游归档没有单独 LICENSE 文件就忽略文件头和正式条款。

## Go 依赖、标准库与 SQLite

引擎的实际模块版本由 [engine/go.mod](engine/go.mod) 和 [engine/go.sum](engine/go.sum) 固定。`third_party` 现在保存选中依赖图的直接、间接、测试及生成依赖的完整上游源码包，另保留可取得且通过 h1 校验的完整模块 ZIP。它们只是源码资料，不启用 vendor、不修改构建解析，也不更新依赖版本。

清单另列六种目标平台生产导入的并集，不能将“模块图选中”理解为每个二进制都链接了它。Go 标准库/工具链的源码按 go.mod 声明版本保存；具体产物使用的编译器版本仍以构建清单为准。SQLite 同时保留 modernc 的生成实现和原始 SQLite 对应版本源码，原始公共领域说明及嵌入材料许可均不删除。

现有二进制打包仍使用 [go-runtime-licenses.txt](third_party/go-runtime-licenses.txt) 作为许可输入；它限定于原生运行依赖，不是全部开发工具和所有平台的完整物料清单。发二进制或安装包时，仍须根据实际目标产物核对模块、编译器及嵌入 NOTICE，并携带适用原文。源码归档不替代这项工作。

## 开发工具与外部环境

站点锁文件中的 `@playwright/test`、`playwright`、`playwright-core` 来自同一固定版本 Playwright 完整源码，适用其原许可。CI 固定的 Mage、nfpm、actionlint 也保存完整工具源码快照；这些工具自身的独立开发依赖图不冒充本项目的引擎依赖闭包。JSONSchema 上游测试套件子模块按实际 Gitlink commit 单独补齐。

托管 Actions、操作系统 SDK、另行下载的浏览器、外部 agent 和模型服务属于执行环境，本仓库不分发其二进制或假装把整个环境封闭归档。工具和源码包内的原始 LICENSE、NOTICE、版权文件与文件头均保持原样；不执行下载源码的脚本或钩子。

## 历史来源：Sonar

引擎早期参考过 [raskrebs/sonar](https://github.com/raskrebs/sonar)，原始记录固定在 `be48afbee89fd2381d0e2c8b0aa9ec1d81194546`。仓库历史的 UPSTREAM/许可声明可以证明这一来源。当前引擎已经重构，Sonar 不是当前运行依赖；这里也不声称每个现有文件均派生自它。

不能仅凭连续若干行的相似率归零，就认定著作权或原许可义务已经消失。因此保守恢复其原版权和完整 MIT 原文，并在 third_party 保存原始完整源码快照，而不是洗去来源。原文亦见 [Sonar-MIT.txt](third_party/licenses/Sonar-MIT.txt)：

```text
MIT License

Copyright (c) 2026 RasKrebs

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## 独立客户端与字体

macOS 客户端及其字体（Monaspace，SIL Open Font License 1.1）、终端依赖（SwiftTerm，MIT）由独立仓库 [option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) 分发并维护许可。本仓库不包含这些资产，不能把其他仓库的材料算作本仓库分发，也不替它们重新许可。

## 名称、商标与后续变更

第三方名称仅说明来源、依赖或兼容性，不表示背书，代码许可也不授予商标权。新增素材应先记录来源与分发许可；来源未知就标注不确定，不凭相似率或猜测指定许可。更新归档与声明必须可复现，并保留所有仍适用的版权和许可内容。
