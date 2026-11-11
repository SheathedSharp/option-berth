# 引擎来源

引擎的历史代码来源为 `github.com/raskrebs/sonar`（Nicolai Thomsen，仓库创建于 2026-03），
仓库记录的参考提交是 `be48afbee89fd2381d0e2c8b0aa9ec1d81194546`。原版权为
`Copyright (c) 2026 RasKrebs`，完整 MIT 声明记录在
[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)，并随发布产物分发。
项目贡献按根目录 [MIT](../LICENSE) 提供，不取代上游作品的版权。

当前设计由本项目的 `worktree → manifest → service → run → runtime facts`
对象模型驱动。Sonar 是参考与历史来源，不是后续架构必须沿用的骨架。
成熟数据库及 OS 适配可以保留；不以改名、包装、行数或来源比例证明原创性或性能。

## 来源边界

[`scripts/audit-upstream-origin.py`](../scripts/audit-upstream-origin.py) 对照上游检出
量出 `engine/` 还复制了多少代码：同一相对路径下连续 5 行以上实质相同的代码算一段复制；
空行、括号行以及 `package` / `import` 行不计，它们在任何 Go 文件里都会相同。

2026-11-11 在已合并的 `main`（`589b17a5`，PR #270）和后续验收分支
`test/acceptance-final-20261111`（`5ecf1f23`）上，对照两棵固定上游树：

| 上游基线 | 提交 | 日期 | 含复制段的文件 | 复制段 | 复制行 |
| --- | --- | --- | ---: | ---: | ---: |
| sonar `main` | `26dbcaea9add181e7001756518921807f4755ef1` | 2026-11-06 | 142 | 685 | 9,622 |
| 先前参考 | `f4c50506b09502642cfedf3f4f6a3031cf80e9f2` | 2026-10-15 | 142 | 685 | 9,633 |

两棵树的唯一差异是 `internal/scanner/config_test.go`（`f4c50506` 多 11 行：38 行对
27 行）。仓库记录的参考提交 `be48afbee89fd2381d0e2c8b0aa9ec1d81194546` 在 `main`
上对照为 9,671 行 / 690 段。后续验收分支没有增加复制行：它把 killer 测试清理改为
持有进程组、给已回收命令加边界，复制行降到 **9,610**（对照 `26dbcae`）与
**9,621**（对照 `f4c50506`）、**9,659**（对照 `be48afbe`），生产 3,852、
测试 5,758。

按包（对照 `26dbcae`，后续分支）：

| 包 | 生产 | 测试 | 合计 |
| --- | ---: | ---: | ---: |
| `internal/daemon` | 2,073 | 3,457 | 5,530 |
| `internal/cmd` | 837 | 703 | 1,540 |
| `internal/groups` | 336 | 506 | 842 |
| `internal/scanner` | 220 | 540 | 760 |
| `internal/ports` | 288 | 389 | 677 |
| `internal/killer` | 86 | 158 | 244 |
| `internal/store` | 12 | 5 | 17 |

`internal/claims`、`internal/display`、`internal/testenv` 与 `internal/store/claims*.go`
已经归零；`internal/docker` 与 `internal/ports/displayname.go` 只剩结构体字段与表驱动
用例数据。`internal/store` 已整包重写：生产代码从 1,076 行降到 199 行。

```sh
git clone https://github.com/raskrebs/sonar /tmp/sonar
git -C /tmp/sonar checkout 26dbcaea9add181e7001756518921807f4755ef1
python3 scripts/audit-upstream-origin.py --upstream /tmp/sonar
```

仍有复制段时审计以 1 退出，因此退出码就是「上游声明是否可以移除」的判据。

## 上游基线核验

`26dbcae` 在 2026-11-11 的 `git ls-remote` 中同时是 sonar 的 `HEAD` 与
`refs/heads/main`；其发布 tarball 的 SHA256 为
`3c48a85985dc020331b79ebd70e79014e671d631d994b4d5890bad91e5382bcd`。`f4c50506`
由 GitHub commit API 返回的提交与 tree 核验，tarball 的 SHA256 为
`7a0f31032682b03de888158dcc006eb736de7bec0bcab91586c06829fbade528`。两棵树解包后
都与提交 tree 逐 blob 比对：636/636 与 634/634 个文件逐字节一致，没有缺失或多余文件。

## 何时可以移除上游声明

只有审计输出归零，才能从 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) 移除
Sonar 声明。判据是复制段归零，不是文件改名、重新格式化、重写注释，也不是只重写部分
逐字节相同的文件：这些做法都不改变审计结果。

自实现按模块推进：先在现有边界上建立行为对照（`go test` 与真实服务场景），再用本项目的
对象模型重写该模块并整体删除旧实现，不保留包装层；模块合并后重跑审计，数字必须下降，
否则这次重写不算数。

## 维护者确认

2026-11-11，仓库维护者以书面任务指令确认：

- 以上双基线审计与两个固定提交是当前来源审计（X1）的依据；9,610 / 9,621 行不代表
  原创性已经完成。
- 审计归零之前，[THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) 中的 Sonar 声明
  不得删除；也不能用「比历史数字低」代替「归零」。
- 已交付的测试与结构样板不要求为降低数字重写；新改动不得增加复制行。

这份记录来自维护者在 2026-11-11 任务中的书面指令；合并前在 PR 中再次确认后生效。

产品与实现分别见 [product.md](../docs/product.md) 和
[architecture.md](../docs/agent/architecture.md)，实际验证范围见
[validation.md](../docs/agent/validation.md)。名称为 option-berth，CLI 为 `oberth`，
清单为 `oberth.yaml`，环境变量前缀为 `BERTH_`。

隐私清理、重构或建立独立 Git 历史，均不得删除适用的来源、版权与许可。
