import UIKit

/// Observes memory-warning notifications. The correct response to memory
/// pressure is to pause job acceptance and release caches — never to ignore it
/// (the OS will kill the process) and never to kill in-flight jobs mid-run.
final class MemoryPressureMonitor {

    var onMemoryWarning: (() -> Void)?
    private var observer: NSObjectProtocol?

    func startMonitoring() {
        observer = NotificationCenter.default.addObserver(
            forName: UIApplication.didReceiveMemoryWarningNotification,
            object: nil, queue: .main
        ) { [weak self] _ in
            self?.onMemoryWarning?()
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
