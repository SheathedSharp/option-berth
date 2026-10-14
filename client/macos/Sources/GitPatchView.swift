import SwiftUI

/// Shared line-number and hunk chrome for worktree patches and historical
/// commit patches. It only restyles lines `oberth git diff` already returned.
struct GitPatchView: View {
    let lines: [GitPatchLine]
    /// Owns a scroll view. A history file passes false and scrolls with its siblings.
    var scrolls = true
    /// Fills the single-file changes pane when that pane is not scrolling.
    var fills = false
    var maxLines: Int?

    var body: some View {
        let capped = maxLines.map { min($0, lines.count) } ?? lines.count
        let slice = Array(lines.prefix(capped))
        let rows = GitPatchRows.make(slice)
        let digits = GitPatchRows.digits(rows)
        let stack = VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                GitPatchLineRow(row: row, digits: digits, fillsWidth: fills)
            }
        }
        if scrolls {
            ScrollView([.horizontal, .vertical]) {
                VStack(alignment: .leading, spacing: 0) {
                    stack
                    if capped < lines.count {
                        Text("… 还有 \(lines.count - capped) 行未显示")
                            .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                            .padding(Metrics.gutter)
                    }
                }.textSelection(.enabled)
            }
        } else if fills {
            stack.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading).clipped()
        } else {
            stack.frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

private struct GitPatchLineRow: View {
    let row: GitPatchRow
    let digits: Int
    var fillsWidth = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 0) {
            if showsGutter {
                number(row.oldNumber, color: row.kind == "del" ? Change.removed : Ink.inkFaint)
                number(row.newNumber, color: row.kind == "add" ? Change.added : Ink.inkFaint)
                    .padding(.leading, 4)
            }
            Text(row.text.isEmpty ? " " : row.text)
                .font(Face.mono(row.kind == "meta" || row.kind == "hunk" ? Metrics.meta : Metrics.chrome, row.kind == "hunk" ? .medium : .regular))
                .foregroundStyle(foreground)
                .fixedSize(horizontal: true, vertical: false)
                .padding(.leading, showsGutter ? Metrics.inline : Metrics.gutter)
                .padding(.trailing, Metrics.gutter)
        }
        .padding(.vertical, row.kind == "hunk" || row.kind == "meta" ? 2 : 0)
        .frame(maxWidth: fillsWidth ? .infinity : nil, alignment: .leading)
        .background(background)
        .accessibilityIdentifier(row.kind == "hunk" ? "git.review.hunk" : "git.review.patch.line")
    }

    private var showsGutter: Bool { row.kind == "add" || row.kind == "del" || row.kind == "context" }

    private var foreground: Color {
        switch row.kind {
        case "add": return Change.added
        case "del": return Change.removed
        case "hunk": return Change.changed
        case "meta": return Ink.inkMuted
        default: return Ink.ink
        }
    }

    private var background: Color {
        switch row.kind {
        case "add": return Change.addedBand
        case "del": return Change.removedBand
        case "hunk": return Change.changedBand
        case "meta": return Ink.sunken
        default: return Color.clear
        }
    }

    private func number(_ value: Int?, color: Color) -> some View {
        Text(value.map(String.init) ?? "")
            .font(Face.mono(Metrics.meta))
            .foregroundStyle(color)
            .frame(width: gutterWidth, alignment: .trailing)
    }

    private var gutterWidth: CGFloat {
        CGFloat(digits) * 7 * CGFloat(UISettings.shared.dataScale) + Metrics.unit
    }
}
