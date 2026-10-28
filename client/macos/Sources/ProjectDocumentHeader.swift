import SwiftUI

/// Project content document header — session-primary v13.
///
/// Squint leader for the detail column: display name carries weight; a live
/// **facts strip** (ready / attention) sits on the title line. Clicking facts
/// opens the services capability panel (`/ob-server`). Branch + path form the
/// second line.
struct ProjectDocumentHeader<Trailing: View>: View {
    let displayName: String
    let liveCount: Int
    let serviceCount: Int
    /// Services that need operator attention (需重启 / 失败 / 超时…).
    var attentionCount: Int = 0
    let branch: String
    let path: String
    var isWorking: Bool = false
    var onFactsTap: (() -> Void)? = nil
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline, spacing: Metrics.inline) {
                titleBlock
                Spacer(minLength: Metrics.unit)
                if isWorking {
                    ProgressView()
                        .controlSize(.small)
                        .help("正在处理项目服务")
                }
                trailing()
            }
            .padding(.horizontal, Metrics.gutter)
            .padding(.top, Metrics.gutter + Metrics.unit)
            .padding(.bottom, Metrics.unit)

            metaLine
                .padding(.horizontal, Metrics.gutter)
                .padding(.bottom, Metrics.gutter)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.surface)
        .overlay(alignment: .bottom) {
            Rectangle()
                .fill(Ink.lineStrong)
                .frame(height: 1)
        }
    }

    private var titleBlock: some View {
        HStack(alignment: .firstTextBaseline, spacing: Metrics.inline) {
            Text(displayName)
                .font(Face.display(15, .semibold))
                .foregroundStyle(Ink.ink)
                .lineLimit(1)
            factsStrip
                .layoutPriority(-1)
        }
    }

    /// Live status compound — opens `/ob-server` when tapped.
    private var factsStrip: some View {
        let label = HStack(spacing: Metrics.unit) {
            if attentionCount > 0 {
                Text(verbatim: "\(attentionCount) 需处理")
                    .font(Face.mono(Metrics.chrome, .medium))
                    .monospacedDigit()
                    .foregroundStyle(Change.changed)
                    .lineLimit(1)
                Text("·")
                    .font(Face.sans(Metrics.meta))
                    .foregroundStyle(Ink.inkFaint)
            }
            Text(verbatim: "\(liveCount)/\(serviceCount) 在跑")
                .font(Face.mono(Metrics.chrome))
                .monospacedDigit()
                .foregroundStyle(Ink.inkMuted)
                .lineLimit(1)
        }
        .contentShape(Rectangle())
        .help("打开服务面板 · /ob-server")
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("workspace.runtimeFacts")
        // v13 turned Services/Git into capability panels over the session stage
        // and the old face picker stopped mounting, which silently orphaned the
        // tour's `.facts` step: the guide ran out of targets after two stops.
        // This button is the live entry to that panel now, so it carries the
        // anchor the guide looks for (client/macos/docs/console.md「布局与导航」).
        .tourAnchor(.facts)

        return Group {
            if let onFactsTap {
                Button(action: onFactsTap) { label }
                    .buttonStyle(.plain)
            } else {
                label
            }
        }
    }

    private var metaLine: some View {
        HStack(spacing: Metrics.inline) {
            if !branch.isEmpty {
                Image(systemName: "arrow.triangle.branch")
                    .font(.system(size: 10, weight: .medium))
                    .foregroundStyle(Ink.inkFaint)
                    .accessibilityHidden(true)
                Text(branch)
                    .font(Face.mono(Metrics.meta))
                    .foregroundStyle(Ink.inkMuted)
                    .lineLimit(1)
                Text("·")
                    .font(Face.sans(Metrics.meta))
                    .foregroundStyle(Ink.inkFaint)
            }
            Text(path.isEmpty ? "目录不可用" : path)
                .font(Face.mono(Metrics.meta))
                .foregroundStyle(Ink.inkFaint)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: 0)
        }
    }
}

extension ProjectDocumentHeader where Trailing == EmptyView {
    init(displayName: String, liveCount: Int, serviceCount: Int,
         attentionCount: Int = 0, branch: String, path: String, isWorking: Bool = false,
         onFactsTap: (() -> Void)? = nil) {
        self.displayName = displayName
        self.liveCount = liveCount
        self.serviceCount = serviceCount
        self.attentionCount = attentionCount
        self.branch = branch
        self.path = path
        self.isWorking = isWorking
        self.onFactsTap = onFactsTap
        self.trailing = { EmptyView() }
    }
}
