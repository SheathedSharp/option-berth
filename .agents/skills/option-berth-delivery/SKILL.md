---
name: option-berth-delivery
description: >-
  本仓库的交付流程：主题分支推送到 origin，开远端 PR，等本 PR 的 required checks
  通过并经用户确认后，用 merge commit 合并。用于开始交付、合并改动、或考虑是否直接
  操作 main 时。发布提交与标签不走这条流程。
---

# option-berth 远端 PR 交付

本仓库把合并前移到远端：改动必须在 `origin` 上以 PR 形式通过检查后再合并。全局
`git-workflow` 的本地 `--no-ff` 合并步骤在这里被替换为远端 PR 的 merge commit；分支、
worktree、提交信息与用户确认的规则不变。

## 适用范围

任何要进入 `main` 的改动：feat、fix、docs、ci、chore。发布提交、标签与发布脚本的原子
推送是例外，见 [docs/releasing.md](../../../docs/releasing.md)。

## 流程

1. 在独立 worktree 的主题分支上完成改动（全局 `git-workflow`）。本地验证按
   [docs/develop.md](../../../docs/develop.md) 执行，输入与结果如实记录；没跑的检查写清楚。
2. 一个主题一个提交，提交信息按全局 `git-commit`（中文 `type(scope): 主题`）。
3. 推送分支：

   ```sh
   git push -u origin <branch>
   ```

4. 开 PR。正文写清实际执行过的验证、结果和未跑项：

   ```sh
   gh pr create --base main --head <branch> \
     --title "<subject>" --body "<验证输入与结果；未跑项>"
   ```

5. 等检查。本 PR 的所有 required checks 都必须通过；清单以 `main` 的 ruleset 为准，
   仓库内的唯一清单是 `.github/quality-gate-checks.json`：

   ```sh
   gh pr checks <branch> --watch
   ```

   失败时按实际日志修复并重新推送；不绕过检查、不关闭 workflow、不放宽超时、
   不把旧提交的绿灯当成本次结果。核心 race 与真实服务场景由每日
   `Nightly full verification` 与发版流程覆盖，PR 通过不等于这两项已跑。修改
   workflow、CODEOWNERS、race 清单或发布/检查脚本时，`Gate integrity` 要求 PR 作者
   是仓库维护者（`OWNER` / `MEMBER` / `COLLABORATOR`），不要尝试绕过。
6. 得到用户明确确认后才合并：

   ```sh
   gh pr merge <branch> --merge --delete-branch
   ```

   用 merge commit，不 squash、不 rebase，除非用户明确要求。不要在本地把主题分支
   合入 `main` 后直接 `git push origin main`。
7. 同步并清理：

   ```sh
   git fetch origin
   git switch main
   git pull --ff-only
   git worktree remove <worktree-path>
   git branch -d <branch>   # 远端分支已由 --delete-branch 删除
   ```

## 禁止

- 不在本地合并后直接推送 `main`；这会绕过 PR 上的自动门槛。
- 不 force push、不删除 `main`、不修改 required status checks 或分支保护。
- 不把多个主题塞进一个 PR；不替用户决定合并时机。
- 不把 PR 绿灯当成完整验收：发版前仍按 [docs/validation.md](../../../docs/validation.md)
  与 [docs/releasing.md](../../../docs/releasing.md) 执行完整门槛。

## 发布

发布和普通改动走同一条 PR 路径：版本 PR 只改 `VERSION`（协议版本发布同时改
`ProtocolVersion` 与生成的 schema），合并后在合并提交上创建注释标签 `vX.Y.Z` 与
`engine/vX.Y.Z` 并推送。`Verified release` 在 runner 上跑完整门槛，通过后才构建和发布资产。
本机不再有创建版本提交、打标签或推送的快捷入口；`scripts/release.py` 只保留
`--verify-only` 与 `--validate-tag`。打标签仍是发布动作，需要单独授权。
