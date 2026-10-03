# 各自成泊

**Option：保留选择。Berth：运行有归属。** 对称双轨围出泊位，独立胶囊表示有明确归属的运行。
品牌图形不是进程拓扑；陶土色不是服务健康状态。深浅色和单色版本保持同一语义。

唯一几何源是 `client/macos/Sources/BerthGeometry.swift`，包含标准与小尺寸轮廓、色板和图标布局。
`BerthMark`、原生图标和13个SVG导出共用它；固定轮廓字标在 `wordmark.svg`。
标准轨宽为3.2/24，20及以下尺寸使用3.6/24的光学校正；保持封闭轮廓与相切曲线。

```sh
python3 brand/build-option-berth.py --check
python3 -m unittest discover -s brand -p 'test_*.py'
mage client
mage states
```

修改几何后运行导出脚本并提交SVG。PNG/ICNS由原生 `--write-icon` 与 `iconutil` 生成到构建目录，
不保留旧位图作为第二套品牌源。`preview-option-berth.html` 可离线查看深浅、单色及小尺寸版本。
原生渲染需检查空态、字体缩放、字标基线和主题对比；离屏结果不等于已检查Dock或全部交互。
不恢复已移除的第三方标志目录；许可见根目录 THIRD_PARTY_NOTICES.md。
