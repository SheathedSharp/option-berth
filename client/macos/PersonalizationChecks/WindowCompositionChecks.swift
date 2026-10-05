import AppKit
import ScreenCaptureKit
@testable import BerthClient

extension PersonalizationChecks {
    /// Capture this test's own window only, not the desktop or another app.
    static func checkTerminalComposition(_ window: NSWindow, output: URL) async throws {
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
        let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: false)
        guard let owned = content.windows.first(where: {
            $0.windowID == CGWindowID(window.windowNumber)
                && $0.owningApplication?.processID == ProcessInfo.processInfo.processIdentifier
        }) else { throw Failure(message: "test-owned terminal window not available for composition capture") }
        let filter = SCContentFilter(desktopIndependentWindow: owned)
        let configuration = SCStreamConfiguration()
        configuration.width = Int(window.frame.width * window.backingScaleFactor)
        configuration.height = Int(window.frame.height * window.backingScaleFactor)
        configuration.showsCursor = false
        let image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: configuration)
        let bitmap = NSBitmapImageRep(cgImage: image)
        // Samples are well away from the title bar, prompt and terminal cursor.
        // Validate composited pixels, not just the view's nativeBackgroundColor.
        var matching = 0
        for x in [0.4, 0.5, 0.6] {
            for y in [0.4, 0.5, 0.6] {
                guard let color = bitmap.colorAt(x: Int(Double(bitmap.pixelsWide) * x),
                                                y: Int(Double(bitmap.pixelsHigh) * y))?.usingColorSpace(.sRGB) else { continue }
                if abs(color.redComponent * 255 - 28) < 5,
                   abs(color.greenComponent * 255 - 34) < 5,
                   abs(color.blueComponent * 255 - 43) < 5,
                   color.alphaComponent > 0.99 { matching += 1 }
            }
        }
        if let png = bitmap.representation(using: .png, properties: [:]) { try png.write(to: output) }
        try expect(matching == 9, "terminal window background does not match configured #1C222B (\(matching)/9 samples)")
    }
}
