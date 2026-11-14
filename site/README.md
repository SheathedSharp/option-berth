# Engine website / 引擎官网

`site/` 是 `oberth` 引擎的静态官网和指南，只讲引擎本身。中文产品页在 `/`，英文在 `/en/`；指南英文在 `/guide/`，中文在 `/guide/zh/`。页面不连接 runtime service，也不带遥测、外部字体或图片服务。唯一的第三方脚本是随站点同源发布的 GSAP 3.15.1，只用于动效，许可与来源记在仓库根的 `THIRD_PARTY_NOTICES.md`；CSP 仍然是 `script-src 'self'`。图标、浅色字标和深色字标的站点副本在 `site/assets/`。

禁用 JavaScript 时，正文、目录、语言切换和下载入口仍然可用。只有靠脚本才有意义的控件（产品页的舞台切换、指南的搜索、主题和抽屉）在没有脚本时不出现，而不是留下点了没反应的按钮。

产品界面和给 coding agent 的 skill 是独立产品，分别在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) 和
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)。本仓库不再保存
macOS 应用截图或采集脚本；旧页面里的截图还留在 Git 历史中，客户端仓库可以按需取用。

## 构建与预览

```sh
python3 site/build.py
python3 -m http.server 8765 --bind 127.0.0.1 --directory site/dist
```

构建只复制白名单资产（`icon.svg`、`mark.svg`、`mark-dark.svg`、`boot.js`、`gsap.min.js`、
`site.css`、`site.js`、`guide.css`、`guide.js`），并检查中英两份文案的键集合一致。

页面引用这些文件时一律带内容哈希（`assets/guide.css?v=a9af2b0e`）。GitHub Pages 给
`/assets/*` 的 `cache-control` 是四小时；不带哈希的话，改完样式后的几小时里，回访的读者会
拿旧 CSS 配新 HTML，页面看起来像样式全丢了。

`dist/` 是可丢弃产物，不进源码提交。语言切换是普通链接，不依赖客户端翻译。

## 产品页的版式

产品页是一张「泊位图」：12 栏满幅板面，8px 缝、16px 外边距，每块格子放一条真实事实
（状态面板、清单、JSON、命令表、四个运行事实、安装片段）。陶土色只留在字标里，操作色是
中性近白；实测区（proof）与结尾区（close）翻成浅色底，作为整页的节奏。任何一块格子的内容
都来自真实运行，不写示意数字。

页面只有一处真交互：状态面板上的 worktree 切换（`main` / `feature/ports`）。点一下，端口从
`21730` 变成 `21741`，GSAP 用遮片式动画换行；没有脚本时只显示第一个 worktree，切换按钮不出现
（`.js-only`）。动效全部走 `gsap.matchMedia()` 的 `prefers-reduced-motion` 分支，减弱动效时
格子不位移、切换瞬时完成。指南是一块阅读
界面，自带浅色和深色两套皮肤（默认跟随系统，可以手动切换），字标用浅色变体 `mark.svg`。
字体只用系统字族：站点 CSP 只允许 `self`，所以没有引用 `design-taste-frontend` 方向单里的
Albert Sans。`mark-dark.svg` 是品牌深色字标，保证深色底上两条轨仍然可读。

## 指南（`/guide/`）

`site/guide/` 是给人读也给机器读的文档站，不替代 `/` 的产品页。它有三栏：左侧是整本书的
目录（按部分分组，当前章高亮），中间是章节正文，右侧是「本页」小节导航、本章的契约入口和
机器入口。正文底部是上一章、下一章和「编辑本页」。

### 语言

英文是规范版本，在 `/guide/`；中文在 `/guide/zh/`。`book.json` 顶部的 `locales` 表决定有
哪些语言、各自的 `<html lang>`、`og:locale` 和 URL 前缀。第一项是默认语言，前缀为空。

加一门语言要做四件事：在 `locales` 里加一项，在 `labels` 和每一章的 `locales` 里补齐同一组
键，再为每章写一份 `<slug>.<id>.html`。构建会拒绝缺项、多出来的语言键，以及没有片段的章节。

### 目录

`book.json` 是唯一的目录：部分、章节顺序、每章的 nav（侧栏用的短名）、title、lede、summary，
以及契约链接。改顺序、加章、改标题都只动这个文件；侧栏、上一章、下一章、`llms.txt` 和
`sitemap.xml` 都由它生成。

### 构建时的检查

构建会拒绝这些情况：

- 同一章各语言的片段结构不一致：标题层级、表格行、列表项、代码块数量或代码文本不同。
- 标题缺少 id，或同一页里 id 重复。
- 片段里出现 h1；标题由 `book.json` 提供。
- 缺少某语言的片段，或正文残留 `{{guide}}`、`{{root}}` 占位符。
- dist 中任何相对链接指向不存在的文件。

同一仓库里 `docs/` 的双语文档和这里的双语片段是两条独立的产物。

### 版式与主题

`guide.css` 自带浅色和深色两套皮肤，默认跟随系统，顶栏的按钮可以手动切换。选择存在
localStorage 的 `oberth-theme` 里，由 `boot.js` 在首屏绘制前应用，不会先闪一下另一套配色。
站点不加载外部字体（CSP 只允许 `self`），两套皮肤都用系统字族。

### 搜索

构建在 `guide/search-<语言>.json` 写出全书的检索索引（每章的小节标题和正文）。页面用带
内容哈希的 URL（`?v=`）取用，避免浏览器拿旧索引配新章节。没有脚本时搜索按钮不出现，而不是
留一个点了没反应的控件。

### 旧地址

英文曾经在 `/guide/en/`。这些地址已经出现在 `llms.txt`、`sitemap.xml` 和别人的书签里，
所以构建会为每一章生成一个转发页，指向新的默认地址，而不是让它们 404。

## 机器入口

dist 根目录有三个机器入口，它们和 HTML 从同一份片段生成，不手写：

- `llms.txt`：英文优先的链接索引，含中文指南。
- `llms-full.txt`：全部英文章节的纯文本。
- `sitemap.xml`：两种语言的每个页面。

`robots.txt` 带一行 `# Agent guide:`。章节页的 JSON-LD 走 CSP hash
（`script-src 'self' 'sha256-…'`），内联结构化数据不会破坏零 console error 的断言。

## 浏览器验证

```sh
cd site
npm ci --ignore-scripts
npm test                       # 本机已安装的 Google Chrome
CI=1 npx playwright install --with-deps chromium webkit
CI=1 npm test                  # CI 两种浏览器引擎
```

回归覆盖两种语言、360/390/768/1440px、图片加载、无横向溢出、命令过滤与空状态、Escape
重置、worktree 切换、减弱动效时格子不位移、无 JavaScript 时的基本可用性，以及结尾的三产品链接。

指南另有这些检查：

- 全部 30 个章节页的语言、标题、横向溢出和 console error。
- 侧栏列出整本书，并且只标一个当前章。
- 上一章、下一章与书的顺序一致。
- 语言菜单落到对应章节。
- 主题按钮切换并跨刷新记住选择。
- 搜索能命中另一章的小节并跳到锚点，Esc 关闭，空查询不出结果。
- 每个代码块都有复制按钮。
- 无脚本时正文、侧栏和语言菜单仍可用，搜索、主题、抽屉按钮不出现。
- `/guide/en/` 旧地址转发到默认版本。
- `llms.txt`、`llms-full.txt`、`sitemap.xml` 覆盖每一章和每一种语言。

测试只启动 localhost 静态服务器。生产网站不请求 npm、外部脚本或跟踪服务。

## 内容证据

页面里的清单、状态和日志取自 `examples/hello-worktree` 的真实运行：隔离
`HOME`/`BERTH_HOME`、自动端口、无端口 worker，结束后由 `down` 回收。截图和日志里的临时
路径已替换为演示值；端口 `21730`、健康 `200` 和无端口 worker 都是这份演示的输出。四事实
卡片把同一次运行里的进程、端口、健康、退出拆开讲，并用无端口 worker 说明“没有监听不等于
没在运行”。清单、命令和平台边界以 [docs/cli.md](../docs/cli.md)、
[docs/agent/architecture.md](../docs/agent/architecture.md) 和
[docs/agent/validation.md](../docs/agent/validation.md) 为准。

首页的实测区（`proof`）读 `site/data/eval.json`，不是示意数字，也不是手写常量。这份快照由
`scripts/export_site_eval.py` 从一次 merged report 生成并提交，`site/build.py` 在构建时
校验：缺文件、schema 不对、缺字段、没有 run URL 都会让构建失败。刷新一次快照：

```sh
# 1. 手动跑一次 agent eval（workflow_dispatch）
# 2. 下载该 run 的 agent-eval-merged-* artifact，取出 report.json
python3 scripts/export_site_eval.py report.json \
  --out site/data/eval.json \
  --run-url https://github.com/SheathedSharp/option-berth/actions/runs/<id> \
  --suite large
python3 site/build.py && (cd site && npm test)
```

快照 schema 是 `oberth.site-eval/v1`，只包含页面需要的事实：测量时间、run URL、agent 和
模型、repeat、场景数、两个臂的完成数、轮次 P50/P95、时长中位、OOB，以及「轮次更少的场景
数」。CI 的 report 结构变化时只改 exporter 和它的测试，页面的数据契约不变。

页面本身不按 provider 组织文案。README 和快照记下确切条件，是为了让这组数字可复核，而不是
把它外推成对所有模型都成立。

当前快照来自 2026-11-04 的 large 矩阵：8 个十服务栈场景 × 2 种方式 × 5 次重复 = 80 次运行，
同一 agent（Codex CLI 0.166）、同一模型（`gpt-6-luna`）、同一二进制；0 个 agent error、
0 个 harness error，覆盖率 8/8。结果：完成任务 40/40 对 29/40，轮次中位 5 对 9（P95 10 对
17），用时中位 45s 对 69s；8/8 场景轮次更少，两个臂 OOB 都是 0。单一 agent、单一模型、内部
夹具，每格 n=5，P95 只作方向性证据。测量方法与门槛见
[docs/agent/agent-eval.md](../docs/agent/agent-eval.md)，原始报告保存在对应 CI run 的
`agent-eval-merged-*` artifact 里。

## 部署

`Product website` workflow 在 PR 中只构建和验证，合并到 main 之后才把 `site/dist` 发到
GitHub Pages。仓库 Pages 的发布源是 GitHub Actions。失败不会部署，部署不需要私有令牌。

下载入口指向官方 `releases/latest`，只承诺引擎 CLI 压缩包。它不猜版本，也不跳过
SHA256SUMS 校验的说明。

### 自定义域（`oberth.zayju.de`）

公开站点由 GitHub Pages 提供，**不使用 Cloudflare Pages**。仓库 Settings → Pages 的
Custom domain 设为 `oberth.zayju.de`，DNS 里 `oberth` 的 CNAME 指向
`sheathedsharp.github.io`。

GitHub 签发自定义域证书之前，这条记录暂时开着 Cloudflare 代理来提供有效 TLS
（Cloudflare 只做 DNS 和代理，不构建，也不用 Pages）。证书就绪后可以关掉代理改成
DNS only，让 GitHub 直接终止 TLS。`Product website` 工作流合并到 `main` 后把 `site/dist`
发布到 GitHub Pages，这一步同时更新公开站点。

`SITE_URL` 默认是公开域名 `https://oberth.zayju.de/`。构建出的 canonical、`og:url`、
`hreflang`、`robots.txt` 和 `sitemap.xml` 都指向它。要在本地或预览站用 GitHub Pages 的
原始地址，构建时显式传：

```sh
SITE_URL=https://sheathedsharp.github.io/option-berth/ python3 site/build.py
```

GitHub Pages 的软限制是每月 100 GB 流量和每小时 10 次构建。构建出的 `site/dist` 目前有
62 个文件（`find site/dist -type f | wc -l`），不需要额外付费。
