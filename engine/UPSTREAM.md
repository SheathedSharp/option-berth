# 引擎来源

引擎的历史代码来源为 `github.com/raskrebs/sonar`，仓库记录的参考提交是
`be48afbee89fd2381d0e2c8b0aa9ec1d81194546`。原版权为
`Copyright (c) 2026 RasKrebs`，完整 MIT 许可保留在 [LICENSE](LICENSE)。
项目贡献按根目录 [MIT](../LICENSE) 提供，不取代上游作品的版权。

当前设计由本项目的 `worktree → manifest → service → run → runtime facts`
对象模型驱动。Sonar 是参考与历史来源，不是后续架构必须沿用的骨架。
成熟数据库及 OS 适配可以保留；不以改名、包装、行数或来源比例证明原创性或性能。

产品与实现分别见 [product.md](../docs/product.md) 和
[architecture.md](../docs/architecture.md)，实际验证范围见
[validation.md](../docs/validation.md)。名称为 option-berth，CLI 为 `oberth`，
清单为 `oberth.yaml`，环境变量前缀为 `BERTH_`。

隐私清理、重构或建立独立 Git 历史，均不得删除适用的来源、版权与许可。
