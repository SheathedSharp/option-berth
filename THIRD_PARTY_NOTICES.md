# Third-party notices

option-berth 的自研代码由 SheathedSharp 及项目贡献者维护，适用根目录 [MIT](LICENSE)。第三方材料保留各自许可，不因本项目采用
MIT 而重新许可。源码与发布包均保留适用声明；发布包还附带目标实际运行依赖的许可原文与构建清单。

## 历史代码来源

引擎历史代码来源为 `github.com/raskrebs/sonar`，原版权
`Copyright (c) 2026 RasKrebs`，MIT 全文见 [engine/LICENSE](engine/LICENSE)。
根许可证保留该版权；设计与来源边界见 [engine/UPSTREAM.md](engine/UPSTREAM.md)。
保留来源不表示后续架构基于该项目，也不将其已有作品宣称为本项目原创。

## 客户端资产

macOS 客户端及其字体（Monaspace，SIL Open Font License 1.1）与终端依赖（SwiftTerm，MIT）
随独立仓库 [option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) 分发；
相应的原版权与许可原文由该仓库保留。本仓库的源码与发布包不包含这些资产，也不把它们
重新许可为 MIT。

## Go 依赖

实际版本由 [engine/go.mod](engine/go.mod) 与 [engine/go.sum](engine/go.sum) 固定。
源码通过模块依赖引用这些库，没有将模块缓存或编译结果作为本仓库内容分发。

原生引擎运行依赖的许可与 NOTICE 原文汇总在
[go-runtime-licenses.txt](third_party/go-runtime-licenses.txt)，包括相关嵌入式第三方许可。
其中 `golang.org/x/sync/semaphore` 复用原有 v0.20.0 模块版本，为镜像变更锁提供有界可取消交接，适用 Go Authors 的 BSD 许可。
这份清单限定于原生运行依赖，不冒充所有可选、测试和未验收平台的完整物料清单。
发布二进制、安装包或增加依赖时，应重新根据实际产物核对模块、版权及 NOTICE，并随产物分发。

## 名称、商标与素材

第三方名称仅用于说明兼容、依赖和来源，不表示认可或背书。代码许可证不授予第三方
商标权。新增素材必须记录来源及分发许可；项目图形资产与字体的许可分别处理。
