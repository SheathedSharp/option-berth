import AppKit
import SwiftUI
import BerthTerminal

/// Headless rendering for real data and a small deterministic state set.
/// The frozen states follow the product surface: projects, services, runs,
/// logs, Git context and runtime facts. Machine wide radar and agent
/// settings are not rendered here.
enum Snapshot {
    @MainActor
    static func run(path: String, scope: Scope = .services(""),
                    width: CGFloat = 900, height: CGFloat = 560) -> Int32 {
        let store = BoardStore()
        let services = ServicesStore()
        store.start(interval: 600)
        services.start(interval: 600)
        let deadline = Date().addingTimeInterval(6)
        while (store.updatedAt == nil || services.updatedAt == nil)
            && store.problem == nil && services.problem == nil && Date() < deadline {
            RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        }
        return write(BoardView(store: store, services: services, scrolls: false,
                               initialScope: scope), to: path, width: width, height: height)
    }

    @MainActor
    static func renderStates(directory: String, width: CGFloat = 900, height: CGFloat = 560) -> Int32 {
        let url = URL(fileURLWithPath: directory)
        do {
            try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        } catch {
            FileHandle.standardError.write(Data("建不了目录 \(directory)：\(error.localizedDescription)\n".utf8))
            return 1
        }

        for target in TourTarget.allCases where target != .facts {
            let navigation = ViewState()
            navigation.showingGuide = true; navigation.guideTarget = target
            let board = BoardView(store: BoardStore(fixture: []), services: ServicesStore(fixture: []),
                                  scrolls: false, views: navigation)
            let code = write(board, to: url.appendingPathComponent("12-guide-\(target.rawValue + 1).png").path,
                             width: width, height: height)
            if code != 0 { return code }
        }

        var historyParser = CommandBlockParser(nonce: "fixture")
        historyParser.consume(Array("\u{1b}]633;E;git status --short;fixture\u{7}\u{1b}]133;C;fixture\u{7}\u{1b}]133;D;0;fixture\u{7}".utf8)[...], now: Date(timeIntervalSince1970: 10))
        historyParser.consume(Array("\u{1b}]633;E;go test ./...;fixture\u{7}\u{1b}]133;C;fixture\u{7}\u{1b}]133;D;1;fixture\u{7}".utf8)[...], now: Date(timeIntervalSince1970: 20))
        let historyID = UUID(uuidString: "00000000-0000-0000-0000-000000000001")!
        let history = historyParser.blocks.map { CommandHistoryEntry(sessionID: historyID, sessionTitle: "Shell · feature/api", block: $0) }
        let historyCode = write(WorktreeHistorySheet(root: "/workspace/demo", sessions: TerminalSessions(), frozen: true, frozenEntries: history),
                                to: url.appendingPathComponent("13-command-history.png").path, width: 690, height: 520)
        if historyCode != 0 { return historyCode }

        let ports = frozenPorts()
        let groups = frozenGroups()
        let runs = frozenRuns()
        let logs = frozenLog()
        let git = frozenGit()
        guard let demo = groups.first(where: { $0.name == "berth-demo" }) else { return 1 }
        let focus = ServiceRef(group: demo.name, service: "api")

        let states: [(String, BoardStore, ServicesStore, GitStore, Scope)] = [
            ("01-empty", BoardStore(fixture: []), ServicesStore(fixture: []), GitStore(), .services("")),
            ("02-services-live", BoardStore(fixture: ports),
             ServicesStore(fixture: groups, runs: runs, focused: focus, logs: logs,
                           logPath: ServicesStore.expectedLogPath(group: demo.name, service: "api"),
                           notice: [ServiceRef(group: demo.name, service: "web"): "pid 31162 · 端口 5173"]),
             GitStore(),
             .services(demo.name)),
            ("03-services-idle", BoardStore(fixture: ports),
             ServicesStore(fixture: groups), GitStore(), .services(demo.name)),
            ("04-runtime-facts", BoardStore(fixture: ports),
             ServicesStore(fixture: groups, runs: runs), GitStore(), .services(demo.name)),
            ("05-connection-error", BoardStore(fixture: ports, problem: "连接被拒绝 —— 后台没在跑"),
             ServicesStore(fixture: groups, runs: runs), GitStore(), .services(demo.name)),
            ("09-terminal", BoardStore(fixture: ports),
             ServicesStore(fixture: groups, runs: runs), GitStore(), .terminal(demo.name)),
            ("08-code", BoardStore(fixture: ports),
             ServicesStore(fixture: groups, runs: runs), git, .code(demo.name)),
        ]

        for (name, store, services, git, scope) in states {
            let code = write(BoardView(store: store, services: services, git: git, scrolls: false,
                                       initialScope: scope),
                             to: url.appendingPathComponent("\(name).png").path,
                             width: width, height: height)
            if code != 0 { return code }
        }

        let agentCode = write(BoardView(store: BoardStore(fixture: ports),
                                             services: ServicesStore(fixture: groups, runs: runs),
                                             git: git, scrolls: false, initialScope: .terminal(demo.name),
                                             initialConsoleAgent: true),
                              to: url.appendingPathComponent("10-agent-console.png").path,
                              width: width, height: height)
        if agentCode != 0 { return agentCode }

        let left = TerminalSession(worktree: "/workspace/demo", title: "Shell · feature/api")
        let right = TerminalSession(worktree: "/workspace/demo", title: "Codex · feature/api", kind: "agent:codex:native")
        let nested = TerminalSession(worktree: "/workspace/demo", title: "Shell · tests")
        let layout = ConsoleWorkspace()
        layout.single(left.id, agent: false)
        do {
            try layout.split(right.id, beside: left.id, agent: false, axis: .horizontal)
            try layout.split(nested.id, beside: right.id, agent: false, axis: .vertical)
        } catch { return 1 }
        let splitCode = write(PaneWorkspaceView(primary: left, workspace: layout, agent: false, frozen: true,
                                                frozenSessions: [left, right, nested]),
                              to: url.appendingPathComponent("11-console-split.png").path,
                              width: width, height: height)
        if splitCode != 0 { return splitCode }

        let proposal = GroupInitResult(path: "/Users/you/code/new-worktree/oberth.yaml",
                                       yaml: "name: new-worktree\nservices: []\n",
                                       proposal: emptyGroup(name: "new-worktree"))
        let sheetCode = write(
            AddProjectSheet(result: proposal, scrolls: false, onWrite: { _ in }, onCancel: {}),
            to: url.appendingPathComponent("06-manifest-review.png").path,
            width: width, height: 440)
        if sheetCode != 0 { return sheetCode }

        let settingsCode = write(SettingsSheet(scrolls: false, previewJev: true),
                                 to: url.appendingPathComponent("07-settings.png").path,
                                 width: 720, height: 560)
        if settingsCode != 0 { return settingsCode }
        let jevCode = write(SettingsSheet(scrolls: false, initialSection: .jev, previewJev: true,
                                          renderHeight: 760),
                            to: url.appendingPathComponent("07-settings-jev.png").path,
                            width: 720, height: 760)
        if jevCode != 0 { return jevCode }
        return write(SettingsSheet(scrolls: false, initialSection: .typography, previewJev: true),
                     to: url.appendingPathComponent("07-settings-typography.png").path,
                     width: 720, height: 560)
    }

    @MainActor
    private static func write(_ view: some View, to path: String,
                              width: CGFloat, height: CGFloat? = nil) -> Int32 {
        let sized = height.map { view.frame(width: width, height: $0) } ?? view.frame(width: width)
        let renderer = ImageRenderer(content: sized.background(Ink.canvas).environment(\.colorScheme, UISettings.shared.colorScheme))
        renderer.scale = 2
        guard let image = renderer.cgImage else {
            FileHandle.standardError.write(Data("离屏渲染没产出图像：\(path)\n".utf8))
            return 1
        }
        let rep = NSBitmapImageRep(cgImage: image)
        guard let png = rep.representation(using: .png, properties: [:]) else {
            FileHandle.standardError.write(Data("PNG 编码失败：\(path)\n".utf8))
            return 1
        }
        do {
            try png.write(to: URL(fileURLWithPath: path))
        } catch {
            FileHandle.standardError.write(Data("写不进 \(path)：\(error.localizedDescription)\n".utf8))
            return 1
        }
        print("已写出 \(path)（\(image.width)×\(image.height) @2x）")
        return 0
    }

    private static func emptyGroup(name: String) -> BerthGroup {
        let json = """
        {"name":"\(name)","repo":"\(name)","worktree":"","branch":"main","source":"file",
         "root_dir":"/Users/you/code/\(name)","config_path":"/Users/you/code/\(name)/oberth.yaml",
         "status":"stopped","members":[],"services":[]}
        """
        return try! JSONDecoder().decode(BerthGroup.self, from: Data(json.utf8))
    }

    @MainActor
    private static func frozenPorts() -> [Port] {
        let json = """
        [
          {"port":18080,"bind_address":"127.0.0.1","pid":31131,"process":"python3",
           "display_name":"python3","url":"http://127.0.0.1:18080","cwd":"/Users/you/code/berth-demo",
           "cwd_in_trash":false,"cwd_gone":false,"project_root":"/Users/you/code/berth-demo","group":"berth-demo"},
          {"port":5173,"bind_address":"0.0.0.0","pid":31162,"process":"node",
           "display_name":"node","url":"http://localhost:5173","cwd":"/Users/you/code/berth-demo",
           "cwd_in_trash":false,"cwd_gone":false,"project_root":"/Users/you/code/berth-demo","group":"berth-demo"},
          {"port":4100,"bind_address":"127.0.0.1","pid":40100,"process":"python3",
           "display_name":"python3","url":"http://127.0.0.1:4100","cwd":"/Users/you/code/berth-demo",
           "cwd_in_trash":false,"cwd_gone":false,"project_root":"/Users/you/code/berth-demo","group":"berth-demo"},
          {"port":8787,"bind_address":"127.0.0.1","pid":42001,"process":"go",
           "display_name":"go","url":"http://127.0.0.1:8787","cwd":"/Users/you/code/other",
           "cwd_in_trash":false,"cwd_gone":false,"project_root":"/Users/you/code/other","group":"other"}
        ]
        """
        return (try? JSONDecoder().decode([Port].self, from: Data(json.utf8))) ?? []
    }

    @MainActor
    private static func frozenGroups() -> [BerthGroup] {
        let json = """
        [
          {"name":"berth-demo","repo":"berth-demo","worktree":"feature/checkout","branch":"feature/checkout","source":"file",
           "root_dir":"/Users/you/code/berth-demo","config_path":"/Users/you/code/berth-demo/oberth.yaml",
           "status":"running","members":[31131,31162],"services":[
             {"name":"api","cmd":"python3 -m http.server 18080","cwd":"/Users/you/code/berth-demo",
              "port":18080,"health":"/health","health_status":{"status":"ok","code":200,"latency_ms":4,"observed_at":"2026-09-24T09:41:19.004Z"},
              "running":true,"port_actual":18080,"log_path":"/Users/you/.option-berth/logs/berth-demo/api.log"},
             {"name":"web","cmd":"pnpm run dev","cwd":"/Users/you/code/berth-demo",
              "port":5173,"health":"/ready","health_status":{"status":"fail","code":503,"reason":"timeout","observed_at":"2026-09-24T09:41:20.004Z"},
              "runtime_cmd":"pnpm run dev:old","runtime_cwd":"/Users/you/code/berth-demo","manifest_runtime_mismatch":true,
              "running":true,"port_actual":5173,"log_path":"/Users/you/.option-berth/logs/berth-demo/web.log"},
             {"name":"worker","cmd":"python3 worker.py","cwd":"/Users/you/code/berth-demo",
              "last_exit":{"code":1,"reason":"ready_timeout","at":"2026-09-24T09:40:00Z","run_id":"worker-timeout"},
              "running":false,"log_path":"/Users/you/.option-berth/logs/berth-demo/worker.log"}
           ]}
        ]
        """
        return (try? JSONDecoder().decode([BerthGroup].self, from: Data(json.utf8))) ?? []
    }

    @MainActor
    private static func frozenRuns() -> RunsSnapshot {
        let json = """
        {"runs":[
          {"id":"18bfc856","pid":31131,"group":"berth-demo","name":"api","cmd":"python3 -m http.server 18080","cwd":"/Users/you/code/berth-demo","started_at":"2026-09-22T00:52:11+08:00","ports":[18080],"port_hint":18080,"status":"running","log_path":"/Users/you/.option-berth/logs/berth-demo/api.log"},
          {"id":"6d1e4f02","pid":31162,"group":"berth-demo","name":"web","cmd":"pnpm run dev","cwd":"/Users/you/code/berth-demo","started_at":"2026-09-22T00:52:14+08:00","ports":[5173],"port_hint":5173,"status":"running","log_path":"/Users/you/.option-berth/logs/berth-demo/web.log"}
        ],"exited":[]}
        """
        return (try? JSONDecoder().decode(RunsSnapshot.self, from: Data(json.utf8))) ?? .empty
    }

    @MainActor
    private static func frozenLog() -> [String] {
        [
            "2026-09-24T09:41:07.312+08:00 INFO     Started server process [31131]",
            "2026-09-24T09:41:07.902+08:00 INFO     Application startup complete.",
            "2026-09-24T09:41:19.004+08:00 INFO     127.0.0.1:52344 - \\\"GET /health HTTP/1.1\\\" 200 OK",
            "2026-09-24T09:41:31.007+08:00 WARNING  127.0.0.1:52360 - \\\"GET /favicon.ico HTTP/1.1\\\" 404 Not Found",
            "2026-09-24T09:41:44.560+08:00 ERROR    embedding: batch 3/7 failed: connection reset by peer",
            "Traceback (most recent call last):",
            "  File \\\"worker.py\\\", line 88, in flush",
            "ConnectionResetError: [Errno 54] Connection reset by peer",
        ]
    }

    @MainActor
    private static func frozenGit() -> GitStore {
        let overviewJSON = """
        {"root":"/Users/you/code/berth-demo","branch":"feature/checkout",
         "head":"0123456789abcdef","upstream":"origin/feature/checkout",
         "ahead":2,"behind":1,"staged":1,"unstaged":2,"untracked":1,"conflicts":0,
         "last_commit":{"hash":"0123456789abcdef","subject":"wire service lifecycle","when":"2026-09-24T08:20:00+08:00"},
         "worktrees":[{"path":"/Users/you/code/berth-demo","branch":"feature/checkout","head":"0123456789abcdef","current":true},
                       {"path":"/Users/you/code/berth-demo-main","branch":"main","head":"fedcba9876543210","current":false}]}
        """
        let treeJSON = """
        {"root":"/Users/you/code/berth-demo","branch":"feature/checkout",
         "head":"0123456789abcdef","upstream":"origin/feature/checkout",
         "ahead":2,"behind":1,"staged":1,"unstaged":2,"untracked":1,"conflicts":0,
         "files":[
           {"path":"Sources/API.swift","status":"M ","additions":12,"deletions":3},
           {"path":"README.md","status":" M","additions":4,"deletions":1},
           {"path":"notes.md","status":"??","additions":18,"deletions":0}
         ]}
        """
        let patchJSON = """
        {"root":"/Users/you/code/berth-demo","files":[{"path":"Sources/API.swift","lines":[
          {"kind":"meta","text":"diff --git a/Sources/API.swift b/Sources/API.swift"},
          {"kind":"hunk","text":"@@ -8,3 +8,5 @@"},
          {"kind":"context","text":" let server = Server()"},
          {"kind":"del","text":"-server.start()"},
          {"kind":"add","text":"+server.start(port: port)"}
        ]}]}
        """
        let decoder = JSONDecoder()
        let overview = try! decoder.decode(GitOverview.self, from: Data(overviewJSON.utf8))
        let tree = try! decoder.decode(GitTree.self, from: Data(treeJSON.utf8))
        let patch = try! decoder.decode(GitPatch.self, from: Data(patchJSON.utf8))
        return GitStore(overview: overview, tree: tree, patch: patch, selectedPath: "Sources/API.swift")
    }
}
