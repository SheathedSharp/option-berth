import SwiftUI

/// 入口。默认起窗口；几个无头开关用来把「窗口是空的」拆成几段看，
/// 以及把图标从标志里生成出来：
///   --probe                只走数据那一段，命令行里打印结果
///   --snapshot <path>      把界面离屏画成 PNG，不开窗口
///   --render-states <dir>  用冻结数据把全部界面状态各画一张（可带 `--size 宽x高`）
///   --write-icon <dir>     把标志画成各档尺寸的 .iconset，交给 iconutil 打包
///   --remove-project <名>  撤销一个项目的纳管（删清单 + 删登记），跑完打印动了什么
///   --parse-log <文件>     把一份日志按客户端认的规则解剖一遍，打印每一行
///
/// 前三个画的是**视图**（`ImageRenderer` 里没有窗口），所以量不到标题栏、
/// 安全区这些「窗口才有」的东西。要量窗口本身用 `--dump-window`（见 WindowDump）。
@main
enum Entry {
    @MainActor
    static func main() {
        let arguments = CommandLine.arguments

        if arguments.contains("--probe") {
            exit(Probe.run())
        }

        if let index = arguments.firstIndex(of: "--snapshot"), index + 1 < arguments.count {
            exit(Snapshot.run(path: arguments[index + 1], scope: scope(from: arguments)))
        }

        if let index = arguments.firstIndex(of: "--render-states"), index + 1 < arguments.count {
            // 尺寸和真窗口那条同一个开关：有些东西（图例这种会折行的）只在窄的时候
            // 才看得出对不对，而「一直用一个宽度画」等于没量过。
            let size = windowSize(from: arguments)
            exit(Snapshot.renderStates(directory: arguments[index + 1],
                                       width: size?.width ?? 900, height: size?.height ?? 560))
        }

        if let index = arguments.firstIndex(of: "--write-icon"), index + 1 < arguments.count {
            exit(AppIcon.write(to: arguments[index + 1]))
        }

        // 撤销纳管。窗口里那个「移除项目」按钮点不到（离屏渲染没有窗口），
        // 这条路径用它验。
        if let index = arguments.firstIndex(of: "--remove-project"), index + 1 < arguments.count {
            exit(ProjectRegistry.runRemoval(name: arguments[index + 1]))
        }

        // 把一份日志文件的每一行解剖出来。和上面那条一样是给验证台的入口：
        // 日志排版认不认得出来、有没有把哪一行吞掉，只有拿真文件跑一遍才算数。
        if let index = arguments.firstIndex(of: "--parse-log"), index + 1 < arguments.count {
            exit(LogParse.report(path: arguments[index + 1]))
        }

        // 这一条要起真窗口，所以排在最后：前面几条都是离屏的，谁都不会闪一下。
        // 它只是排一个「等布局落定再量」的活儿，窗口还是照常起。
        if let index = arguments.firstIndex(of: "--dump-window"), index + 1 < arguments.count {
            WindowDump.run(path: arguments[index + 1],
                           size: windowSize(from: arguments),
                           press: menuItem(from: arguments))
        }

        OptionBerthApp.main()
    }

    /// 可选的 `--press "<菜单项>"`：量之前先按一下那一项。
    ///
    /// 快捷键按不出来证据，但菜单项和快捷键走的是同一个 action —— 按菜单项
    /// 之后画面变成什么样，就是那个快捷键的效果。
    static func menuItem(from arguments: [String]) -> String? {
        guard let index = arguments.firstIndex(of: "--press"), index + 1 < arguments.count else {
            return nil
        }
        return arguments[index + 1]
    }

    /// 可选的 `--size 宽x高`（点为单位）：`--dump-window` 拿它当真窗口的尺寸，
    /// `--render-states` 拿它当画布尺寸。
    ///
    /// 布局里「依赖尺寸」的那一部分（哪一列分到多少、长行会不会被裁、图例在哪一行折断）
    /// 只有换着尺寸看才看得出来 —— 一直用一个尺寸画，等于没量过比例。
    static func windowSize(from arguments: [String]) -> CGSize? {
        guard let index = arguments.firstIndex(of: "--size"), index + 1 < arguments.count else {
            return nil
        }
        let parts = arguments[index + 1].lowercased().split(separator: "x")
        guard parts.count == 2, let width = Double(parts[0]), let height = Double(parts[1]),
              width > 0, height > 0 else { return nil }
        return CGSize(width: width, height: height)
    }

    /// 可选的 `--scope`：`services:项目` 或 `code:项目`；旧的 `project:项目`
    /// 仍能解析，但会落到服务页。
    static func scope(from arguments: [String]) -> Scope {
        guard let index = arguments.firstIndex(of: "--scope"), index + 1 < arguments.count else {
            return .services("")
        }
        return Scope(argument: arguments[index + 1])
    }

}

struct OptionBerthApp: App {
    @StateObject private var store = BoardStore()
    @StateObject private var services = ServicesStore()
    @StateObject private var git = GitStore()
    @StateObject private var settings = UISettings.shared
    @StateObject private var views = ViewState(scope: Entry.scope(from: CommandLine.arguments))
    @NSApplicationDelegateAdaptor(MenuBarDelegate.self) private var menuBar

    var body: some Scene {
        WindowGroup("option-berth") {
            BoardView(store: store, services: services, git: git, views: views, settings: settings)
                .environmentObject(settings)
                .frame(minWidth: 720, minHeight: 420)
                .onAppear {
                    store.start()
                    services.start()
                }
                .onDisappear {
                    store.stop()
                    services.stop()
                }
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 900, height: 560)
        .commands {
            CommandGroup(replacing: .newItem) {}
            CommandGroup(replacing: .printItem) {}
            CommandGroup(replacing: .sidebar) {}
            CommandGroup(replacing: .appInfo) {}
            CommandGroup(replacing: .systemServices) {}
            CommandGroup(replacing: .appVisibility) {}
            CommandGroup(replacing: .appSettings) {
                Button("设置…") { views.showingSettings = true }
                    .keyboardShortcut(",", modifiers: .command)
            }
            CommandMenu(MenuBar.viewTitle) {
                Button(views.railVisible ? "隐藏左栏" : "显示左栏") {
                    views.railVisible.toggle()
                }
                .keyboardShortcut("l", modifiers: .command)
                Divider()
                Button("服务") { views.show(.services, projects: projectNames) }
                    .keyboardShortcut("s", modifiers: .command)
                Button("代码") { views.show(.code, projects: projectNames) }
                    .keyboardShortcut("g", modifiers: .command)
                Divider()
                Button("查找日志") { services.beginLogFind() }
                    .keyboardShortcut("f", modifiers: .command)
                    .disabled(services.focused == nil)
                Button("刷新") {
                    store.refresh()
                    services.refresh()
                }
                .keyboardShortcut("r", modifiers: .command)
            }
        }
    }

    private var projectNames: [String] { services.projects.map(\.name).sorted() }
}
