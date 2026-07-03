import Foundation

/// Observes the system thermal state and reports changes. Thermal thresholds are
/// safety-critical and deliberately NOT user-configurable.
final class ThermalMonitor {

    var onStateChange: ((ProcessInfo.ThermalState) -> Void)?
    private var observer: NSObjectProtocol?

    func startMonitoring() {
        // Emit the current state immediately so the session starts correctly even
        // if the device is already warm.
        onStateChange?(ProcessInfo.processInfo.thermalState)
        observer = NotificationCenter.default.addObserver(
            forName: ProcessInfo.thermalStateDidChangeNotification,
            object: nil, queue: .main
        ) { [weak self] _ in
            self?.onStateChange?(ProcessInfo.processInfo.thermalState)
        }
    }

    func stopMonitoring() {
        if let observer {
            NotificationCenter.default.removeObserver(observer)
            self.observer = nil
        }
    }

    deinit { stopMonitoring() }
}
