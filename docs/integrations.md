# 接入编排工具与开发服务器

option-berth 是 **worktree 的运行事实层**。编排工具负责创建工作区、调度会话，也常常分配自己的
端口；`oberth` 负责回答这份代码上的服务现在是什么状态、失败为什么失败，并在结束时停止它留下的
服务。

接入方式是**用户在自己的脚本里调用 CLI**：工具提供 setup / run / cleanup 槽位的，把 `oberth`
命令粘进去；没有槽位的，用外层 wrapper 或 `AGENTS.md` 说明。这些脚本和配置都由用户自己创建；
option-berth 不安装 hooks、不注册 MCP、不修改任何工具的配置，也不分发集成代码；接口仍然只有
CLI 与 `--json`。

## 版本前提

本页的清理步骤用到 `down --worktree`、`down --identity` 和 `worktrees`，从 v1.3.0 起提供。
更早的版本在删除 checkout 目录**之前**进入该目录执行 `oberth down`。

## 三条命令

### 创建后：启动并等待就绪

```sh
cd /path/to/checkout
oberth up --wait
```

- `--wait` 等到本次选择的服务真正监听；声明了 `health:` 的服务还要通过健康检查；无端口 worker
  以运行记录活跃为准。默认等待 30s，慢的就绪过程用 `--wait-timeout` 放宽。
- `--wait-timeout` 只约束**启动之后**的监听与健康等待，不约束 `prepare:`。依赖安装慢要靠
  `prepare_inputs:` 缓存（内容未变时跳过），或调大编排工具自己的脚本超时。
- 失败时退出码为 1，逐服务给出 `state`、`reason` 和 `hint`；只有进程真的起过，`hint` 才指向
  `oberth logs <service> --once`。
- 观察失明时 `up --wait` 会立即返回 `observation_*` 错误并保留刚启动的运行。这不是“没起来”。
  按 `hint` 在普通终端运行 `oberth serve --detach` 后再试；不要在沙箱里反复重试。
- `up` 默认拒绝服务工作目录在 `$HOME` 之外。编排工具把 workspace 放在 home 外时，确认后加
  `--allow-outside-home`；`down` 没有这个开关。
- 清单要由人确认：`oberth init`，或 `oberth init draft` 起草、人 `oberth init adopt` 采纳。
  agent 的草稿不等于授权。

### 运行中：读事实

```sh
oberth status --json
oberth logs <service> --once --json
```

`status --json` 里与编排最相关的是：

- `worktree.services[]`：`pid`、`run_id`、`port_actual`、`running`、`health_status`、
  `last_exit.reason`。逐服务的下一步提示来自 `up --json` 的 `services[].hint`，不是 `status`。
- `worktree.identity`（`wt_…`）和 `scope.root`：停止与核对 checkout 根路径用。
- 顶层 `observation`：监听表不可信时出现，带 `state`、`code`、`detail`、`hint`。
  `observation_*` 都不是“没有监听”，更不是“没在运行”。

不要用 `ss` / `lsof` / `/proc` 的即时输出替代这些字段；它们没有运行归属，也无法区分
“看不见”和“没在跑”。

### 结束：按 worktree 停

```sh
oberth down                                         # 目录还在，使用最近清单
oberth down --worktree /absolute/path/to/checkout   # 按 checkout 根路径
oberth down --identity wt_...                       # 按身份，目录已删除时
```

- `--worktree` 要的是 **checkout 根路径**，不是子目录；从 `status --json` 的 `scope.root`
  或 `git rev-parse --show-toplevel` 取。
- 目录已经删除时，daemon 仍能按身份账本停止它留下的运行与监听，并交还 `port: auto` 预留；
  前提是 daemon 之前见过这个 checkout。
- `down` 只有在确认停止后才交还预留。取消、停止不完整、动作后观察失败、仍有登记运行/监听或
  预留已被刷新时，它会保留预留并报错；不要把错误当成已经释放。
- 目标已经没有任何运行或预留时，显式身份仍是一次成功的空停止。
- `oberth worktrees --gone` 只列出 daemon 记过的身份，不是孤儿进程的全量清单。账本里没有清单
  的行没有可停的服务组，`down --identity` 会返回 `not_found`。
- 两个 checkout 占用同一组名时，组作用域的停止返回 `ambiguous`；此时按端口或 pid 点名
  `kill`，不要替调用者猜一个。

## 通用接法

| 工具的槽位 | 放什么 |
|---|---|
| 创建 / setup | `oberth up --wait`（需要机器读输出时加 `--json`） |
| 运行 / run | 不要再启动同一批服务。`oberth` 起的服务归 daemon，工具停止 run 脚本不会停它们 |
| 归档 / 删除 / cleanup | `oberth down --worktree <checkout 根>`；没有该能力时在删除前进入目录 `oberth down` |
| 只有单一 run 槽位 | 把启动放 setup；不要用 run 槽位伪装 setup，否则工具会按住或杀掉它 |

几个容易踩的点：

- **PATH**：setup / hook 往往不加载你的 shell rc。用 `oberth` 的绝对路径，或在脚本里显式设置
  `PATH`；本页示例按 README 的默认安装位置补 `~/.local/bin`，装在别处就换成实际路径。同一个
  环境会传给服务的 `cmd:`；只让 `oberth` 可执行还不够，`node`、`npm`、`python3` 也必须在
  PATH 上。启动后用 `oberth logs <service> --once` 复核。
- **工作区根不等于 Git checkout 根**：`up` 从 cwd 找最近的 `oberth.yaml`。工具的工作区目录
  如果包含多层目录，先 `cd "$(git rev-parse --show-toplevel)"`，并把同一个根传给
  `down --worktree`。
- **不要写死 `PORT`**：`port: auto` 的服务由引擎分配端口并通过环境注入 `PORT`。服务自己的
  `env:` 优先级高于注入的 `PORT`，所以 `env: PORT: 3000` 会覆盖分配值，导致 `up --wait`
  等错端口。用 `${port}` 引用，或 `env: { PORT: "${port}" }`。
- **严格端口**：框架如果会在端口被占时悄悄换一个，`up --wait` 等的是清单里的端口，最终会以
  `ready_timeout` 失败（并且只清理本次新启动的运行）。优先使用框架的严格端口选项，让冲突
  直接变成一条可读的失败事实。

## 工具配方

以下配置都由用户自己创建；项目级配置（`.conductor/settings.toml`、`.cursor/worktrees.json`、
项目 `.claude/settings.json`、`.codex/`）可以随仓库提交，用户级配置（如 `~/.codex/hooks.json`、
用户级 Claude Code settings）留在本机。option-berth 不会写入这些文件。

这些脚本只在已经有人确认过 `oberth.yaml` 的仓库里启用；没有清单时 `up` 会失败，先运行
`oberth init` 并确认启动命令和端口。

### Conductor

Conductor 在仓库里的 `.conductor/settings.toml` 配置 `[scripts]`；个人覆盖放在
`.conductor/settings.local.toml`，用户级默认在 `~/.conductor/settings.toml`。setup 在工作区
创建、Git 检出之后运行，archive 在工作区归档前运行。

```toml
# <repo>/.conductor/settings.toml（用户自己写，随仓库提交）
"$schema" = "https://conductor.build/schemas/settings.repo.schema.json"

[scripts]
setup = 'PATH="$HOME/.local/bin:$PATH" oberth up --wait'
archive = 'PATH="$HOME/.local/bin:$PATH" oberth down --worktree "$CONDUCTOR_WORKSPACE_PATH"'
```

- 不要把 `oberth up --wait` 放进 `scripts.run.<id>`。那是 Run 按钮的 action，Conductor 在
  Stop 时向它的进程组发 SIGTERM；而 oberth 起的服务属于 daemon，不由这个 action 持有。
- setup / archive 从工作区目录执行；Conductor 为工作区创建 Git worktree，
  `CONDUCTOR_WORKSPACE_PATH` 通常就是这个 checkout 根。先确认它与 `oberth status --json` 的
  `scope.root` 一致，再把同一个值传给 `down --worktree`；不一致时用 `scope.root`。
- `CONDUCTOR_PORT` 是 Conductor 自己分配的一段 10 个端口。不要让同一个服务同时由两套机制
  分配端口：oberth 管理的服务用 `port: auto`；如果 Conductor 的预览要求绑定
  `CONDUCTOR_PORT`，把这个服务留在 oberth 清单之外，或只作为 `machine:` 只读引用。
- archive 是文档里唯一的清理入口，没有单独的删除 hook。Conductor 归档会打断正在运行的
  setup；归档 hook 是否总能在打断后执行，官方文档没有说明，重要工作区请自行复核。
- setup 失败是否阻止工作区创建、脚本超时是多少，官方文档没有说明。

### Cursor

Cursor 读取仓库里的 `.cursor/worktrees.json`，在它创建工作区时执行 setup 命令。命令既可以是
数组，也可以是指向 `.cursor/` 下脚本的相对路径；脚本形式更好控制引号和 PATH。

```json
{
  "setup-worktree-unix": "oberth-setup.sh"
}
```

Windows 的完整服务场景不在本仓验证范围，见 [验证说明](validation.md)；本页不提供 Windows
版本的 setup 脚本。

```sh
#!/bin/sh
# .cursor/oberth-setup.sh
set -eu
cd "$(git rev-parse --show-toplevel)"
exec /absolute/path/to/oberth up --wait
```

- Cursor 文档说命令在 worktree 中依次执行，但没有说明用哪个 shell、如何继承 PATH；脚本里
  用绝对路径，并显式 `cd` 到 Git 根。
- **没有 teardown hook。** Cursor 的自动清理会删除 worktree 目录；文档没有说明删除时会运行
  用户命令。删除前先执行 `oberth down --worktree <checkout 根>`；忘记时，删除后用
  `oberth worktrees --gone` 找身份，再 `oberth down --identity wt_…`（有上一节的版本前提）。
- setup 失败是否阻止 agent、是否重跑，官方文档没有说明；`oberth up --wait` 可安全重复执行。
- 调试时看 Output 面板的 `Worktrees Setup` 通道。

### Claude Code

Claude Code 有两条路，先选清楚：

**A. 不接管 worktree 创建（推荐）**。保留 Claude 自己的 `git worktree` 行为，把本页末尾的
`AGENTS.md` / `CLAUDE.md` 段落放进仓库，让 agent 在需要时先读 `oberth status --json`、
再用 `oberth up --wait`。如果你在外层脚本里创建 worktree，在脚本里启动和清理：

```sh
#!/bin/sh
set -eu
worktree=$(cd "$1" && pwd -P)
shift
export PATH="$HOME/.local/bin:$PATH"
trap 'oberth down --worktree "$worktree"' EXIT
cd "$worktree"
oberth up --wait --wait-timeout 120s
claude "$@"
```

`trap` 保证 `claude` 失败或被中断时也会清理；`oberth` 起的服务是 daemon 持有的，不会随
脚本退出自动消失。

**B. 让 Claude Code 的 WorktreeCreate 接管创建**。只有当你确实需要自动创建并启动时才用。
配置 `WorktreeCreate` 会**取代** Claude Code 默认的 `git worktree` 创建：`.worktreeinclude`、
基线与分支选择都不再自动处理，hook 必须自己创建目录，并把目录路径作为 stdout 的最后一个
非空行输出，其他输出走 stderr。非零退出会让创建工作区失败。

把下面的 hooks 写进项目级 `.claude/settings.json`（可提交）或用户级 settings；示例是项目级：

```json
{
  "hooks": {
    "WorktreeCreate": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "${CLAUDE_PROJECT_DIR}/.claude/hooks/worktree-create.sh",
            "timeout": 600
          }
        ]
      }
    ],
    "WorktreeRemove": [
      {
        "hooks": [
          { "type": "command", "command": "${CLAUDE_PROJECT_DIR}/.claude/hooks/worktree-remove.sh" }
        ]
      }
    ]
  }
}
```

```sh
#!/bin/sh
# worktree-create.sh；stdin 是 hook JSON，stdout 最后一行必须是 worktree 路径
set -eu
export PATH="$HOME/.local/bin:$PATH"
payload=$(cat)
name=$(printf '%s' "$payload" | jq -r .name)
root=$(git rev-parse --show-toplevel)
dir="$root/.claude/worktrees/$name"
git worktree add "$dir" >&2
cleanup() {
  oberth down --worktree "$dir" >&2 || true
  branch=$(git -C "$dir" rev-parse --abbrev-ref HEAD 2>/dev/null || true)
  git worktree remove --force "$dir" >&2 || true
  if [ -n "$branch" ] && [ "$branch" != "HEAD" ]; then
    git -C "$root" branch -D "$branch" >&2 || true
  fi
}
trap cleanup EXIT
cd "$dir"
oberth up --wait --wait-timeout 120s >&2
trap - EXIT
echo "$dir"
```

```sh
#!/bin/sh
# worktree-remove.sh
set -eu
export PATH="$HOME/.local/bin:$PATH"
payload=$(cat)
p=$(printf '%s' "$payload" | jq -r .worktree_path)
case "$p" in
  /*) ;;
  *) echo "worktree_path must be absolute" >&2; exit 1 ;;
esac
oberth down --worktree "$p" >&2 || exit 1
branch=$(git -C "$p" rev-parse --abbrev-ref HEAD 2>/dev/null || true)
common=$(git -C "$p" rev-parse --git-common-dir 2>/dev/null || true)
git worktree remove --force "$p"
if [ -n "$branch" ] && [ "$branch" != "HEAD" ] && [ -n "$common" ]; then
  git --git-dir="$common" branch -D "$branch" >&2 || true
fi
```

- 脚本放在 `.claude/hooks/` 并随项目提交；`${CLAUDE_PROJECT_DIR}` 指向会话开始的项目根。
- 这两个事件没有 matcher；即使写了也会被忽略。示例中的 `jq` 需要自行安装。
- `WorktreeRemove` 只对 `WorktreeCreate` 创建的 worktree 触发，而且 subagent / workflow agent
  结束时不会触发；用默认 `git worktree` 创建的 worktree 没有文档化的离开事件。
- hooks 以用户的完整权限运行，不在沙箱里；提交到仓库的 `.claude/settings.json` 会变成在用户
  机器上执行命令。交互会话有 workspace trust 确认；`-p` / SDK 会话没有这个确认。
- 不要用 `SessionEnd` 做清理：默认预算 1.5s，最多 60s，而且 `/clear`、`/resume` 也会触发。
- `WorktreeCreate` 只覆盖 Claude Code 自己创建的 worktree；其他编排工具创建的 checkout 不会
  触发它，那里要用工具自己的 setup / cleanup。

### Codex

Codex **没有** worktree-created / worktree-removed hook。最可靠的做法是外层 wrapper：

```sh
#!/bin/sh
set -eu
worktree=$(cd "$1" && pwd -P)
shift
export PATH="$HOME/.local/bin:$PATH"
trap 'oberth down --worktree "$worktree"' EXIT
cd "$worktree"
oberth up --wait --wait-timeout 180s
codex "$@"
```

`trap` 同样保证 Codex 失败时清理。不要让 `oberth up` 和 `codex` 都去启动同一批服务；启动只做
一次，Codex 通过 `oberth status --json` 读事实。

Codex 的 `SessionStart` / `SessionEnd` hook 是最近似的生命周期入口，但它们不是 worktree 事件，
而且以下行为在官方文档里没有说明，不能当作保证：

- `codex exec` / `codex exec --worktree` 是否运行这些 hook；
- hook 命令是否受 Codex 沙箱限制（如果受限，`oberth up` 可能写不了状态或绑不了本地端口）；
- hook 是否继承 `PATH`、`HOME`、`BERTH_HOME` 等环境；
- `SessionEnd` 的默认 1s / 最大 3s 是否够 `oberth down` 完成。

如果你接受这些未知，可以在用户级 `~/.codex/hooks.json` 里用 `SessionStart`（matcher
`startup|fork`）启动、用 `SessionEnd` 尽力清理；脚本里用 `oberth` 的绝对路径，并且不要把
`SessionEnd` 当成唯一的清理路径。项目级 `.codex/` 只在项目被信任时加载，`--worktree` 新建的
路径可能不在信任范围内，所以优先用户级。

ChatGPT 桌面版的 Codex 另有 Local environments 的 setup script（仅桌面应用，配置在项目的
`.codex/` 下）：它在新建 worktree 时自动运行，可以放 `oberth up --wait`；它没有对应的删除
hook，清理仍要在删除前执行 `oberth down`。

Codex 自己管理的 worktree（`codex --worktree`、`/worktree`）会按保留策略删除目录；官方文档
没有说明删除时运行用户命令。删除前执行 `oberth down --worktree <checkout 根>`，或用
`worktrees --gone` / `down --identity` 补停。

### 其他工具

只要工具有 setup / run / cleanup 槽位，就按「通用接法」粘三条命令。没有 cleanup 槽位时，
把清理放到外层脚本或删除前的最后一步；不要依赖删除目录本身来停止服务。

## 贴进 AGENTS.md / CLAUDE.md 的一段

把下面这段放进仓库根目录的 `AGENTS.md`（Claude Code 也读 `CLAUDE.md`，可按工具追加）。
它解决的是最常见的问题：agent 在端口被占时先跑 `ss`、直接执行 `python3 app.py`、翻 `/proc`，
很久之后才想到 `oberth`。

````md
## 本地服务

这个 worktree 的服务由 option-berth（`oberth`）管理。启动或排查前先用它读事实，不要先猜端口、
翻 `ps` / `lsof` / `/proc`，也不要直接运行 `npm run dev`、`python3 app.py` 一类命令。

- 先运行 `oberth status --json`。服务状态在 `worktree.services[]`：`last_exit.reason` 是最近
  失败原因，`health_status.reason` 是健康检查原因；监听不可信时读顶层 `observation`
  （`state`、`code`、`hint`）。
- 启动用 `oberth up --wait --json`。`--wait` 等到服务真正监听并通过声明的健康检查；无端口
  worker 以运行记录活跃为准。
- `oberth up --json` 的 `services[].hint` 是逐服务的下一步；`status --json` 没有这个字段。
- 失败时用 `oberth logs <service> --once --json` 读服务自己的输出，不要用等待超时或端口扫描
  代替原因。
- `observation.state` 不是 `observable` 时，明确说“看不见监听”，按 `observation.hint` 处理；
  不要把它当成“没在跑”，也不要声称服务健康。
- 停止当前 worktree 用 `oberth down`，或 `oberth down --worktree "$(git rev-parse --show-toplevel)"`。
  目录已删除时用 `oberth worktrees --gone` 找到 `wt_…`，再 `oberth down --identity wt_…`
  （账本行没有清单时就没有可停的服务组）。除非用户明确点名端口或 pid，不要按端口或全局
  进程名杀服务。
- 没有 `oberth.yaml` 时运行 `oberth init`，把需要人确认的字段留给用户；可以用
  `oberth init draft` 起草，但只有人确认后才运行 `oberth init adopt`。
- 不要启动或停止 `machine:` / `workspace:` 引用的服务；它们只读，也不属于这个 worktree
  的生命周期。

如果本机 `oberth down --help` 没有 `--worktree`，在删除 worktree 目录之前用 `oberth down`。
````

## 框架配方

`port: auto` 是大多数本地开发服务的正确选择：引擎为每个 worktree 预留不同端口，并把端口注入
`PORT`；`cmd:` 里的 `${port}` 在启动前按当前服务展开。`cmd:` 按 shell 风格拆成 argv，但不经过
shell，所以不要把整条命令包进 `sh -c`，除非你确实需要 shell 语义。

| 框架 | `cmd:` | 端口行为 |
|---|---|---|
| Vite | `npm run dev -- --port ${port} --strictPort` | 不读 `PORT`；没有 `--strictPort` 时会悄悄换端口。`--` 用于把参数转给 dev 脚本 |
| SvelteKit | `npm run dev -- --port ${port} --strictPort` | 开发服务器是 Vite，行为同上；要求生成项目的 dev 脚本是 `vite dev` |
| Next.js | `npx next dev -p ${port}` | 显式 `-p` 让端口占用直接失败；没有 `--strictPort`。只有端口来自默认值 3000 时才会自动换端口 |
| Nuxt | `npx nuxt dev --port ${port} --strictPort` | `--strictPort` 在端口不可用时退出，不换端口 |
| Astro | `npx astro dev --port ${port}` | 文档没有严格端口选项；端口占用时会尝试下一个端口，`up --wait` 会按清单端口超时 |
| Django | `python manage.py runserver 127.0.0.1:${port}` | 端口是位置参数；占用时直接报错退出，没有自动换端口 |
| Flask | `flask run --port ${port}` | 占用时直接报错；读 `FLASK_RUN_PORT`，不是 `PORT` |
| Rails | `bin/rails server -p ${port}` | 占用时直接报错；没有 `-p` 时读 `PORT` |
| Go | 应用自己的 flag / 环境变量 | `net/http` 没有标准端口参数；`go run . --port ${port}` 只在应用自己解析 `--port` 时成立 |
| Node / Express | `node server.js` | 应用自己读 `process.env.PORT` 时可直接用；否则把 `${port}` 传给应用自己的参数 |

一个完整的最小清单：

```yaml
name: web
services:
  - name: web
    cmd: npm run dev -- --port ${port} --strictPort
    port: auto
    health: /
```

`health: /` 适合根路径返回 2xx/3xx 的开发服务器；没有可用的健康路径时删掉这一行，就绪判定会
退化为端口监听。无端口 worker 不写 `port:`，用运行记录判断活跃：

```yaml
services:
  - name: worker
    cmd: python3 -u worker.py
    env:
      API_URL: ${api.url}
```

## 排障

| 现象 | 先看什么 |
|---|---|
| `up --wait` 报 `observation_*` | 当前进程看不见监听表，不是服务没起来。按 `hint` 在普通终端运行 `oberth serve --detach`，或运行 `oberth doctor --only listener_observation` |
| `up --wait` 超时 `ready_timeout` | 服务没有监听清单端口，或健康检查未通过。看 `oberth logs <service> --once`；框架悄悄换端口时改用严格端口选项 |
| 失败原因是 `port_occupied` | 固定端口被别的进程占用。停掉占用者或换端口；不要用 `pkill` 扩大目标 |
| `down --worktree` 返回 `not_found` | 传入的不是记录的 checkout 根，或账本行没有清单可停。用 `status --json` 的 `scope.root`；删除前优先 `oberth down` |
| `down` 返回 `ambiguous` | 两个 checkout 共用了同一组名。按端口或 pid 点名 `kill`，不要替调用者挑一个 |
| setup 里找不到 `oberth` 或 `node` / `npm` | 脚本没有加载 shell rc。用绝对路径或在脚本里设置 `PATH`；服务 `cmd:` 继承同一环境 |
| 服务在 home 外启动失败 | `up` 的默认限制针对服务工作目录。确认后加 `--allow-outside-home`；`down` 没有这个开关 |

## 验证边界

本页的 `oberth` 命令、字段和错误语义按本仓 [CLI 契约](cli.md)；第三方配置来自各工具在
2026-10-31 的官方文档。**没有**在 Conductor、Cursor、Claude Code、Codex 的每一种组合里实际
跑通这些脚本；工具升级后以官方文档为准。沙箱里的 agent 会话可能看不到监听表，这不是服务
失败，见 [验证说明](validation.md) 的观察边界。
