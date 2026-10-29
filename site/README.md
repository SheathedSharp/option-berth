# Engine website / 引擎官网

静态中英产品介绍，只讲 `oberth` 引擎：为什么不再靠猜、清单与运行事实、常用命令、
安装包与边界。页面不连接 daemon，不使用遥测、外部字体、图片服务或运行时框架。
图标、浅色字标与深色字标来自 `brand/` 的原始资产；正文、语言切换和下载入口在禁用
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
`site.js`），并检查中英两份文案的键集合一致。中文版在 `/`，英文版在 `/en/`；语言切换是
普通链接，不依赖客户端翻译。`dist/` 是可丢弃产物，不进入源码提交。

页面用深色品牌变体作为底色，陶土色只出现在少数可操作元素和品牌标记上。字体使用系统字族：
站点 CSP 只允许 `self`，README 也约定不加载外部字体，因此没有引用 `design-taste-frontend`
方向单里的 Albert Sans；`mark-dark.svg` 是品牌深色字标，保证深色底上两条轨仍可读。

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
测试只启动 localhost 静态服务器；生产网站不会请求 npm、外部脚本或跟踪服务。

## Content evidence

页面里的清单、状态与日志取自 `examples/hello-worktree` 的真实运行：隔离 `HOME`/`BERTH_HOME`、
自动端口、无端口 worker，结束后由 `down` 回收。截图与日志中的临时路径已替换为演示值；
端口 `21730`、健康 `200` 与无端口 worker 是这份演示的输出。四事实卡片把同一次运行里的
进程、端口、健康、退出拆开讲，并用无端口 worker 说明“没有监听不等于没在运行”。清单、命令与平台边界以
[docs/cli.md](../docs/cli.md)、[docs/architecture.md](../docs/architecture.md) 和
[docs/validation.md](../docs/validation.md) 为准。

## Deploy

`Product website` workflow 在 PR 中只构建和验证；合并到 main 后才将 `site/dist` 发送到
GitHub Pages。仓库 Pages 的发布源为 GitHub Actions。失败不会部署，部署不需要私有令牌。
下载入口指向官方 `releases/latest`，只承诺引擎 CLI 压缩包；不猜测版本，也不跳过
SHA256SUMS 校验说明。
