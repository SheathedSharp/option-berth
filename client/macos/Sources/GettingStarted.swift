import AppKit
import SwiftUI

struct TourMeasurement: Equatable {
    let target: TourTarget
    let highlight: CGRect
    let card: CGRect
    let viewport: CGSize
}
struct TourMeasurementKey: PreferenceKey {
    static var defaultValue: TourMeasurement? { nil }
    static func reduce(value: inout TourMeasurement?, nextValue: () -> TourMeasurement?) { value = nextValue() ?? value }
}

struct TourAnchors: PreferenceKey {
    static var defaultValue: [TourTarget: Anchor<CGRect>] { [:] }
    static func reduce(value: inout [TourTarget: Anchor<CGRect>], nextValue: () -> [TourTarget: Anchor<CGRect>]) {
        value.merge(nextValue()) { first, _ in first }
    }
}
extension View {
    func tourAnchor(_ target: TourTarget) -> some View {
        anchorPreference(key: TourAnchors.self, value: .bounds) { [target: $0] }
    }
}

/// An in-place layer over the real workspace. No sheets, screenshots or synthetic
/// controls stand in for the highlighted view. Missing targets are omitted.
struct WorkspaceTour: View {
    @ObservedObject var navigation: ViewState
    let frames: [TourTarget: CGRect]
    let size: CGSize
    var frozen = false
    @Environment(\.accessibilityReduceMotion) private var reduce

    private var available: [TourTarget] {
        TourTarget.allCases.filter { frames[$0].flatMap { TourLayout.visible($0, in: size) } != nil }
    }
    var body: some View {
        if let target = available.contains(navigation.guideTarget) ? navigation.guideTarget : available.first,
           let source = frames[target], let visible = TourLayout.visible(source, in: size) {
            let hole = visible.insetBy(dx: -5, dy: -5).intersection(CGRect(origin: .zero, size: size))
            let card = TourLayout.card(near: hole, in: size)
            let index = available.firstIndex(of: target) ?? 0
            ZStack(alignment: .topLeading) {
                TourMask(hole: hole)
                .fill(.black.opacity(0.58), style: FillStyle(eoFill: true))
                .contentShape(Rectangle()).onTapGesture { }
                .accessibilityHidden(true)
                .animation(reduce ? nil : .easeInOut(duration: 0.16), value: hole)
                RoundedRectangle(cornerRadius: 8).stroke(Ink.accent, lineWidth: 2)
                    .frame(width: hole.width, height: hole.height).position(x: hole.midX, y: hole.midY)
                    .allowsHitTesting(false).accessibilityHidden(true)
                    .animation(reduce ? nil : .easeInOut(duration: 0.16), value: hole)
                VStack(alignment: .leading, spacing: 12) {
                    HStack {
                        Text("WORKSPACE TOUR").font(Face.mono(9)).tracking(1.3).foregroundStyle(Ink.inkMuted)
                        Spacer()
                        Text("\(index + 1) / \(available.count)").font(Face.mono(11)).foregroundStyle(Ink.accent)
                    }
                    Text(target.title).font(Face.sans(17, .semibold)).fixedSize(horizontal: false, vertical: true)
                        .accessibilityAddTraits(.isHeader)
                    Text(target.chinese).font(Face.sans(12.5)).fixedSize(horizontal: false, vertical: true)
                    Text(target.english).font(Face.sans(11.5)).foregroundStyle(Ink.inkMuted)
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: 0)
                    if frozen {
                        HStack {
                            Text("跳过 / Skip"); Spacer(); Text("返回 / Back"); Text(index + 1 == available.count ? "完成 / Done" : "继续 / Next")
                        }.font(Face.sans(11))
                    } else {
                        TourControls(first: index == 0, last: index + 1 == available.count,
                                     skip: { navigation.showingGuide = false },
                                     back: { move(to: max(0, index - 1)) },
                                     next: {
                            if index + 1 == available.count { navigation.showingGuide = false }
                            else { move(to: index + 1) }
                        }).frame(height: 28)
                    }
                }
                .transaction { $0.animation = nil }
                .padding(20).frame(width: card.width, height: card.height, alignment: .topLeading)
                .foregroundStyle(Ink.ink).background(Ink.surface, in: RoundedRectangle(cornerRadius: 12))
                .overlay(RoundedRectangle(cornerRadius: 12).stroke(Ink.line, lineWidth: 1))
                .shadow(color: .black.opacity(0.25), radius: 16, y: 6)
                .position(x: card.midX, y: card.midY)
                .animation(reduce ? nil : .easeInOut(duration: 0.16), value: card)
                .accessibilityElement(children: .contain).accessibilityLabel("工作区使用指引 / Workspace tour")
            }
            .frame(width: size.width, height: size.height)
            .preference(key: TourMeasurementKey.self, value: TourMeasurement(target: target, highlight: hole, card: card, viewport: size))
        } else {
            // Never show an unanchored popover. A window without any targets can
            // still dismiss the layer; it does not gain permission to run actions.
            Color.clear.onAppear { navigation.showingGuide = false }
        }
    }
    private func move(to index: Int) {
        guard available.indices.contains(index) else { return }
        navigation.guideTarget = available[index]
    }
}

private struct TourMask: Shape {
    var hole: CGRect
    var animatableData: AnimatablePair<AnimatablePair<CGFloat, CGFloat>, AnimatablePair<CGFloat, CGFloat>> {
        get { AnimatablePair(AnimatablePair(hole.minX, hole.minY), AnimatablePair(hole.width, hole.height)) }
        set { hole = CGRect(x: newValue.first.first, y: newValue.first.second,
                            width: newValue.second.first, height: newValue.second.second) }
    }
    func path(in rect: CGRect) -> Path {
        Path { path in
            path.addRect(rect)
            path.addRoundedRect(in: hole, cornerSize: CGSize(width: 8, height: 8))
        }
    }
}

/// Native controls provide keyboard equivalents and a closed focus loop while
/// the underlying workspace is disabled. They are also exercised as NSButtons.
private struct TourControls: NSViewRepresentable {
    let first: Bool
    let last: Bool
    let skip: () -> Void
    let back: () -> Void
    let next: () -> Void
    func makeNSView(context: Context) -> TourControlsView { TourControlsView() }
    func updateNSView(_ view: TourControlsView, context: Context) {
        view.skipAction = skip; view.backAction = back; view.nextAction = next
        view.backButton.isEnabled = !first
        view.nextButton.title = last ? "完成 / Done" : "继续 / Next"
        view.skipButton.nextKeyView = first ? view.nextButton : view.backButton
        view.backButton.nextKeyView = view.nextButton
        view.nextButton.nextKeyView = view.skipButton
    }
    static func dismantleNSView(_ view: TourControlsView, coordinator: ()) { view.restoreFocus() }
}

final class TourControlsView: NSStackView {
    let skipButton = NSButton(title: "跳过 / Skip", target: nil, action: nil)
    let backButton = NSButton(title: "返回 / Back", target: nil, action: nil)
    let nextButton = NSButton(title: "继续 / Next", target: nil, action: nil)
    var skipAction: () -> Void = {}
    var backAction: () -> Void = {}
    var nextAction: () -> Void = {}
    private weak var previous: NSResponder?
    private weak var owningWindow: NSWindow?
    init() {
        super.init(frame: .zero)
        orientation = .horizontal; spacing = 8; distribution = .fillEqually
        setAccessibilityIdentifier("guide.controls")
        for (button, id, selector) in [(skipButton, "guide.skip", #selector(skip)), (backButton, "guide.back", #selector(back)), (nextButton, "guide.next", #selector(next))] {
            button.bezelStyle = .rounded; button.font = .systemFont(ofSize: 11)
            button.target = self; button.action = selector
            button.setAccessibilityIdentifier(id)
            addArrangedSubview(button)
        }
        skipButton.keyEquivalent = "\u{1b}"; nextButton.keyEquivalent = "\r"
        skipButton.keyEquivalentModifierMask = []; nextButton.keyEquivalentModifierMask = []
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) is unavailable") }
    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        guard let window, owningWindow == nil else { return }
        owningWindow = window; previous = window.firstResponder
        window.makeFirstResponder(nextButton)
    }
    func restoreFocus() {
        guard let window = owningWindow else { return }
        if [skipButton, backButton, nextButton].contains(where: { window.firstResponder === $0 }) {
            if let view = previous as? NSView, view.window === window { window.makeFirstResponder(view) }
            else { window.makeFirstResponder(nil) }
        }
        previous = nil; owningWindow = nil
    }
    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        guard event.modifierFlags.intersection(.deviceIndependentFlagsMask).isEmpty else { return super.performKeyEquivalent(with: event) }
        switch event.charactersIgnoringModifiers {
        case "\u{f702}": if backButton.isEnabled { back() }; return true
        case "\u{f703}": next(); return true
        default: return super.performKeyEquivalent(with: event)
        }
    }
    @objc private func skip() { skipAction() }
    @objc private func back() { if backButton.isEnabled { backAction() } }
    @objc private func next() { nextAction() }
}
