import SwiftUI

/// Settings for the local client. Preferences apply to the whole window as
/// soon as they change. Explicit user files override legacy GUI preferences.
struct SettingsSheet: View {
    let onClose: () -> Void
    @ObservedObject var settings: UISettings
    var scrolls: Bool = true
    private let sheetHeight: CGFloat
    @State private var section: Section = .appearance
    @State private var jev: JevSettings
    @State private var jevNotice: String?
    @Environment(\.accessibilityReduceMotion) private var reduce

    enum Section: String, CaseIterable, Identifiable {
        case appearance
        case typography
        case workspace
        case jev

        var id: String { rawValue }

        var title: String {
            switch self {
            case .appearance: return "外观"
            case .typography: return "字体与字号"
            case .workspace: return "工作区"
            case .jev: return "Jev 增强"
            }
        }

        var subtitle: String {
            switch self {
            case .appearance: return "主题与颜色"
            case .typography: return "每个模块的阅读密度"
            case .workspace: return "连接与清单"
            case .jev: return "异常时把选择交还给人"
            }
        }
    }

    init(settings: UISettings = .shared, scrolls: Bool = true,
         initialSection: Section = .appearance, previewJev: Bool = false,
         renderHeight: CGFloat = 560,
         onClose: @escaping () -> Void = {}) {
        self.settings = settings
        self.scrolls = scrolls
        self.sheetHeight = renderHeight
        self.onClose = onClose
        _section = State(initialValue: initialSection)
        _jev = State(initialValue: previewJev ? .preview : JevSettingsStore.load())
        _jevNotice = State(initialValue: nil)
    }

    var body: some View {
        HStack(spacing: 0) {
            sidebar
            Hairline(axis: .vertical)
            VStack(alignment: .leading, spacing: 0) {
                header
                Hairline()
                if scrolls {
                    ScrollView {
                        content
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(24)
                    }
                } else {
                    content
                        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
                        .padding(24)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        }
        .frame(width: 720, height: sheetHeight)
        .background(Ink.canvas)
    }

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 9) {
                BerthMark(size: 22)
                Text("设置")
                    .font(Face.display(17, .semibold))
                    .foregroundStyle(Ink.ink)
            }
            .padding(.horizontal, 18)
            .padding(.top, 26)
            .padding(.bottom, 20)

            ForEach(Section.allCases) { item in
                Button {
                    withAnimation(Motion.selection(reduced: reduce)) { section = item }
                } label: {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(item.title)
                            .font(Face.sans(11.5, section == item ? .medium : .regular))
                        Text(item.subtitle)
                            .font(Face.sans(9.5))
                            .foregroundStyle(section == item ? Ink.accent.opacity(0.82) : Ink.inkFaint)
                    }
                    .foregroundStyle(section == item ? Ink.accent : Ink.inkMuted)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 18)
                    .padding(.vertical, 11)
                    .background(section == item ? Ink.accentSoft : Color.clear)
                }
                .buttonStyle(.plain)
            }
            Spacer(minLength: 0)
            Text("外观只影响此客户端；Jev 配置供本机 agent 使用")
                .font(Face.sans(9.5))
                .foregroundStyle(Ink.inkFaint)
                .fixedSize(horizontal: false, vertical: true)
                .padding(18)
        }
        .frame(width: 172, alignment: .leading)
        .background(Ink.surface)
    }

    private var header: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(section.title)
                    .font(Face.display(18, .medium))
                    .foregroundStyle(Ink.ink)
                Text(section.subtitle)
                    .font(Face.sans(10))
                    .foregroundStyle(Ink.inkFaint)
            }
            Spacer()
            SheetButton(title: "完成", primary: true, action: onClose)
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 14)
        .background(Ink.surface)
    }

    @ViewBuilder private var content: some View {
        switch section {
        case .appearance: appearanceContent
        case .typography: typographyContent
        case .workspace: workspaceContent
        case .jev: jevContent
        }
    }

    private var appearanceContent: some View {
        VStack(alignment: .leading, spacing: 22) {
            ClientConfigurationPanel(settings: settings, frozen: !scrolls)
            if settings.themeFilePresent {
                Text("theme.json 正在管理外观；从上方打开内置编辑器可即时修改。")
                    .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
            }

            VStack(alignment: .leading, spacing: 16) {
            settingHeading("强调色", detail: "未提供主题文件时，可在此调整纸张默认色")
            HStack(spacing: 12) {
                if scrolls {
                    ColorPicker("自定义强调色", selection: accentBinding, supportsOpacity: false)
                        .font(Face.sans(11))
                        .foregroundStyle(Ink.ink)
                } else {
                    Text("自定义强调色")
                        .font(Face.sans(11))
                        .foregroundStyle(Ink.ink)
                }
                Spacer()
                if settings.accentHex != nil {
                    Button("恢复主题颜色") { settings.resetAccent() }
                        .font(Face.sans(10.5, .medium))
                        .foregroundStyle(Ink.accent)
                        .buttonStyle(.plain)
                }
            }
            .padding(12)
            .background(Ink.surface)
            .overlay { RoundedRectangle(cornerRadius: 7).strokeBorder(Ink.line, lineWidth: 1) }

            HStack(spacing: 10) {
                Text("快速选择")
                    .font(Face.sans(10))
                    .foregroundStyle(Ink.inkFaint)
                ForEach(Self.accentOptions, id: \.0) { option in
                    Button {
                        settings.setAccent(Color(hex: option.1))
                    } label: {
                        Circle()
                            .fill(Color(hex: option.1))
                            .frame(width: 20, height: 20)
                            .overlay {
                                Circle().strokeBorder(Ink.canvas, lineWidth: 2)
                            }
                            .overlay {
                                Circle().strokeBorder(Ink.lineStrong, lineWidth: 1)
                            }
                    }
                    .buttonStyle(.plain)
                    .help(option.0)
                }
                Spacer()
            }

            }.disabled(settings.themeFilePresent)
            previewCard
        }
    }

    private var typographyContent: some View {
        VStack(alignment: .leading, spacing: 20) {
            if settings.themeFilePresent {
                Text("字体与缩放由 theme.json 管理，请在外观页打开配置文件。")
                    .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
            }
            VStack(alignment: .leading, spacing: 12) {
                settingHeading("字体家族", detail: "从已安装字体中选择，改动会即时应用")
                FontPreviewPicker(title: "界面字体", selection: $settings.interfaceFontName,
                                  families: settings.availableFontFamilies,
                                  sampleFont: Face.sans(14), colorScheme: settingsColorScheme)
                FontPreviewPicker(title: "数据字体", selection: $settings.dataFontName,
                                  families: settings.availableFontFamilies,
                                  sampleFont: Face.mono(13), colorScheme: settingsColorScheme)
            }

            VStack(alignment: .leading, spacing: 12) {
                settingHeading("模块字号", detail: "分别调整界面、数据表格和日志的阅读密度")
                scaleRow(title: "界面文字", value: $settings.interfaceScale)
                scaleRow(title: "数据文字", value: $settings.dataScale)
                scaleRow(title: "日志文字", value: $settings.logScale)
            }

            VStack(alignment: .leading, spacing: 7) {
                Text("预览")
                    .font(Face.sans(10, .medium))
                    .foregroundStyle(Ink.inkFaint)
                    .tracking(0.6)
                HStack(spacing: 16) {
                    Text("服务  api")
                        .font(Face.sans(12, .medium))
                    Text("18080  127.0.0.1")
                        .font(Face.mono(11))
                    Text("INFO  listening")
                        .font(Face.log(10))
                        .foregroundStyle(Ink.inkMuted)
                }
                .foregroundStyle(Ink.ink)
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Ink.surface)
                .overlay { RoundedRectangle(cornerRadius: 7).strokeBorder(Ink.line, lineWidth: 1) }
            }
        }.disabled(settings.themeFilePresent)
    }

    private var workspaceContent: some View {
        VStack(alignment: .leading, spacing: 16) {
            settingHeading("本地连接", detail: "这些值来自当前客户端环境")
            settingRow("后台 socket", DaemonClient.defaultSocketPath())
            settingRow("清单文件", "oberth.yaml")
            Text("新建项目时可以让本机 agent 起草，命令行也可用 oberth init draft。草稿必须由人审阅后才会成为清单。")
                .font(Face.sans(11))
                .foregroundStyle(Ink.inkFaint)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, 4)

            Hairline().padding(.vertical, 6)
            HStack {
                VStack(alignment: .leading, spacing: 3) {
                    Text("恢复默认外观")
                        .font(Face.sans(11.5, .medium))
                        .foregroundStyle(Ink.ink)
                    Text("清除旧界面偏好；不会删除或覆盖配置文件")
                        .font(Face.sans(10))
                        .foregroundStyle(Ink.inkFaint)
                }
                Spacer()
                Button("恢复默认") { settings.reset() }
                    .font(Face.sans(10.5, .medium))
                    .foregroundStyle(Ink.accent)
                    .buttonStyle(.plain)
            }
        }
    }

    private var jevContent: some View {
        VStack(alignment: .leading, spacing: 18) {
            settingHeading("Jev attention", detail: "异常发生后，由 Jev 判断是否需要把选择交还给你")

            HStack(spacing: 14) {
                VStack(alignment: .leading, spacing: 4) {
                    Text("启用 Jev 增强")
                        .font(Face.sans(12, .medium))
                        .foregroundStyle(Ink.ink)
                    Text(jev.statusLabel)
                        .font(Face.sans(10))
                        .foregroundStyle(jev.enabled && jev.hasKey ? Ink.live : Ink.inkFaint)
                }
                Spacer(minLength: 12)
                Button(action: toggleJev) {
                    HStack(spacing: 8) {
                        Circle()
                            .fill(jev.enabled ? Ink.live : Ink.dormant)
                            .frame(width: 8, height: 8)
                        Text(jev.enabled ? "已开启" : "开启")
                            .font(Face.sans(11, .medium))
                    }
                    .foregroundStyle(jev.enabled ? Ink.ink : Ink.inkMuted)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(jev.enabled ? Ink.accentSoft : Ink.surface)
                    .overlay {
                        RoundedRectangle(cornerRadius: 6)
                            .strokeBorder(jev.enabled ? Ink.accent : Ink.lineStrong, lineWidth: 1)
                    }
                }
                .buttonStyle(.plain)
                .accessibilityLabel("启用 Jev 增强")
                .accessibilityValue(jev.enabled ? "已开启" : "已关闭")
            }
            .padding(13)
            .background(Ink.surface)
            .overlay { RoundedRectangle(cornerRadius: 7).strokeBorder(Ink.line, lineWidth: 1) }

            VStack(alignment: .leading, spacing: 10) {
                settingHeading("提供方", detail: "当前设置页使用 OpenRouter 的 Jev Decisions API")
                HStack(spacing: 12) {
                    Text("OpenRouter")
                        .font(Face.sans(11, .medium))
                        .foregroundStyle(Ink.ink)
                    Text("typesafe/jev-1.13")
                        .font(Face.mono(10.5))
                        .foregroundStyle(Ink.inkMuted)
                    Spacer()
                    Text("HTTPS")
                        .font(Face.mono(9.5, .medium))
                        .tracking(0.6)
                        .foregroundStyle(Ink.inkFaint)
                }
                .padding(11)
                .background(Ink.canvas)
                .overlay { RoundedRectangle(cornerRadius: 6).strokeBorder(Ink.line, lineWidth: 1) }
            }

            VStack(alignment: .leading, spacing: 8) {
                settingHeading("OpenRouter API key", detail: "只保存在本机的 ~/.option-berth/jev.json，daemon 和质量日志不会读取它")
                if scrolls {
                    SecureField("sk-or-…", text: $jev.apiKey)
                        .textFieldStyle(.plain)
                        .font(Face.mono(11))
                        .foregroundStyle(Ink.ink)
                        .padding(.horizontal, 11)
                        .padding(.vertical, 9)
                        .background(Ink.surface)
                        .overlay { RoundedRectangle(cornerRadius: 6).strokeBorder(Ink.lineStrong, lineWidth: 1) }
                        .accessibilityLabel("OpenRouter API key")
                } else {
                    // ImageRenderer cannot draw a real SecureField. Keep the
                    // snapshot deterministic and ensure no user's key appears.
                    Text(jev.hasKey ? "••••••••••••" : "尚未配置")
                        .font(Face.mono(11))
                        .foregroundStyle(jev.hasKey ? Ink.inkMuted : Ink.inkFaint)
                        .padding(.horizontal, 11)
                        .padding(.vertical, 9)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Ink.surface)
                        .overlay { RoundedRectangle(cornerRadius: 6).strokeBorder(Ink.lineStrong, lineWidth: 1) }
                }
                HStack(spacing: 12) {
                    Text(jev.maskedKey)
                        .font(Face.mono(10))
                        .foregroundStyle(Ink.inkFaint)
                    Spacer()
                    if jev.hasKey {
                        Button("清除 key") { clearJevKey() }
                            .font(Face.sans(10.5, .medium))
                            .foregroundStyle(Ink.accent)
                            .buttonStyle(.plain)
                    }
                    Button("保存配置") { saveJev() }
                        .font(Face.sans(10.5, .medium))
                        .foregroundStyle(Ink.accent)
                        .buttonStyle(.plain)
                }
            }

            HStack(alignment: .top, spacing: 10) {
                StatusDot(tone: jev.enabled && jev.hasKey ? Ink.live : Ink.dormant)
                    .padding(.top, 2)
                VStack(alignment: .leading, spacing: 4) {
                    Text(jev.enabled && jev.hasKey ? "任务级 runner 已具备调用条件" : "配置完成后，任务级 runner 才会调用 Jev")
                        .font(Face.sans(11, .medium))
                        .foregroundStyle(Ink.ink)
                    Text(jevNotice ?? "agent 会在持续服务任务开始时自动启动 jev-attention --session；环境变量仍可覆盖它。")
                        .font(Face.sans(10))
                        .foregroundStyle(Ink.inkFaint)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 0)
            }
            .padding(12)
            .background(Ink.surface)
            .overlay { RoundedRectangle(cornerRadius: 7).strokeBorder(Ink.line, lineWidth: 1) }

            settingRow("本地配置文件", jevPathLabel)
        }
    }

    private func toggleJev() {
        jev.enabled.toggle()
        saveJev()
    }

    private func clearJevKey() {
        jev.apiKey = ""
        jev.enabled = false
        saveJev()
    }

    private func saveJev() {
        do {
            try JevSettingsStore.save(jev)
            jevNotice = "已保存；下一次任务级 attention session 启动时立即生效。"
        } catch {
            jevNotice = "保存失败：\(error.localizedDescription)"
        }
    }

    private var jevPathLabel: String {
        let environment = ProcessInfo.processInfo.environment
        if let override = environment["BERTH_HOME"], !override.isEmpty {
            return "\(override)/jev.json"
        }
        return "~/.option-berth/jev.json"
    }

    private func settingHeading(_ title: String, detail: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(Face.display(15, .medium))
                .foregroundStyle(Ink.ink)
            Text(detail)
                .font(Face.sans(10))
                .foregroundStyle(Ink.inkFaint)
        }
    }

    private func scaleRow(title: String, value: Binding<Double>) -> some View {
        HStack(spacing: 12) {
            Text(title)
                .font(Face.sans(11))
                .foregroundStyle(Ink.inkMuted)
                .frame(width: 76, alignment: .leading)
            Slider(value: value, in: 0.8...1.4, step: 0.05)
                .tint(Ink.accent)
            Menu {
                ForEach(Self.scaleOptions, id: \.self) { option in
                    Button {
                        value.wrappedValue = option
                    } label: {
                        Text(Self.scaleTitle(option))
                    }
                }
            } label: {
                HStack(spacing: 4) {
                    Text(Self.scaleTitle(value.wrappedValue))
                    Text("⌄")
                        .font(Face.sans(11, .medium))
                        .foregroundStyle(Ink.inkFaint)
                }
                .font(Face.mono(10, .medium))
                .foregroundStyle(Ink.ink)
                .frame(width: 58, height: 26, alignment: .trailing)
                .contentShape(Rectangle())
            }
            .menuStyle(.borderlessButton)
            .tint(Ink.ink)
            .accessibilityLabel(title)
        }
    }

    private static let scaleOptions: [Double] = stride(from: 80, through: 140, by: 5).map { Double($0) / 100 }

    private static func scaleTitle(_ value: Double) -> String {
        "\(Int((value * 100).rounded()))%"
    }

    private var previewCard: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text("实时预览")
                .font(Face.sans(10, .medium))
                .foregroundStyle(Ink.inkFaint)
                .tracking(0.6)
            HStack(spacing: 10) {
                StatusDot(tone: Ink.live)
                Text("api")
                    .font(Face.mono(12, .medium))
                    .foregroundStyle(Ink.ink)
                Text("正在监听")
                    .font(Face.sans(11))
                    .foregroundStyle(Ink.inkMuted)
                Spacer()
                Text("18080")
                    .font(Face.mono(11, .medium))
                    .foregroundStyle(Ink.accent)
            }
            .padding(12)
            .background(Ink.surface)
            .overlay { RoundedRectangle(cornerRadius: 7).strokeBorder(Ink.line, lineWidth: 1) }
        }
    }

    private func settingRow(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(label.uppercased())
                .font(Face.mono(9.5, .medium))
                .tracking(0.7)
                .foregroundStyle(Ink.inkFaint)
            Text(value)
                .font(Face.mono(11))
                .foregroundStyle(Ink.ink)
                .textSelection(.enabled)
                .lineLimit(2)
                .truncationMode(.middle)
        }
    }

    private var accentBinding: Binding<Color> {
        Binding(get: { settings.accentColor }, set: { settings.setAccent($0) })
    }

    private var settingsColorScheme: ColorScheme {
        settings.colorScheme
    }

    private static let accentOptions: [(String, UInt32)] = [
        ("蓝", 0x1E4BD8), ("青", 0x168A9A), ("紫", 0x7158D8),
        ("珊瑚", 0xC45B4D), ("琥珀", 0xB57922), ("苔绿", 0x4D8A58)
    ]
}

/// Native menus flatten multiline labels. Keep the sample in the view hierarchy,
/// and use a popover so every candidate's sample is rendered before selection.
private struct FontPreviewPicker: View {
    let title: String
    @Binding var selection: String
    let families: [String]
    let sampleFont: Font
    let colorScheme: ColorScheme
    @State private var isPresented = false
    @State private var search = ""

    private static let sample = "Aa 你好 123 · option-berth"

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 12) {
                Text(title)
                    .font(Face.sans(11))
                    .foregroundStyle(Ink.inkMuted)
                Spacer(minLength: 8)
                Button {
                    search = ""
                    isPresented = true
                } label: {
                    HStack(spacing: 8) {
                        Text(fontName(selection))
                            .lineLimit(1)
                            .truncationMode(.tail)
                        Text("⌄")
                    }
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(Ink.ink)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .background(Ink.canvas)
                    .overlay {
                        RoundedRectangle(cornerRadius: 5)
                            .strokeBorder(Ink.lineStrong, lineWidth: 1)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(title)
                .accessibilityValue(fontName(selection))
                .popover(isPresented: $isPresented, arrowEdge: .bottom) {
                    fontList
                        .preferredColorScheme(colorScheme)
                }
            }

            Text(Self.sample)
                .font(sampleFont)
                .foregroundStyle(Ink.ink)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Ink.surface)
                .overlay {
                    RoundedRectangle(cornerRadius: 5)
                        .strokeBorder(Ink.line, lineWidth: 1)
                }
                .accessibilityLabel("\(title)预览：\(Self.sample)")
        }
    }

    private var fontList: some View {
        VStack(alignment: .leading, spacing: 0) {
            TextField("搜索字体", text: $search)
                .textFieldStyle(.plain)
                .font(.system(size: 12))
                .foregroundStyle(Ink.ink)
                .padding(12)
                .accessibilityLabel("搜索字体")
            Hairline()
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(filteredFamilies, id: \.self) { family in
                            Button {
                                selection = family
                                isPresented = false
                            } label: {
                                HStack(spacing: 8) {
                                    VStack(alignment: .leading, spacing: 4) {
                                        Text(fontName(family))
                                            .font(.system(size: 11, weight: .medium))
                                        Text(Self.sample)
                                            .font(previewFont(family))
                                            .fixedSize(horizontal: false, vertical: true)
                                    }
                                    Spacer(minLength: 0)
                                    if selection == family {
                                        Text("✓")
                                            .font(.system(size: 12, weight: .semibold))
                                    }
                                }
                                .foregroundStyle(Ink.ink)
                                .padding(12)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(selection == family ? Ink.accentSoft : Ink.canvas)
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel(fontName(family))
                            .id(family)
                        }
                        if filteredFamilies.isEmpty {
                            Text("没有匹配的字体")
                                .font(.system(size: 12))
                                .foregroundStyle(Ink.inkMuted)
                                .padding(12)
                        }
                    }
                }
                .onAppear { proxy.scrollTo(selection, anchor: .center) }
            }
        }
        .frame(width: 320, height: 360)
        .background(Ink.canvas)
    }

    private var filteredFamilies: [String] {
        let query = search.trimmingCharacters(in: .whitespacesAndNewlines)
        return query.isEmpty ? families : families.filter { fontName($0).localizedCaseInsensitiveContains(query) }
    }

    private func fontName(_ family: String) -> String {
        family == "__system__" ? "系统字体" : family
    }

    private func previewFont(_ family: String) -> Font {
        if family == "__system__" { return .system(size: 14) }
        if family == "Monaspace Neon", FontBook.monoAvailable {
            return .custom(FontBook.monoRegular, size: 14)
        }
        return .custom(family, size: 14)
    }
}
