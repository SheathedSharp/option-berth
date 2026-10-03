import AppKit
import SwiftUI

/// 一条能拖的竖分隔线。
///
/// 面板的宽度不该只有作者能定：清单占多宽、左栏占多宽，是看的人当时的事。
/// 命中区比那条 1pt 的线宽得多 —— 1pt 是拖不到的，鼠标得稳到像素级才行。
struct SplitHandle: View {
    @Binding var width: Double
    let range: ClosedRange<Double>

    @State private var hovering = false
    /// 拖之前那条线的宽度。手势给的位移是从**开始拖**那一刻算的，而宽度每次事件
    /// 都在变 —— 不加这个基准，拖一格会跳两格。
    @State private var start: Double?

    var body: some View {
        Rectangle()
            .fill(hovering ? Ink.lineStrong : Ink.line)
            .frame(width: 1)
            .frame(width: Self.hitWidth)
            .contentShape(Rectangle())
            .onHover { inside in
                hovering = inside
                // 光标是这一条唯一的提示：这里可以拖。
                if inside {
                    NSCursor.resizeLeftRight.push()
                } else {
                    NSCursor.pop()
                }
            }
            .gesture(
                DragGesture(minimumDistance: 1)
                    .onChanged { value in
                        let base = start ?? width
                        if start == nil { start = base }
                        let moved = base + Double(value.translation.width)
                        width = min(max(moved, range.lowerBound), range.upperBound)
                    }
                    .onEnded { _ in start = nil }
            )
    }

    /// 命中区的宽度。布局上它真的占这么宽 —— 两边的 pane 要按这个数算剩下的空间。
    static let hitWidth: CGFloat = 7
}
