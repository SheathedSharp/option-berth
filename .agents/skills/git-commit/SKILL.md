---
name: git-commit
description: >-
  Write git commit messages for this repository. Use when creating or rewriting
  commits, amending messages, or when the user asks for a commit message, subject
  line, or to avoid jargon in git history.
metadata:
  version: "1.0.0"
  source: "github.com/SheathedSharp/option-berth"
---

# Git commit messages (option-berth)

Follow [AGENTS.md](../../AGENTS.md) at the repo root: **one theme per commit**, subject in Chinese as `type(scope): 主题`.

## Who reads the subject line

Assume the reader is **not** in the current chat: no roadmap codes, no sprint labels, no “we just finished X” without saying what X is in product terms.

## Do not put in the subject

- Internal milestone IDs (`M0`–`M3`, phase names from a plan doc) unless the commit *only* updates that doc’s checklist.
- Vague process words without a concrete object: 闭环、落地、对齐契约、native 验收（不说验了什么）、补齐（不说补了什么）。
- English slug chains: `ConsoleChecks green` → say what behavior or test suite in Chinese.

## Write the subject as

- **type(scope):** one clear outcome in plain language (feature, fix, or user-visible behavior).
- **scope:** `client`, `engine`, `docs`, `ci`, etc.—match the tree you touched.

## Use the body for

- **Why** (problem or intent), in 1–3 short sentences.
- **What** at the level a reviewer needs: components, CLI flags, test names—only if non-obvious from the diff.
- Pointers to `docs/*.md` when the commit implements a documented contract.

## Examples

**Bad subject**

```text
feat(client): 闭环会话输入条 M0–M3 与 Console 原生验收
```

**Better subject + body**

```text
feat(client): 会话输入条支持 Shell/Agent 分轨发送与侧栏进入

底部输入框 Enter 进 Shell，⌘↩ 续接 Agent PTY；侧栏可预览 Agent 输出并进入终端。
稳定 ConsoleChecks/ConfigurationChecks；BERTH_CONSOLE_TEST_QUIET 降低本地跑测抢焦点。
```

**Bad subject**

```text
fix(engine): 优化 diff
```

**Better subject**

```text
fix(engine): commit diff 超过 48 个文件时截断并返回元数据
```

## Before committing

1. `git status` / `git diff`—one theme; split if needed.
2. `git log -5 --oneline`—match existing style.
3. Draft subject; read it aloud as if posting to the team—if it needs “M2 是什么？”, rewrite.
4. Only commit when the user asked; never rewrite `git config` or force-push `main` without explicit request.
5. Branch first: `<type>/<scope>-<主题>`, using the `type` below. The rule and examples live in the
   [develop skill](../develop/SKILL.md) 「交付」—keep it in one place and don't restate it here.

## This repo’s types

| type | Use for |
|------|---------|
| feat | User-visible behavior or new capability |
| fix | Bug or incorrect behavior |
| perf | Faster or lighter without changing behavior |
| refactor | Same behavior, restructured |
| docs | Documentation only |
| test | Tests only, no product behavior change |
| chore | Tooling, deps, non-user-facing maintenance |
