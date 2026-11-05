# Engine website / 引擎官网

静态中英产品介绍，只讲 `oberth` 引擎：为什么不再靠猜、清单与运行事实、常用命令、
安装包与边界。页面不连接 daemon，不使用遥测、外部字体、图片服务或运行时框架。
图标、浅色字标与深色字标的站点副本在 `site/assets/`；正文、语言切换和下载入口在禁用
JavaScript 时仍可用，舞台切换与复制按钮在无脚本时隐藏而不是留下死控件。

产品界面与给 coding agent 的 skill 是独立产品，分别在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) 与
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)。本仓库不再保存
macOS 应用截图或采集脚本；旧页面里的截图仍留在 Git 历史中，供客户端仓库按需取用。

## Build and preview

```sh
python3 site/build.py
python3 -m http.server 8765 --bind 127.0.0.1 --directory site/dist
```

构建只复制白名单资产（`icon.svg`、`mark.svg`、`mark-dark.svg`、`boot.js`、`site.css`、
`site.js`、`guide.css`、`guide.js`），并检查中英两份文案的键集合一致。中文版在 `/`，英文版在
`/en/`；指南在 `/guide/` 与 `/guide/en/`；语言切换是普通链接，不依赖客户端翻译。`dist/` 是
可丢弃产物，不进入源码提交。

页面用深色品牌变体作为底色，陶土色只出现在少数可操作元素和品牌标记上。字体使用系统字族：
站点 CSP 只允许 `self`，README 也约定不加载外部字体，因此没有引用 `design-taste-frontend`
方向单里的 Albert Sans；`mark-dark.svg` 是品牌深色字标，保证深色底上两条轨仍可读。

## Guide book（`/guide/`）

`site/guide/` 是给人读也给机器读的指南书，不替代 `/` 的产品页。`book.json` 是唯一的目录：
章节顺序、所属部分、标题、一句摘要和契约链接。每章有两份 HTML 片段
（`<slug>.zh.html` / `<slug>.en.html`）；构建用 `templates/guide.html` 套页面骨架，
用 `templates/guide-chapter.html` 套章节的左栏、正文和右栏。

构建会拒绝：两份片段的结构不一致（标题层级、表格行、列表项、代码块数量或代码文本不同）、
章节缺少 h2 id、同页 id 重复、缺少对应语言的片段、正文残留 `{{guide}}` / `{{root}}`，
以及 dist 中任何相对链接指向不存在的文件。

`site/assets/guide.css` 只加书籍版式，调色板与字体来自 `site.css`；视觉母题是品牌标记的
两条轨与陶土胶囊——首页的链、章节左栏的五站、标题下的胶囊标记都是同一个形状。
`guide.js` 在 `site.js` 之前加载：先给阅读块加 `reveal`，再交给站点已有的 observer；
它只驱动链的五站切换、本页目录高亮和移动端目录折叠。

机器入口在 dist 根目录：`llms.txt`（英文优先的链接索引，含中文指南）、`llms-full.txt`
（全部英文章节的纯文本）和 `sitemap.xml`。它们和 HTML 从同一份片段生成，不手写；
`robots.txt` 带一行 `# Agent guide:`。章节页的 JSON-LD 走 CSP hash
（`script-src 'self' 'sha256-…'`），内联结构化数据不会破坏零 console error 断言。

加一章：在 `book.json` 里加条目（slug、part、station、两种语言的 nav/title/lede/summary、
refs），再写两份片段；构建会检查结构镜像。改 part 或顺序只改 `book.json`，侧栏、首页目录、
`llms.txt` 和 `sitemap.xml` 都会跟着生成。

## Browser verification

```sh
cd site
npm ci --ignore-scripts
npm test                       # 本机已安装的 Google Chrome
CI=1 npx playwright install --with-deps chromium webkit
CI=1 npm test                  # CI 两种浏览器引擎
```

回归覆盖两种语言、360/390/768/1440px、图片加载、无横向溢出、命令过滤与空状态、Escape
重置、无 JavaScript 的基本可用性、减弱动态效果的初始值/实时变化，以及结尾的三产品链接。
指南另有 24 个页面的语言、标题、横向溢出与 console error 检查，链的五站切换（指针与键盘）、
无脚本时五个面板全部可读、语言切换落到对应章节，以及 `llms.txt` / `llms-full.txt` /
`sitemap.xml` 覆盖每一章。测试只启动 localhost 静态服务器；生产网站不会请求 npm、外部脚本
或跟踪服务。

## Content evidence

页面里的清单、状态与日志取自 `examples/hello-worktree` 的真实运行：隔离 `HOME`/`BERTH_HOME`、
自动端口、无端口 worker，结束后由 `down` 回收。截图与日志中的临时路径已替换为演示值；
端口 `21730`、健康 `200` 与无端口 worker 是这份演示的输出。四事实卡片把同一次运行里的
进程、端口、健康、退出拆开讲，并用无端口 worker 说明“没有监听不等于没在运行”。清单、命令与平台边界以
[docs/cli.md](../docs/cli.md)、[docs/architecture.md](../docs/agent/architecture.md) 和
[docs/validation.md](../docs/agent/validation.md) 为准。

首页的实测区（`proof`）不是示意数字，也不是手写常量。它读 `site/data/eval.json`，这份快照由
`scripts/export_site_eval.py` 从一次 merged report 生成并提交，`site/build.py` 在构建时校验：
缺文件、schema 不对、缺字段、没有 run URL 都会让构建失败。刷新一次快照：

```sh
# 1. 手动跑一次 agent eval（workflow_dispatch）
# 2. 下载该 run 的 agent-eval-merged-* artifact，取出 report.json
python3 scripts/export_site_eval.py report.json \
  --out site/data/eval.json \
  --run-url https://github.com/SheathedSharp/option-berth/actions/runs/<id> \
  --suite large
python3 site/build.py && (cd site && npm test)
```

快照 schema 是 `oberth.site-eval/v1`，只包含页面需要的事实：测量时间、run URL、agent/模型、
repeat、场景数、两个臂的完成数/轮次 P50/P95/时长中位/OOB，以及「轮次更少的场景数」。CI 的
report 结构变化时只改 exporter 与它的测试，页面的数据契约不变；页面本身不按 provider 组织
文案，README 与快照记下确切条件，是为了让这组数字可复核，而不是把它外推成对所有模型都成立。

当前快照来自 2026-11-04 的 large 矩阵：8 个十服务栈场景 × 2 种方式 × 5 次重复 = 80 次运行，
同一 agent（Codex CLI 0.166）、同一模型（`gpt-6-luna`）、同一二进制；0 个 agent error、
0 个 harness error，覆盖率 8/8。结果：完成任务 40/40 对 29/40，轮次中位 5 对 9（P95 10 对
17），用时中位 45s 对 69s；8/8 场景轮次更少，两个臂 OOB 都是 0。单一 agent、单一模型、内部
夹具，每格 n=5，P95 只作方向性证据；测量方法与门槛见
[docs/agent/agent-eval.md](../docs/agent/agent-eval.md)，原始报告保存在对应 CI run 的
`agent-eval-merged-*` artifact 里。

## Deploy

`Product website` workflow 在 PR 中只构建和验证；合并到 main 后才将 `site/dist` 发送到
GitHub Pages。仓库 Pages 的发布源为 GitHub Actions。失败不会部署，部署不需要私有令牌。
下载入口指向官方 `releases/latest`，只承诺引擎 CLI 压缩包；不猜测版本，也不跳过
SHA256SUMS 校验说明。

### 自定义域（`oberth.zayju.de`）

公开站点由 GitHub Pages 提供，**不使用 Cloudflare Pages**。仓库 Settings → Pages 的 Custom
domain 设为 `oberth.zayju.de`，DNS 里 `oberth` 的 CNAME 指向 `sheathedsharp.github.io`。
GitHub 的自定义域证书签发前，这条记录暂时开启 Cloudflare 代理以提供有效 TLS（Cloudflare
只做 DNS/代理，不构建、不使用 Pages）；证书就绪后可关掉代理改为 DNS only，让 GitHub 直接
终止 TLS。`Product website` 工作流合并到 `main` 后把 `site/dist` 发布到 GitHub Pages，
这一步同时更新公开站点。

`SITE_URL` 默认是公开域名 `https://oberth.zayju.de/`，构建出的 canonical、`og:url`、
`hreflang`、`robots.txt` 与 `sitemap.xml` 都指向它。要在本地或预览站使用 GitHub Pages 的
原始地址，构建时显式传：

```sh
SITE_URL=https://sheathedsharp.github.io/option-berth/ python3 site/build.py
```

GitHub Pages 的软限制是每月 100 GB 流量与每小时 10 次构建；这个静态站只包含 11 个文件，
不需要额外付费。
