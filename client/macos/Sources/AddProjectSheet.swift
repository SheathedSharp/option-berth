import SwiftUI

/// A human review step for a manifest proposal or an existing manifest.
/// The bytes in the editor are the bytes sent back to the daemon.
struct AddProjectSheet: View {
    enum Mode { case create, edit }

    let result: GroupInitResult
    var problem: String?
    var mode: Mode = .create
    var scrolls: Bool = true
    let onWrite: (String) -> Void
    let onCancel: () -> Void

    @Environment(\.accessibilityReduceMotion) private var reduce
    @State private var text: String
    @State private var drafting = false
    @State private var draftProblem: String?
    @State private var draftStage = "准备中"
    @State private var draftMessage = "正在读取项目事实"
    @State private var draftStartedAt = Date()

    init(
        result: GroupInitResult,
        problem: String? = nil,
        mode: Mode = .create,
        scrolls: Bool = true,
        onWrite: @escaping (String) -> Void,
        onCancel: @escaping () -> Void
    ) {
        self.result = result
        self.problem = problem
        self.mode = mode
        self.scrolls = scrolls
        self.onWrite = onWrite
        self.onCancel = onCancel
        _text = State(initialValue: result.yaml)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 6) {
                Text(mode == .edit ? "改这份清单" : "写一份清单")
                    .font(Face.sans(13.5, .medium))
                    .foregroundStyle(Ink.ink)
                Text(short(result.path))
                    .font(Face.mono(10.5))
                    .foregroundStyle(Ink.inkFaint)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            .padding(.horizontal, Metrics.gutter + 2)
            .padding(.vertical, 12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Ink.surface)
            Hairline()
            Group {
                if scrolls {
                    TextEditor(text: $text)
                        .font(Face.mono(11))
                        .foregroundStyle(Ink.ink)
                        .scrollContentBackground(.hidden)
                        .padding(.horizontal, Metrics.gutter)
                        .padding(.vertical, 8)
                } else {
                    Text(text)
                        .font(Face.mono(11))
                        .foregroundStyle(Ink.ink)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, Metrics.gutter)
                        .padding(.vertical, 8)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            Hairline()
            HStack(alignment: .bottom, spacing: 8) {
                VStack(alignment: .leading, spacing: 4) {
                    if drafting {
                        TimelineView(.periodic(from: .now, by: 1)) { context in
                            HStack(spacing: 7) {
                                StatusDot(tone: Ink.accent, breathing: true, reduced: reduce)
                                Text(draftStage)
                                    .font(Face.sans(11, .medium))
                                    .foregroundStyle(Ink.ink)
                                Text(elapsed(context.date))
                                    .font(Face.mono(10))
                                    .foregroundStyle(Ink.inkFaint)
                            }
                        }
                        Text(draftMessage)
                            .font(Face.sans(10.5))
                            .foregroundStyle(Ink.inkFaint)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    } else {
                        Text(mode == .edit
                             ? "保存后后台会重新读取这份清单。"
                             : "写入后这个 worktree 会出现在左栏，服务可以先留空。")
                            .font(Face.sans(11))
                            .foregroundStyle(Ink.inkFaint)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    if let problem, !problem.isEmpty {
                        Text(problem)
                            .font(Face.sans(11))
                            .foregroundStyle(Ink.ink)
                            .fixedSize(horizontal: false, vertical: true)
                            .lineLimit(4)
                    }
                    if let draftProblem, !draftProblem.isEmpty {
                        Text(draftProblem)
                            .font(Face.sans(11))
                            .foregroundStyle(Ink.ink)
                            .fixedSize(horizontal: false, vertical: true)
                            .lineLimit(4)
                    }
                }
                Spacer(minLength: 12)
                if mode == .create {
                    SheetButton(title: drafting ? "agent 起草中…" : "让 agent 起草",
                                enabled: !drafting) { draftWithAgent() }
                }
                SheetButton(title: "取消", action: onCancel)
                SheetButton(title: mode == .edit ? "保存" : "写进 oberth.yaml",
                            primary: true, enabled: !drafting) { onWrite(text) }
            }
            .padding(.horizontal, Metrics.gutter + 2)
            .padding(.vertical, 10)
            .background(Ink.surface)
        }
        .frame(width: 600, height: 440)
        .background(Ink.canvas)
    }

    private func short(_ path: String) -> String {
        path.replacingOccurrences(of: FileManager.default.homeDirectoryForCurrentUser.path, with: "~")
    }

    private func elapsed(_ now: Date) -> String {
        let seconds = max(0, Int(now.timeIntervalSince(draftStartedAt)))
        return seconds == 1 ? "1 秒" : "\(seconds) 秒"
    }

    /// Ask the same local agent as `oberth init draft`, but keep the result in
    /// this review sheet. `--dry-run` is deliberate: the person still decides
    /// when the edited bytes become the project's manifest.
    private func draftWithAgent() {
        guard mode == .create else { return }
        drafting = true
        draftProblem = nil
        draftStage = "准备中"
        draftMessage = "正在读取项目事实"
        draftStartedAt = Date()
        let root = URL(fileURLWithPath: result.path).deletingLastPathComponent().path
        DispatchQueue.global(qos: .userInitiated).async {
            let outcome = CLI.stream(["init", "draft", "--dry-run", "--json", "--progress"],
                                     workingDirectory: root) { line in
                guard let data = line.data(using: .utf8),
                      let event = try? JSONDecoder().decode(DraftProgressEvent.self, from: data)
                else { return }
                DispatchQueue.main.async {
                    accept(event)
                }
            }
            DispatchQueue.main.async {
                switch outcome {
                case .success:
                    if drafting {
                        drafting = false
                        draftProblem = "agent 没有返回可用的 oberth.yaml 草稿。"
                    }
                case .failure(let failure):
                    drafting = false
                    draftProblem = failure.message
                }
            }
        }
    }

    private func accept(_ event: DraftProgressEvent) {
        switch event.type {
        case "progress":
            draftStage = stageTitle(event.stage ?? "")
            draftMessage = event.message ?? "agent 正在工作"
        case "done":
            guard let yaml = event.yaml,
                  !yaml.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
                drafting = false
                draftProblem = "agent 没有返回可用的 oberth.yaml 草稿。"
                return
            }
            text = yaml
            drafting = false
            draftStage = "草稿已就绪"
            draftMessage = "已回填编辑器，检查后再写入文件"
        case "error":
            drafting = false
            draftProblem = event.message
        default:
            break
        }
    }

    private func stageTitle(_ stage: String) -> String {
        switch stage {
        case "preparing": return "准备中"
        case "starting": return "启动 agent"
        case "inspecting": return "检查项目"
        case "validating": return "校验草稿"
        default: return "agent 工作中"
        }
    }

    private struct DraftProgressEvent: Decodable {
        let type: String
        let stage: String?
        let message: String?
        let yaml: String?
    }
}

/// Large action button used by manifest sheets.
struct SheetButton: View {
    let title: String
    var primary: Bool = false
    var enabled: Bool = true
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            Text(title)
                .font(Face.sans(12, .medium))
                .foregroundStyle(primary ? Color.white : Ink.ink)
                .padding(.horizontal, 12)
                .padding(.vertical, 5)
                .background {
                    RoundedRectangle(cornerRadius: 6, style: .continuous)
                        .fill(primary
                              ? (hovering ? Ink.accent.opacity(0.88) : Ink.accent)
                              : (hovering ? Ink.sunken : Ink.canvas))
                }
                .overlay {
                    if !primary {
                        RoundedRectangle(cornerRadius: 6, style: .continuous)
                            .strokeBorder(Ink.lineStrong, lineWidth: 1)
                    }
                }
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .opacity(enabled ? 1 : 0.45)
        .onHover { now in withAnimation(Motion.hover) { hovering = now && enabled } }
    }
}

extension GroupInitResult: Identifiable {
    var id: String { path }
}
