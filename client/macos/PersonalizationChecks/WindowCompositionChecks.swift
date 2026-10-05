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
        // colorAt() may return NSCalibratedRGBColor even for a Display-P3
        // bitmap. Converting that NSColor applies the wrong transfer function.
        // Let CoreGraphics convert the capture profile into explicit sRGB bytes.
        let width = image.width, height = image.height
        guard width > 0, height > 0, width <= 4096, height <= 4096 else {
            throw Failure(message: "unexpected native composition dimensions")
        }
        let pixels = UnsafeMutablePointer<UInt8>.allocate(capacity: width * height * 4)
        defer { pixels.deallocate() }
        guard let context = CGContext(data: pixels, width: width, height: height,
            bitsPerComponent: 8, bytesPerRow: width * 4,
            space: CGColorSpace(name: CGColorSpace.sRGB)!,
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue | CGBitmapInfo.byteOrder32Big.rawValue) else {
            throw Failure(message: "sRGB composition sample context unavailable")
        }
        context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
        // Samples are well away from the title bar, prompt and terminal cursor.
        // Validate composited pixels, not just the view's nativeBackgroundColor.
        var matching = 0
        for x in [0.4, 0.5, 0.6] {
            for y in [0.4, 0.5, 0.6] {
                let index = (Int(Double(height) * y) * width + Int(Double(width) * x)) * 4
                if abs(Int(pixels[index]) - 28) < 5,
                   abs(Int(pixels[index + 1]) - 34) < 5,
                   abs(Int(pixels[index + 2]) - 43) < 5,
                   pixels[index + 3] == 255 { matching += 1 }
            }
        }
        if let png = bitmap.representation(using: .png, properties: [:]) { try png.write(to: output) }
        try expect(matching == 9, "terminal window background does not match configured #1C222B (\(matching)/9 samples)")
    }
}
