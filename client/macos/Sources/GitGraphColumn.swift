import SwiftUI

/// Per-row graph column. Path geometry follows Xit `HistoryCellView.path(for:)`
/// (Copyright 2013 David Catmull, Apache-2.0); SwiftUI's y axis points down.
/// Lane color comes from the connection id, via `Lane`, not from the column.
enum GitGraphMetrics {
    static let columnWidth: CGFloat = 10
    static let leftMargin: CGFloat = 4
    static let lineWidth: CGFloat = 2
    static let dotDiameter: CGFloat = 6

    static func width(columns: Int) -> CGFloat {
        leftMargin + CGFloat(max(columns, 1)) * columnWidth + 8
    }

    static func center(_ index: Int) -> CGFloat {
        leftMargin + columnWidth * CGFloat(index) + columnWidth / 2
    }

    static func corner(_ from: Int, _ to: Int) -> CGFloat {
        min(CGFloat(abs(from - to)) * 0.25, lineWidth)
    }
}

struct GitGraphColumn: View {
    let row: GitGraphLayout.Row
    var head = false
    var merge = false

    var body: some View {
        Canvas { context, size in
            let height = size.height
            let dot = row.dotColumn
            for line in row.lines {
                var path = Path()
                switch (line.parentIndex, line.childIndex) {
                case (nil, let child?):
                    let bend = GitGraphMetrics.corner(dot, child)
                    path.move(to: CGPoint(x: GitGraphMetrics.center(child), y: 0))
                    path.addLine(to: CGPoint(x: GitGraphMetrics.center(child), y: bend))
                    path.addLine(to: CGPoint(x: GitGraphMetrics.center(dot), y: height / 2))
                case (let parent?, nil):
                    let bend = GitGraphMetrics.corner(dot, parent)
                    path.move(to: CGPoint(x: GitGraphMetrics.center(parent), y: height))
                    path.addLine(to: CGPoint(x: GitGraphMetrics.center(parent), y: height - bend))
                    path.addLine(to: CGPoint(x: GitGraphMetrics.center(dot), y: height / 2))
                case (let parent?, let child?):
                    path.move(to: CGPoint(x: GitGraphMetrics.center(child), y: 0))
                    if parent != child {
                        let bend = GitGraphMetrics.corner(child, parent)
                        path.addLine(to: CGPoint(x: GitGraphMetrics.center(child), y: bend))
                        path.addLine(to: CGPoint(x: GitGraphMetrics.center(parent), y: height - bend))
                    }
                    path.addLine(to: CGPoint(x: GitGraphMetrics.center(parent), y: height))
                case (nil, nil):
                    break
                }
                context.stroke(path, with: .color(Lane.color(forLaneID: line.colorIndex)),
                               style: StrokeStyle(lineWidth: GitGraphMetrics.lineWidth, lineCap: .round, lineJoin: .round))
            }
            let center = CGPoint(x: GitGraphMetrics.center(dot), y: height / 2)
            let color = Lane.color(forLaneID: row.dotColor)
            if merge {
                var ring = Path()
                ring.addEllipse(in: CGRect(x: center.x - 4.5, y: center.y - 4.5, width: 9, height: 9))
                context.stroke(ring, with: .color(color), lineWidth: 1.75)
            } else {
                var disc = Path()
                disc.addEllipse(in: CGRect(x: center.x - 3, y: center.y - 3, width: 6, height: 6))
                context.fill(disc, with: .color(color))
            }
            if head {
                var halo = Path()
                halo.addEllipse(in: CGRect(x: center.x - 6.5, y: center.y - 6.5, width: 13, height: 13))
                context.stroke(halo, with: .color(Ink.live), lineWidth: 1.5)
            }
        }
        .accessibilityHidden(true)
    }
}
