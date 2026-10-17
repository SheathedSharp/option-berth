import SwiftUI

/// Project content document header — Operate craft, not a flat HStack tweak.
///
/// Squint leader for the detail column (layout.md / operate.md): display name
/// carries weight; live compound sits quiet on the same line; branch + path
/// form the second line. Tone step (Ink.surface + lineStrong) separates header
/// from the face picker / table — not hairline costume everywhere.
///
/// Skills: operate (hierarchy from weight/size/grey), quieter (compound not badge),
/// craft-floor (no kicker, no pill), apple-macos-layout (familiar document chrome).
struct ProjectDocumentHeader<Trailing: View>: View {
    let displayName: String
    let liveCount: Int
    let serviceCount: Int
    let branch: String
    let path: String
    var isWorking: Bool = false
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
            .padding(.top, Metrics.section)
            .padding(.bottom, Metrics.inline)

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
                .font(Face.sans(Metrics.display, .semibold))
                .foregroundStyle(Ink.ink)
                .lineLimit(1)
            // Quiet live compound — not a badge pill (quieter / tells).
            Text(verbatim: "\(liveCount) · \(serviceCount) 服务")
                .font(Face.sans(Metrics.chrome))
                .foregroundStyle(Ink.inkMuted)
                .lineLimit(1)
                .layoutPriority(-1)
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
         branch: String, path: String, isWorking: Bool = false) {
        self.displayName = displayName
        self.liveCount = liveCount
        self.serviceCount = serviceCount
        self.branch = branch
        self.path = path
        self.isWorking = isWorking
        self.trailing = { EmptyView() }
    }
}
