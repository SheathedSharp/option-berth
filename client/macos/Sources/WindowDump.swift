import AppKit
import SwiftUI

/// 第四个无头开关：**量窗口本体**，不是量视图。
///
/// 另外三个开关（`--probe` / `--snapshot` / `--render-states`）画的是**视图**——
/// `ImageRenderer` 里没有窗口，也就没有标题栏、没有安全区、没有红绿灯。有一类错
/// 它们**结构性地看不见**：布局里依赖窗口的那一部分。
///
/// 真踩到的：窗口是 `.hiddenTitleBar`（标题栏透明、内容铺满整窗），SwiftUI 会按
/// 标题栏高度（这台机器上 32pt）把内容让开一次，而代码里又横了一条 36pt 的让位条，
/// 于是窗口顶上空出 68pt 的白带子 —— 左栏像被削掉了头。离屏那三张图里只看得到
/// 36pt，怎么画都「对」。这就是「图和窗口对不上」这一类错，得换个工具才看得见。
///
/// 它做四件事：
/// 1. 打 `NSWindow` 的真实几何（frame / styleMask / contentLayoutRect / safeAreaInsets）
/// 2. 量红绿灯在窗口里实际占哪一段（别猜 18..30 那种数）
/// 3. 对顶部那一条做击测试，确认它**还能拖窗口**（左栏底色铺到顶之后最容易碰坏这个）
/// 4. 用 `cacheDisplay` 把**内容视图**渲染成 PNG —— 不走录屏权限，
///    沙箱里 `screencapture` 是直接被拒的（`could not create image from display`）
///
/// 用法：`OptionBerth --dump-window .cache/window.png`
/// （会闪一下窗口，等两秒自己退出。）
///
/// **量谁**：弹窗开着就量弹窗 —— sheet 是一个独立的 `NSWindow`，「设置面板是不是真的
/// 跟着内容变高变矮」只有量它才看得见。
enum WindowDump {
    /// 起了窗口、等布局落定之后再量。太早量到的是布局前的空壳。
    private static let settle: TimeInterval = 2.0

    /// 排一个「等布局落定再量」的活儿，随即返回（返回的 0 是「排上了」那套形状里的
    /// 一个：前面几条开关都是 `exit(run())`，这一条不是 —— 它要窗口活着）。
    @discardableResult
    @MainActor
    static func run(path: String, size: CGSize? = nil, press menuItem: String? = nil) -> Int32 {
        DispatchQueue.main.asyncAfter(deadline: .now() + settle) {
            // 窗口是 `OptionBerthApp.main()` 建的，而这条命令在它之前就跑到了 ——
            // 所以摆尺寸这一步必须在窗口出来之后做，否则 `windows.first` 是 nil，
            // 三张图会是一模一样大（第一版就是这么量了个假的）。
            if let size, let window = mainWindow() {
                window.setContentSize(size)
            }
            if let menuItem { press(menuItem) }
            // 摆过尺寸、按过菜单之后，再给布局一拍，然后才量。
            let wait: TimeInterval = (size == nil && menuItem == nil) ? 0 : 0.6
            DispatchQueue.main.asyncAfter(deadline: .now() + wait) {
                report(path: path)
            }
        }
        return 0
    }

    /// 按一下主菜单里叫这个名字的那一项。
    ///
    /// `perform` 走的就是点菜单项的那条路 —— 快捷键挂的是同一个 action，
    /// 所以这条量法能替快捷键作证（键盘事件本身在这套验证台里按不出来）。
    @MainActor
    private static func press(_ title: String) {
        for top in NSApplication.shared.mainMenu?.items ?? [] {
            for item in top.submenu?.items ?? [] where item.title == title {
                if let target = item.target, let action = item.action {
                    // `perform` 回的是 action 的返回值（`Unmanaged<AnyObject>?`），菜单
                    // action 要的东西全在副作用里，这个值没人接。
                    _ = target.perform(action, with: item)
                    return
                }
            }
        }
        FileHandle.standardError.write(Data("菜单里没有「\(title)」这一项\n".utf8))
    }

    /// 一个菜单项的快捷键，写成 `⌘P` 这样。没有就是空串。
    private static func shortcut(_ item: NSMenuItem) -> String {
        guard !item.keyEquivalent.isEmpty else { return "" }
        var keys = ""
        let mask = item.keyEquivalentModifierMask
        if mask.contains(.control) { keys += "⌃" }
        if mask.contains(.option) { keys += "⌥" }
        if mask.contains(.shift) { keys += "⇧" }
        if mask.contains(.command) { keys += "⌘" }
        return "   \(keys)\(item.keyEquivalent.uppercased())"
    }

    /// 量哪一个窗口：**弹窗优先**。
    ///
    /// 弹窗（sheet）是一个独立的 `NSWindow`，挂在主窗口上。只量 `windows.first` 的话，
    /// 设置面板这类东西永远量不到 —— 而「弹窗是不是真的跟着内容变高变矮」恰恰只有
    /// 量它才能作证（离屏那几张图里没有窗口，量不出这件事）。
    @MainActor
    private static func measured() -> NSWindow? {
        let windows = NSApplication.shared.windows
        return windows.first(where: { $0.isSheet }) ?? windows.first
    }

    /// 摆尺寸摆的是主窗口：弹窗的大小归它自己（跟着内容走），不许外面硬摆。
    @MainActor
    private static func mainWindow() -> NSWindow? {
        let windows = NSApplication.shared.windows
        return windows.first(where: { !$0.isSheet }) ?? windows.first
    }

    @MainActor
    private static func report(path: String) {
        guard let window = measured(),
              let content = window.contentView else {
            FileHandle.standardError.write(Data("没有窗口可量\n".utf8))
            exit(2)
        }

        print("---- 量的是\(window.isSheet ? "弹窗" : "主窗口") ----")
        print("---- 窗口几何 ----")
        print("frame             = \(window.frame)")
        print("styleMask         = \(window.styleMask.rawValue) fullSizeContent=\(window.styleMask.contains(.fullSizeContentView))")
        print("titlebarTransparent = \(window.titlebarAppearsTransparent) titleVisibility=\(window.titleVisibility.rawValue)")
        print("contentView.frame = \(content.frame)  bounds=\(content.bounds)")
        print("contentLayoutRect = \(window.contentLayoutRect)")
        print("safeAreaInsets    = \(content.safeAreaInsets)")

        // 红绿灯的实位。窗口左上角原点、顶距往下算 —— 和截图的坐标一致，好对照。
        for kind in [NSWindow.ButtonType.closeButton, .miniaturizeButton, .zoomButton] {
            guard let button = window.standardWindowButton(kind), let host = button.superview else { continue }
            let rect = host.convert(button.frame, to: nil)
            let top = window.frame.height - rect.maxY
            print("红绿灯 \(kind.rawValue): x=\(rect.minX) 顶距=\(top) 高=\(rect.height)")
        }

        // 顶部那一条还能不能拖窗口。左栏底色铺到顶之后，命中会落到 SwiftUI 的
        // hosting view 上，那时只能靠 `mouseDownCanMoveWindow` 判断。
        if let frameView = content.superview {
            for (label, x) in [("左栏上方", 100.0), ("内容区上方", 500.0), ("左栏（标题栏以下）", 100.0)] {
                let fromTop: CGFloat = label.contains("以下") ? 60 : 16
                let point = NSPoint(x: x, y: window.frame.height - fromTop)
                let hit = frameView.hitTest(point)
                print("命中 \(label) (x=\(Int(x)) 顶距=\(Int(fromTop))) → \(hit.map { String(describing: type(of: $0)) } ?? "nil") 可拖窗口=\(hit?.mouseDownCanMoveWindow ?? false)")
            }
        }

        // 菜单：⌘L / ⌘S 这些到底挂上没有，只有把菜单打出来才算数 —— 快捷键在
        // 命令行里按不出来，但键等价物读得出来（配合 `--press` 连效果一起验）。
        // `[隐藏]` 说的是**栏里有没有它**：撤掉的默认菜单走的是 `isHidden`，
        // 不是 `removeItem`（见 `MenuBar`）—— 只打标题的话，那四个会看起来还在。
        //
        // 有一处读数是**旧的**：跟着状态换字的项（「隐藏左栏 / 显示左栏」）要等菜单
        // **真的弹开**才重画。量过：状态切了之后读还是旧字，`update()` 也不管用，
        // 弹出来读就是新字 —— 所以按过那一项之后这里打的是旧标题，用户看到的是新的。
        print("---- 菜单 ----")
        for top in NSApplication.shared.mainMenu?.items ?? [] {
            print("\(top.title)\(shortcut(top))\(top.isHidden ? "  [隐藏]" : "")")
            for item in top.submenu?.items ?? [] {
                print("  \(item.title)\(shortcut(item))")
            }
        }

        guard let rep = content.bitmapImageRepForCachingDisplay(in: content.bounds) else {
            FileHandle.standardError.write(Data("cacheDisplay 拿不到位图\n".utf8))
            exit(1)
        }
        content.cacheDisplay(in: content.bounds, to: rep)
        guard let png = rep.representation(using: .png, properties: [:]) else {
            FileHandle.standardError.write(Data("PNG 编码失败\n".utf8))
            exit(1)
        }
        do {
            try png.write(to: URL(fileURLWithPath: path))
            print("已写出 \(path)（\(rep.pixelsWide)x\(rep.pixelsHigh)）")
        } catch {
            FileHandle.standardError.write(Data("写不进 \(path)：\(error.localizedDescription)\n".utf8))
            exit(1)
        }
        exit(0)
    }
}
