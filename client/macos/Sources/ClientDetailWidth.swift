import SwiftUI

/// Every detail view observes the same configuration publisher, but owns a
/// separate preference. The binding never writes into a user's JSON file.
@propertyWrapper struct ClientDetailWidth: DynamicProperty {
    @ObservedObject private var settings: UISettings
    private let pane: ClientDetailPane
    init(_ pane: ClientDetailPane, settings: UISettings = .shared) {
        self.pane = pane
        _settings = ObservedObject(wrappedValue: settings)
    }
    var wrappedValue: Double {
        get { settings.detailWidth(for: pane) }
        nonmutating set { settings.resizeDetail(newValue, for: pane) }
    }
    var projectedValue: Binding<Double> {
        Binding(get: { wrappedValue }, set: { wrappedValue = $0 })
    }
}
