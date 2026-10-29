# Engine website / 引擎官网

静态中英产品介绍，只讲 `oberth` 引擎：清单、命令、运行事实与发布包边界。页面不连接
daemon，不使用遥测、外部字体、图片服务或运行时框架。图标与字标来自 `brand/` 的原始资产；
正文和下载入口在禁用 JavaScript 时仍可用。

产品界面与给 coding agent 的 skill 是独立产品，分别在
[option-berth-macos](https://github.com/SheathedSharp/option-berth-macos) 与
[option-berth-skill](https://github.com/SheathedSharp/option-berth-skill)。本仓库不再保存
macOS 应用截图或采集脚本；旧页面里的截图仍留在 Git 历史中，供客户端仓库按需取用。

## Build and preview

```sh
python3 site/build.py
python3 -m http.server 8765 --bind 127.0.0.1 --directory site/dist
```

构建只复制白名单资产（`icon.svg`、`mark.svg`、`site.css`、`site.js`），并检查中英两份文案
的键集合一致。中文版在 `/`，英文版在 `/en/`；语言切换是普通链接，不依赖客户端翻译。
`dist/` 是可丢弃产物，不进入源码提交。

## Browser verification

```sh
cd site
npm ci --ignore-scripts
npm test                       # 本机已安装的 Google Chrome
CI=1 npx playwright install --with-deps chromium webkit
CI=1 npm test                  # CI 两种浏览器引擎
```

回归覆盖两种语言、360/390/768/1440px、图片加载、无横向溢出、命令过滤与空状态、Escape
重置、无 JavaScript 的基本可用性，以及减弱动态效果的初始值/实时变化。测试只启动
localhost 静态服务器；生产网站不会请求 npm、外部脚本或跟踪服务。

## Content evidence

页面里的命令输出取自 `examples/hello-worktree` 的真实运行：隔离 `HOME`/`BERTH_HOME`、
自动端口、无端口 worker，结束后由 `down` 回收。截图与日志中的临时路径已替换为演示值；
其余字段保持原样。清单、命令与平台边界以 [docs/cli.md](../docs/cli.md)、
[docs/architecture.md](../docs/architecture.md) 和 [docs/validation.md](../docs/validation.md) 为准。

## Deploy

`Product website` workflow 在 PR 中只构建和验证；合并到 main 后才将 `site/dist` 发送到
GitHub Pages。仓库 Pages 的发布源为 GitHub Actions。失败不会部署，部署不需要私有令牌。
下载入口指向官方 `releases/latest`，只承诺引擎 CLI 压缩包；不猜测版本，也不跳过
SHA256SUMS 校验说明。
