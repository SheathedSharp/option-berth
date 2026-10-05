# Product website / 产品官网

静态中英产品介绍；不连接用户 daemon，不提供云端会话、不使用遥测、外部字体或运行时框架。
产品图标来自 `brand/` 的原始生成资产。正文、图片和下载入口在禁用 JavaScript 时仍可用。

## Build and preview

```sh
python3 site/build.py
python3 -m http.server 8765 --bind 127.0.0.1 --directory site/dist
```

构建只复制白名单资产，核对五张 PNG 的尺寸与 SHA256。中文版在 `/`，英文版在 `/en/`；
语言切换是普通链接，不依赖客户端翻译。`dist/` 是可丢弃产物，不进入源码提交。

## Browser verification

```sh
cd site
npm ci --ignore-scripts
npm test                       # 本机已安装的 Google Chrome
CI=1 npx playwright install --with-deps chromium webkit
CI=1 npm test                  # CI 两种浏览器引擎
```

回归覆盖两种语言、360/390/768/1440px、图片加载、无横向溢出、键盘画廊、原生 dialog
关闭与焦点归还、无 JavaScript 的基本可用性，以及减弱动态效果的初始值/实时变化。
测试只启动 localhost 静态服务器；生产网站不会请求 npm、外部脚本或跟踪服务。

## Real native screenshots

```sh
mage buildEngine
swift build --package-path client/macos --force-resolved-versions --product SiteCapture
binary_dir=$(swift build --package-path client/macos --show-bin-path)
python3 scripts/capture_site.py --binary bin/oberth --runner "$binary_dir/SiteCapture" --output site/assets
```

截图来自客户端 PR #49/#50 合并后的实际 `BerthClient` 视图，置于原生 NSWindow，使用真实
CLI/daemon、示例 API/worker、Git 仓库和两个自有 zsh PTY，不使用冻结模型、AI 生成图或
ImageRenderer。采集器创建隔离 HOME/BERTH_HOME 和演示项目，结束时核对服务停止并回收
自己的会话；没有 provider 登录、用户工程、日常账本或真实桌面。`captures.json` 保存源
main 提交、采集方法、尺寸和图片哈希，不保存临时路径、命令输出、环境或凭证。
采集不会隐式构建、安装应用或发布网站。更换截图后重新检查实际图片及网页呈现。

## Deploy

`Product website` workflow 在 PR 中只构建和验证；合并到 main 后才将 `site/dist` 发送到
GitHub Pages。仓库 Pages 的发布源为 GitHub Actions。失败不会部署，部署不需要私有令牌。
下载入口指向官方 `releases/latest`，不猜测版本或跳过签名/校验说明。当前 macOS 资产是
ad-hoc 签名，不能描述为 Developer ID 签名或 Apple 公证。
