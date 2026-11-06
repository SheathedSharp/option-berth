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

2026-11-06 在 `refactor/engine-daemon-rewrite`（基于 `main` 的 `7aa022e2`）上对照
sonar `main`（`f4c50506b09502642cfedf3f4f6a3031cf80e9f2`，2026-10-15）的结果：

| 项 | 数量 |
| --- | --- |
| 与上游同相对路径的 Go 文件 | 314 |
| 其中逐字节相同 | 11 个文件 / 483 行 |
| 含复制段的文件 | 168 |
| 复制段 | 951 |
| 复制行 | 15,229 |

`internal/claims`、`internal/display`、`internal/testenv` 与 `internal/store/claims*.go`
已经归零；`internal/docker` 与 `internal/ports/displayname.go` 只剩结构体字段与表驱动
用例数据。`internal/store` 已整包重写：生产代码从 1,076 行降到 199 行。

```sh
git clone --depth 1 https://github.com/raskrebs/sonar /tmp/sonar
python3 scripts/audit-upstream-origin.py --upstream /tmp/sonar
```

仍有复制段时审计以 1 退出，因此退出码就是「上游声明是否可以移除」的判据。

## 何时可以移除上游声明

只有审计输出归零，才能从 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) 移除
Sonar 声明。判据是复制段归零，不是文件改名、重新格式化、重写注释，也不是只重写
37 个逐字节相同的文件：这些做法都不改变审计结果。

自实现按模块推进：先在现有边界上建立行为对照（`go test` 与真实服务场景），再用本项目的
对象模型重写该模块并整体删除旧实现，不保留包装层；模块合并后重跑审计，数字必须下降，
否则这次重写不算数。

产品与实现分别见 [product.md](../docs/product.md) 和
[architecture.md](../docs/agent/architecture.md)，实际验证范围见
[validation.md](../docs/agent/validation.md)。名称为 option-berth，CLI 为 `oberth`，
清单为 `oberth.yaml`，环境变量前缀为 `BERTH_`。

隐私清理、重构或建立独立 Git 历史，均不得删除适用的来源、版权与许可。
