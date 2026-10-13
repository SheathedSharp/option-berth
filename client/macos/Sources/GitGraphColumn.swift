import SwiftUI

/// Per-row graph column. Lane assignment still comes from Xit's
/// `CommitHistory.generateConnections` (see `GitGraphLayout`); the stroke is a
/// smooth lane curve: straight when a branch stays in its column, cubic when
/// it joins or leaves a commit. SwiftUI's y axis points down.
enum GitGraphMetrics {
    static var columnWidth: CGFloat { Metrics.unit * 4 }
    static var leftMargin: CGFloat { Metrics.unit * 3 }
    static var lineWidth: CGFloat { max(1.75, Metrics.unit * 0.5) }
    static var dotRadius: CGFloat { Metrics.unit + 1 }
    static var rowHeight: CGFloat { Metrics.rowHeight }

    static func width(columns: Int) -> CGFloat {
        leftMargin + CGFloat(max(columns, 1)) * columnWidth + Metrics.unit * 2
    }

    static func center(_ index: Int) -> CGFloat {
        leftMargin + columnWidth * CGFloat(index) + columnWidth / 2
    }
}

struct GitGraphColumn: View {
    let row: GitGraphLayout.Row
    var head = false
    var selected = false

    var body: some View {
        Canvas { context, size in
            let mid = size.height / 2
            let dot = CGPoint(x: GitGraphMetrics.center(row.dotColumn), y: mid)
            for line in row.lines {
                var path = Path()
                switch (line.parentIndex, line.childIndex) {
                case (nil, let child?):
                    curve(&path, from: CGPoint(x: GitGraphMetrics.center(child), y: 0), to: dot)
                case (let parent?, nil):
                    curve(&path, from: dot, to: CGPoint(x: GitGraphMetrics.center(parent), y: size.height))
                case (let parent?, let child?):
                    curve(&path,
                          from: CGPoint(x: GitGraphMetrics.center(child), y: 0),
                          to: CGPoint(x: GitGraphMetrics.center(parent), y: size.height))
                case (nil, nil):
                    break
                }
                context.stroke(path, with: .color(Lane.color(forLaneID: line.colorIndex)),
                               style: StrokeStyle(lineWidth: GitGraphMetrics.lineWidth, lineCap: .round, lineJoin: .round))
            }
            let color = Lane.color(forLaneID: row.dotColor)
            let radius = GitGraphMetrics.dotRadius
            // Knock the lane out behind the node so the curve meets the dot
            // instead of cutting through it.
            var knockout = Path()
            knockout.addEllipse(in: circle(dot, radius + 2.5))
            context.fill(knockout, with: .color(selected ? Ink.accentSoft : Ink.canvas))
            var disc = Path()
            disc.addEllipse(in: circle(dot, radius))
            context.fill(disc, with: .color(color))
            if head {
                var halo = Path()
                halo.addEllipse(in: circle(dot, radius + 4))
                context.stroke(halo, with: .color(Ink.live), lineWidth: 1.5)
            }
        }
        .accessibilityHidden(true)
    }

    private func circle(_ center: CGPoint, _ radius: CGFloat) -> CGRect {
        CGRect(x: center.x - radius, y: center.y - radius, width: radius * 2, height: radius * 2)
    }

    private func curve(_ path: inout Path, from: CGPoint, to: CGPoint) {
        path.move(to: from)
        if abs(from.x - to.x) < 0.5 {
            path.addLine(to: to)
            return
        }
        let midY = (from.y + to.y) / 2
        path.addCurve(to: to, control1: CGPoint(x: from.x, y: midY), control2: CGPoint(x: to.x, y: midY))
    }
}
