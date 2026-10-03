import AppKit

/// 菜单栏里只留这个应用自己的菜单。
///
/// SwiftUI 建出来的主菜单自带四个和这个看板无关的菜单：Edit（剪切板那一套）、
/// View、Window、Help —— 每个 AppKit 应用都有的默认项，撤掉。
///
/// **撤的是「看得见」，不是「按得动」。** 撤法用 `isHidden` 而不是 `removeItem`：
/// 藏在暗处的菜单项**照样参与键等价物匹配**。这条量过 ——
/// `Edit ▸ Paste ⌘V` 藏起来之后 `mainMenu.performKeyEquivalent` 仍然接住它，
/// 而整个删掉的对照组返回 false（`StandardKeyBinding.dict` 里那 233 条
/// **根本没有 ⌘V / ⌘A / ⌘Z**，带 ⌘ 的只有 ⌘. / ⌘⌫ 这几条）。所以文本框里的粘贴、
/// 全选、撤销**只有菜单这一条来路**：把 Edit 删干净就等于把清单编辑器里的粘贴删掉。
///
/// 例外只有一个：**⌘C 归「代码」了**，而藏着的 `Copy` 会把它挡掉 —— 同一个键上挂着
/// 两项时排在前面的先匹配，而且**前面那项禁用时后面的项也不会被接上**
/// （两项都量过：启用时 `Copy命中=true`，禁用时**两个都 false**，这一下就没了）。
/// 所以 Copy 的键等价物必须摘掉，文本框里的复制改由 `copyInText` 补上。
enum MenuBar {
    /// 这个菜单是我们自己建的 —— `OptionBerthApp` 里那个 `CommandMenu` 用同一个名字，
    /// 所以「留下谁」和「建了谁」不会各说各的。
    static let viewTitle = "视图"

    /// 撤掉默认菜单。SwiftUI 建完主菜单之后跑，跑几次都一样（幂等）。
    @MainActor
    static func prune() {
        guard let menu = NSApplication.shared.mainMenu else { return }
        let top = menu.items
        // 第 0 项永远是 App 菜单（`option-berth → 设置… / 退出`），那是撤不掉的：
        // macOS 的菜单栏第一格就是它。撤掉的是它后面那些默认菜单。
        for (index, item) in top.enumerated() where index > 0 && item.title != viewTitle {
            item.isHidden = true
        }
        removeCopyShortcut(in: top)
    }

    /// 把 `Copy` 的 ⌘C 摘掉（它藏在 Edit 里，会挡住「代码」）。
    ///
    /// 按 **action** 找而不是按标题找：`Edit` 那一栏的标题跟着系统语言走，
    /// 而 `copy:` 这个 selector 不变。右键菜单里的「拷贝」不走这条路，还在。
    @MainActor
    private static func removeCopyShortcut(in top: [NSMenuItem]) {
        for item in top {
            for sub in item.submenu?.items ?? [] where sub.action == #selector(NSText.copy(_:)) {
                sub.keyEquivalent = ""
            }
        }
    }

    /// ⌘C 的兜底：**正在编辑文本的时候，它还是复制。**
    ///
    /// 判据是首响应者是不是文本视图 —— 设置面板那些格子（`NSTextField` 的字段编辑器）
    /// 和清单那一面（`TextEditor` 的 `NSTextView`）都算。不在这两种情形时把事件原样放走，
    /// 菜单里那一项（「代码」）照常接住它。
    ///
    /// 本地监听跑在派发之前，所以这里接住的那一下不会再去撞菜单。
    @MainActor
    static func installCopyInText() {
        NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
            let flags = event.modifierFlags.intersection(.deviceIndependentFlagsMask)
            guard flags == .command,
                  event.charactersIgnoringModifiers == "c",
                  event.window?.firstResponder is NSTextView else { return event }
            return NSApp.sendAction(#selector(NSText.copy(_:)), to: nil, from: nil) ? nil : event
        }
    }
}

/// 等主菜单建好，然后剪一次。
///
/// 为什么要有这一层：菜单是 SwiftUI 按 `.commands` 建的，建完长什么样只有运行起来才知道 ——
/// 这份 delegate 唯一的事就是把「建好的那一刻」接住。
final class MenuBarDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        MainActor.assumeIsolated {
            MenuBar.prune()
            MenuBar.installCopyInText()
        }
        // SwiftUI 有可能在这之后才把主菜单换上它自己那份（谁先谁后不是能假设的事），
        // 所以再补一次。`prune` 幂等，重复剪不会剪坏什么。
        DispatchQueue.main.async {
            MainActor.assumeIsolated { MenuBar.prune() }
        }
    }
}
