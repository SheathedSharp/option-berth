# Third-party notices

option-berth 的自研代码由 SheathedSharp 及项目贡献者维护，适用根目录 [MIT](LICENSE)。第三方材料保留各自许可，不因本项目采用
MIT 而重新许可。源码与发布包均保留适用声明；发布包还附带目标实际运行依赖的许可原文与构建清单。

## 历史代码来源

引擎历史代码来源为 `github.com/raskrebs/sonar`，原版权
`Copyright (c) 2026 RasKrebs`，MIT 全文见 [engine/LICENSE](engine/LICENSE)。
根许可证保留该版权；设计与来源边界见 [engine/UPSTREAM.md](engine/UPSTREAM.md)。
保留来源不表示后续架构基于该项目，也不将其已有作品宣称为本项目原创。

## Monaspace 字体

`client/macos/Fonts/MonaspaceNeon-*.otf` 保持未修改的字体文件，适用 SIL Open Font
License 1.1。原版权、保留字体名和许可全文见
[LICENSE-Monaspace.txt](client/macos/Fonts/LICENSE-Monaspace.txt)。
分发时必须附带原版权和 OFL，不将字体改标 MIT，不单独销售字体，不擅用保留名命名修改版。

## Go 依赖

实际版本由 [engine/go.mod](engine/go.mod) 与 [engine/go.sum](engine/go.sum) 固定。
源码通过模块依赖引用这些库，没有将模块缓存或编译结果作为本仓库内容分发。

当前原生引擎构建引用的19个外部模块，其许可与 NOTICE 原文汇总在
[go-runtime-licenses.txt](third_party/go-runtime-licenses.txt)，包括相关嵌入式第三方许可。
这份清单限定于原生运行依赖，不冒充所有可选、测试和未验收平台的完整物料清单。
发布二进制、安装包或增加依赖时，应重新根据实际产物核对模块、版权及 NOTICE，并随产物分发。

## 名称、商标与素材

第三方名称仅用于说明兼容、依赖和来源，不表示认可或背书。代码许可证不授予第三方
商标权。新增素材必须记录来源及分发许可；项目图形资产与字体的许可分别处理。
