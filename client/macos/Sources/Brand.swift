import AppKit
import CoreText
import SwiftUI

// 这一层是品牌，不是样式表。它回答三个问题：用什么颜色、用什么字、什么时候动。
//
// 和上一版的关系：上一版是「档案纸」—— 六个黑白灰、一个彩色都没有，状态只能靠
// 粗细和边框表达。这一版回到暖纸底，多了**一个**低饱和陶土强调色。它只回答一个问题：
// 「这里有东西活着」。剩下的层级仍然是发丝线和字距的活，颜色不参与分层 ——
// 所以整个界面里带颜色的地方仍然数得出来。

extension Color {
    init(hex: UInt32) {
        self.init(
            .sRGB,
            red: Double((hex >> 16) & 0xFF) / 255,
            green: Double((hex >> 8) & 0xFF) / 255,
            blue: Double(hex & 0xFF) / 255,
            opacity: 1
        )
    }
}

/// 当前主题的基础色板。默认主题是暖纸浅色板，设置页也可以切换到冷白、深色和森林方案。
///
/// 纸张灰阶全部带一点暖底 —— 纯灰放在纸上会显脏，轻微的棕灰更像真实的阅读材料。
/// 强调色只有一个，且只用在**状态**上（还活着 / 选中 / 主操作），
/// 不用于装饰。想再加一个颜色之前先问：这个信息能不能用粗细、边框、字距表达？多半能。
enum Ink {
    static var canvas: Color { UISettings.shared.canvasColor }
    static var surface: Color { UISettings.shared.surfaceColor }      // 左栏 / 表头
    static var sunken: Color { UISettings.shared.sunkenColor }       // 悬停 / 内嵌块

    static var ink: Color { UISettings.shared.inkColor }          // 正文
    static var inkMuted: Color { UISettings.shared.inkMutedColor }     // 次级
    static var inkFaint: Color { UISettings.shared.inkFaintColor }     // 三级：标签、时间
    static var dormant: Color { UISettings.shared.dormantColor }      // 「空的泊位」—— 结构色，不是文字色

    static var line: Color { UISettings.shared.lineColor }         // 发丝线
    static var lineStrong: Color { UISettings.shared.lineStrongColor }   // 强规则线、刻度

    static var accent: Color { UISettings.shared.accentColor }       // 强调：选中、主操作
    static var accentSoft: Color { UISettings.shared.accentHex == nil
        ? UISettings.shared.accentSoftColor
        : UISettings.shared.accentColor.opacity(UISettings.shared.theme == .midnight || UISettings.shared.theme == .forest ? 0.22 : 0.10) }
    static var live: Color { UISettings.shared.liveColor }         // 实时：还在监听的那个点

}

/// 改动本身的三种颜色：加、删、改了。
///
/// **这是「一个强调色」那条规矩的例外，而且是有意的。** 上面那套灰阶回答的是
/// 「哪儿还活着」；而 diff 和改动清单是**数据**：哪几行是加的、哪几个文件没了，
/// 是这一屏的全部内容。绿加红删是所有人已经会读的一套写法（命令行那版也这么上色），
/// 换成同色系的中性色反而要读两遍才明白。
///
/// 三个色都压得比 git 的默认色深、底比它的浅：要在白底上读一整屏代码，
/// 不能像报错那样喊。冲突（`UU`）用红 —— 它是这一面唯一真的需要停下来的状态。
enum Change {
    static let added = Color(hex: 0x1B7F4B)        // 加：绿
    static let addedBand = Color(hex: 0xE9F5ED)    // 加行的底
    static let removed = Color(hex: 0xB3261E)      // 删：红
    static let removedBand = Color(hex: 0xFDECEA)  // 删行的底
    static let changed = Color(hex: 0x9A5B00)      // 改了：琥珀
    static let changedBand = Color(hex: 0xFBF3E4)  // 改行的底；端口表上那枚琥珀标签的底
}

/// 历史树上分道的颜色 —— 八个，白底上分得开，也都不抢 `Change` 那三色的语义：
/// 那三个是**数据的状态**（加了、删了、改了），这八个是**身份**（谁是谁）。
///
/// 取色按 **lane id**，不按列：列会回收（图才窄），按列上色迟早会把一条分支的颜色
/// 交给后来的另一条分支。
enum Lane {
    static let palette: [Color] = [
        Color(hex: 0x2F6FD0),   // 蓝
        Color(hex: 0x12A0A0),   // 青
        Color(hex: 0x7A5AF8),   // 紫
        Color(hex: 0xC2477B),   // 品红
        Color(hex: 0xD9822B),   // 橙
        Color(hex: 0x6B8E23),   // 橄榄
        Color(hex: 0xA0522D),   // 赭
        Color(hex: 0x5C6BC0),   // 靛
    ]

    static func color(forLaneID id: Int) -> Color {
        let count = palette.count
        return palette[((id % count) + count) % count]
    }
}

enum Metrics {
    // 红绿灯的让位条。窗口是 `.hiddenTitleBar`，标题栏透明、还在内容之上，
    // 所以这一条**必须是左栏自己的上内边距**，不能横在左栏上面 ——
    // 横在上面的话系统按标题栏高度（32pt）让一次、这一条再让一次，窗口顶上
    // 会空出 68pt 的白带，而离屏渲染里只看得到 36pt（那条路没有窗口）。
    //
    // **只有左栏用这一条**：那三个点在 x 9..69，正好坐在左栏上；内容区从 x=173
    // 才开始，头顶是空的，给它让位就是在右上角白留一片。详见 BoardView.body。
    static let trafficLightInset: CGFloat = 36
    static let railWidth: CGFloat = 172
    static let rowHeight: CGFloat = 40
    static let gutter: CGFloat = 16
    /// 端口列。等宽之后这个宽度就是「6 位端口 + 两个字符的余量」。
    static let portColumn: CGFloat = 62
}

// MARK: - 字体

/// 打包进来的等宽字体。它是数据那一半的声音：端口、地址、路径、计数、时间。
///
/// 用 Monaspace Neon（GitHub Next，SIL OFL 1.1，许可原文在 Fonts/LICENSE-Monaspace.txt）。
/// 选它不是因为它是「编程字体」，是因为它在小字号下 x-height 稳、字宽一致，
/// 而这两件事决定了一列数字能不能对齐着读。
enum FontBook {
    static let monoSemibold = "MonaspaceNeon-SemiBold"
    static let monoMedium = "MonaspaceNeon-Medium"
    static let monoRegular = "MonaspaceNeon-Regular"

    /// 字体到底用上没上。
    ///
    /// 注册就写在取值路径里（`static let` 是原子的懒初始化），所以谁先读到都对，
    /// 不需要谁记得去调一个 `register()`。
    /// 拿不到就退回系统等宽 —— 界面不该因为少一个字体文件而错版。
    static let monoAvailable: Bool = {
        let bundle = Bundle.main
        let urls = (bundle.urls(forResourcesWithExtension: "otf", subdirectory: nil) ?? [])
            + (bundle.urls(forResourcesWithExtension: "otf", subdirectory: "Fonts") ?? [])
        for url in urls {
            CTFontManagerRegisterFontsForURL(url as CFURL, .process, nil)
        }
        return NSFont(name: monoRegular, size: 12) != nil
    }()
}

enum MonoWeight {
    case regular, medium, semibold

    var postScript: String {
        switch self {
        case .regular: return FontBook.monoRegular
        case .medium: return FontBook.monoMedium
        case .semibold: return FontBook.monoSemibold
        }
    }

    var system: Font.Weight {
        switch self {
        case .regular: return .regular
        case .medium: return .medium
        case .semibold: return .semibold
        }
    }
}

/// 两套声音：等宽给数据，系统无衬线给人话。
///
/// 中文不进取 —— Monaspace 没有汉字，混排时汉字本来就会回落到系统字体，
/// 与其让一列字在中英之间换度量，不如一开始就分清楚谁用哪套。
enum Face {
    static func mono(_ size: CGFloat, _ weight: MonoWeight = .regular) -> Font {
        let scaled = size * UISettings.shared.dataScale
        let selected = UISettings.shared.dataFontName
        if selected != "Monaspace Neon" && selected != "__system__" {
            return .custom(selected, size: scaled).weight(weight.system)
        }
        guard FontBook.monoAvailable else {
            return .system(size: scaled, weight: weight.system, design: .monospaced)
        }
        return .custom(weight.postScript, size: scaled)
    }

    static func sans(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font {
        let scaled = size * UISettings.shared.interfaceScale
        if UISettings.shared.interfaceFontName == "__system__" {
            return .system(size: scaled, weight: weight)
        }
        return .custom(UISettings.shared.interfaceFontName, size: scaled).weight(weight)
    }

    /// Editorial display face for the few moments that orient a reader.
    /// New York gives the paper theme a distinct voice while body copy stays
    /// in the familiar system sans for fast scanning and Chinese fallback.
    static func display(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font {
        let scaled = size * UISettings.shared.interfaceScale
        if UISettings.shared.interfaceFontName == "__system__" {
            return .system(size: scaled, weight: weight, design: .serif)
        }
        return .custom(UISettings.shared.interfaceFontName, size: scaled).weight(weight)
    }

    /// Log rows have their own scale because they can contain a lot more text
    /// than the surrounding service controls.
    static func log(_ size: CGFloat, _ weight: MonoWeight = .regular) -> Font {
        let scaled = size * UISettings.shared.logScale
        let selected = UISettings.shared.dataFontName
        if selected != "Monaspace Neon" && selected != "__system__" {
            return .custom(selected, size: scaled).weight(weight.system)
        }
        guard FontBook.monoAvailable else {
            return .system(size: scaled, weight: weight.system, design: .monospaced)
        }
        return .custom(weight.postScript, size: scaled)
    }
}

// MARK: - 动效

/// 动效令牌。所有数字都从这里取，不许在视图里现编。
///
/// 原则来自 Apple 流体界面那一套：**能被抓住、能从当前值接上、能被中途反悔**。
/// 所以会动的状态一律用弹簧而不是曲线动画 —— 弹簧天生带速度和位置，
/// 打断的时候不会跳。
///
/// 频率是闸门：一天上百次的动作（键盘、悬停）不该有动画；偶尔发生的（切范围、
/// 展开）才给标准档。这个应用里唯一跑个不停的动画是「daemon 正在启动」——
/// 那是一个转瞬即逝的状态，不是常驻装饰。
enum Motion {
    /// 左栏选中指示器滑过去。
    static let selection = Animation.spring(response: 0.30, dampingFraction: 0.80)
    /// 范围切换时内容区的交叉淡入。
    static let swap = Animation.spring(response: 0.34, dampingFraction: 0.88)
    /// 悬停与按下。一天几十次，只能「几乎察觉不到」。
    static let hover = Animation.easeOut(duration: 0.12)
    /// 「正在启动」的呼吸。
    static let breathe = Animation.easeInOut(duration: 1.4).repeatForever(autoreverses: true)

    // reduced motion 下换成更温和的变体，不是直接关掉 —— 淡入还在，
    // 位移和回弹去掉。动效里承载信息的那部分（东西出现了）要保住。
    static func selection(reduced: Bool) -> Animation {
        reduced ? .easeOut(duration: 0.15) : selection
    }

    static func swap(reduced: Bool) -> Animation {
        reduced ? .easeOut(duration: 0.12) : swap
    }

    static func breathe(reduced: Bool) -> Animation? {
        reduced ? nil : breathe
    }
}

// MARK: - 标志

/// 标志自己的三个色。它和界面那套 `Ink` **分开**：界面只有「活着」一处颜色，
/// 标志是三个角色，每个有自己的名字和工作 —— 要改标志的颜色，改这里。
///
/// 三个角色：陆地和船都是暖的，深陶土负责开口；底是象牙白。
/// 这条关系是配了很多轮才定的：底一暖，陶土才不显得突兀。
enum Mark {
    static let land = Color(hex: 0x23262B)       // 陆地：暖炭
    static let water = Color(hex: 0xA8543E)      // 河与水：深陶土
    static let boat = Color(hex: 0xC97954)       // 船：陶土
    static let paper = Color(hex: 0xFAF7F1)      // 标志自己的底：象牙白
}

/// 泊位标志：Option 键那个字符，照它的实测比例重画，给了三个角色。
///
/// 左上一横是**陆地**，一笔斜线接底横是**河与水**，右上那个向下的梯形是**船**；
/// 三块之间留气口，谁也不贴着谁。左上与右上之间那道断口就是**泊位入口**。
///
/// 比例全部来自那个字符本身（font-size 420 下逐行扫描墨段量的，不是估的）：
/// 笔宽 = 可见宽的 8.86%、斜线斜率正好 2.0、左上横 3.0 个笔宽、右上横 3.55 个、
/// 底横 2.95 个、两段顶横的断口 2.7 个笔宽。这几组数之间本来就是配好的 ——
/// 所以长度和宽度不用再自己编。几何画在 24×24 网格上再等比缩放，
/// 16pt（角标）和 1024px（图标）是同一张图。
struct BerthMark: View {
    var size: CGFloat = 18
    /// 泊位空着的时候船画成灰的。用在空态和连不上 daemon 的时候 ——
    /// 那时候标志本身也在说同一句话：没有东西停在这儿。
    var occupied: Bool = true
    /// 小尺寸那一档：笔宽加粗一档。真字符的 8.86% 缩到 16pt 只有 1.2px，
    /// 加粗到 2.15 才立得住；再小就把气口吃掉了。
    var compact: Bool = false

    // —— 24 网格上的骨架（字符实测值换算来的）——
    private var unit: CGFloat { size / 24 }
    private var inkU: CGFloat { compact ? 2.15 : 1.86 }   // 笔宽（网格单位）
    private var ink: CGFloat { inkU * unit }              // 笔宽（点）

    private let ly: CGFloat = 3.783      // 两段顶横的中心线
    private let l1x0: CGFloat = 2.433    // 陆地左端
    private let l1x1: CGFloat = 8.013    // 陆地右端（= 河水起笔该在的地方）
    private let kx: CGFloat = 9.072      // 河水实际起笔（退开一个气口）
    private let ky: CGFloat = 5.948
    private let ebx: CGFloat = 16.014    // 肘
    private let ba: CGFloat = 20.139     // 底横的 y
    private let rx: CGFloat = 21.504     // 底横右端
    private let b0: CGFloat = 14.904     // 船左端
    private let b1: CGFloat = 21.504     // 船右端（与底横右端对齐）
    private let bin: CGFloat = 1.05      // 船底每侧内收
    private let bd: CGFloat = 1.30       // 船高 = 1.30 个笔宽
    private let br: CGFloat = 0.50       // 船的转角半径

    var body: some View {
        ZStack {
            line(Mark.land) { p in
                p.move(to: pt(l1x0, ly))
                p.addLine(to: pt(l1x1, ly))
            }
            line(Mark.water) { p in
                p.move(to: pt(kx, ky))
                p.addLine(to: pt(ebx, ba))
                p.addLine(to: pt(rx, ba))
            }
            boatBody.fill(occupied ? Mark.boat : Ink.dormant)
        }
        .frame(width: size, height: size)
    }

    /// 船：向下的梯形（宽顶窄底 = 船体剪影），四角倒成小圆角。
    private var boatBody: Path {
        let top = ly - inkU / 2
        let bot = top + inkU * bd
        return roundedPolygon(
            [pt(b0, top), pt(b1, top), pt(b1 - bin, bot), pt(b0 + bin, bot)],
            radius: br * unit
        )
    }

    private func pt(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
        CGPoint(x: x * unit, y: y * unit)
    }

    /// 一笔：等宽、两端圆头、转角圆。整个标志只用这一种笔画语法。
    private func line(_ color: Color, _ build: (inout Path) -> Void) -> some View {
        Path { build(&$0) }
            .stroke(color, style: StrokeStyle(lineWidth: ink, lineCap: .round, lineJoin: .round))
    }

    /// 圆角多边形。倒角用二次曲线，和别的元素同一套圆角语法。
    private func roundedPolygon(_ points: [CGPoint], radius: CGFloat) -> Path {
        var path = Path()
        let n = points.count
        for i in 0..<n {
            let p0 = points[(i - 1 + n) % n], p1 = points[i], p2 = points[(i + 1) % n]
            let v1 = CGVector(dx: p0.x - p1.x, dy: p0.y - p1.y)
            let v2 = CGVector(dx: p2.x - p1.x, dy: p2.y - p1.y)
            let len1 = max(hypot(v1.dx, v1.dy), 0.0001)
            let len2 = max(hypot(v2.dx, v2.dy), 0.0001)
            let u1 = CGVector(dx: v1.dx / len1, dy: v1.dy / len1)
            let u2 = CGVector(dx: v2.dx / len2, dy: v2.dy / len2)
            let cosA = max(-1, min(1, u1.dx * u2.dx + u1.dy * u2.dy))
            let t = min(radius / max(tan(acos(cosA) / 2), 0.0001), len1 / 2, len2 / 2)
            let a = CGPoint(x: p1.x + u1.dx * t, y: p1.y + u1.dy * t)
            let b = CGPoint(x: p1.x + u2.dx * t, y: p1.y + u2.dy * t)
            if i == 0 { path.move(to: a) } else { path.addLine(to: a) }
            path.addQuadCurve(to: b, control: p1)
        }
        path.closeSubpath()
        return path
    }
}

/// 字标。中间那一横用标志里那条深陶土 —— 它是「标志」和「名字」之间
/// 唯一的那处呼应，也是整个界面上最小的一点颜色。
///
/// **名字是拆成三段写的，这是刻意的**（只有连字符是彩色的，得单独一个 Text）。
/// 代价是任何按字符串改名的脚本都看不见它 —— 全仓库搜 `code-berth` 搜不到这里，
/// 两次改名都漏过这一处，所以改名字时记得手改这一行。
struct Wordmark: View {
    var size: CGFloat = 12.5

    var body: some View {
        HStack(spacing: 0) {
            Text("option").foregroundStyle(Ink.ink)
            Text("-").foregroundStyle(Mark.water)
            Text("berth").foregroundStyle(Ink.ink)
        }
        .font(Face.sans(size, .semibold))
        .tracking(0.1)
        .lineLimit(1)
        .fixedSize()
    }
}

// MARK: - 零件

/// 一条 1pt 的发丝线。层级靠它表达，不用阴影。
struct Hairline: View {
    var axis: Axis = .horizontal
    var tone: Color = Ink.line

    var body: some View {
        Rectangle()
            .fill(tone)
            .frame(
                width: axis == .vertical ? 1 : nil,
                height: axis == .horizontal ? 1 : nil
            )
    }
}

/// 列标题 / 分区标题。无衬线大写 + 字距 —— 让它和正文拉开层级，
/// 不靠额外颜色制造噪音。
struct ColumnLabel: View {
    let text: String
    var tone: Color = Ink.inkFaint
    var size: CGFloat = 9.5

    var body: some View {
        Text(text.uppercased())
            .font(Face.sans(size, .medium))
            .tracking(1.0)
            .foregroundStyle(tone)
            .lineLimit(1)
    }
}

/// 行上的标签。监听详情只有两件事配得上它：监听地址不是回环（蓝），以及工作目录
/// 在废纸篓里（琥珀）。两个都是**扫表时不能漏掉的事实**，也都不是分类 ——
/// 那一行是什么还是什么。
struct Tag: View {
    let text: String
    var tone: Tone = .accent

    enum Tone {
        case accent, quiet, warn

        var fg: Color {
            switch self {
            case .accent: return Ink.accent
            case .quiet: return Ink.inkMuted
            case .warn: return Change.changed
            }
        }

        var bg: Color {
            switch self {
            case .accent: return Ink.accentSoft
            case .quiet: return Ink.sunken
            case .warn: return Change.changedBand
            }
        }
    }

    var body: some View {
        Text(text)
            .font(Face.sans(10.5, .medium))
            .foregroundStyle(tone.fg)
            .padding(.horizontal, 6)
            .padding(.vertical, 1.5)
            .background(RoundedRectangle(cornerRadius: 4, style: .continuous).fill(tone.bg))
            .fixedSize()
    }
}

/// 状态点。整个界面上唯一一个「会亮」的东西。
struct StatusDot: View {
    var tone: Color
    var breathing: Bool = false
    var reduced: Bool = false

    @State private var dim = false

    var body: some View {
        Circle()
            .fill(tone)
            .frame(width: 6, height: 6)
            .opacity(breathing && dim ? 0.25 : 1)
            .animation(breathing ? Motion.breathe(reduced: reduced) : nil, value: dim)
            .onAppear { if breathing && !reduced { dim = true } }
            .onChange(of: breathing) { _, now in dim = now && !reduced }
    }
}

/// 一个「?」：这一节的详情挂在这后面。
///
/// 说一遍就够了的解释都收进它，标题那一行才留得住 —— 但是**它和标题各是一个按钮**：
/// 点它不该把这一节折起来（所以在外面的是两个独立的可点区，不是一个整行的按钮）。
struct InfoMark: View {
    /// 卡片正开着的时候（悬停或钉住）转成强调色，让「这是哪一节的详情」有着落。
    var on: Bool = false

    var body: some View {
        Text("?")
            .font(Face.mono(9.5, .medium))
            .foregroundStyle(on ? Ink.accent : Ink.inkFaint)
            .frame(width: 14, height: 14)
            .background { Circle().fill(on ? Ink.accentSoft : Ink.sunken) }
            .overlay { Circle().strokeBorder(on ? Ink.accent : Ink.lineStrong, lineWidth: 1) }
            .contentShape(Circle())
    }
}

/// 详情的浮层卡片。
///
/// **不用阴影** —— 这个界面从头到尾没有一处阴影，层级一直是靠发丝线表达的。
/// 浮层要离开纸面，靠的是底色（`Ink.surface`）和一圈描边。
///
/// 宽度是给「面板 560、左右各留 18」定的：300 的卡片挂在任何一节的标题下面，
/// 都不会顶到边。
struct TipCard: View {
    let text: String

    var body: some View {
        Text(text)
            .font(Face.sans(11))
            .foregroundStyle(Ink.ink)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
            .frame(width: 300, alignment: .leading)
            .background {
                RoundedRectangle(cornerRadius: 6, style: .continuous).fill(Ink.surface)
            }
            .overlay {
                RoundedRectangle(cornerRadius: 6, style: .continuous)
                    .strokeBorder(Ink.lineStrong, lineWidth: 1)
            }
    }
}
