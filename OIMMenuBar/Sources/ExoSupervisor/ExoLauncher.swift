import AppKit
import Foundation

/// Guides the user to install/launch Exo — deliberately never vendors,
/// bundles, or silently auto-installs it. Exo has no PyPI package, ships as a
/// signed DMG or a Homebrew cask, and is a fast-moving project this app
/// doesn't control; embedding a copy would be a real ongoing maintenance
/// commitment this app is explicitly not taking on.
enum ExoLauncher {
    // NOT assets.exolabs.net — confirmed live that it 404s at the root (it's
    // an asset-hosting bucket for the DMG file itself, not a browsable page).
    // The GitHub repo is guaranteed to have a real index with install
    // instructions and links to the current release.
    static let downloadPageURL = URL(string: "https://github.com/exo-explore/exo")!

    /// Primary CTA when Exo isn't detected: open the official download page.
    /// Zero trust/permission surface — this is the safest possible action.
    static func openDownloadPage() {
        NSWorkspace.shared.open(downloadPageURL)
    }

    /// Secondary CTA for Homebrew users. Streams output to `onOutput` so the
    /// install isn't a silent background shell command — the user sees
    /// exactly what's happening, same transparency principle as the node's
    /// own log viewer. Reports a clear message (not silence) if `brew` itself
    /// can't be found or the process fails to launch — an earlier version
    /// swallowed both failure modes with `try?`, which is exactly the "no
    /// status or progress, then it failed" bug this fixes.
    @discardableResult
    static func installViaHomebrew(onOutput: @escaping (String) -> Void, onFinish: @escaping (Int32) -> Void) -> Process? {
        guard let brew = ExoDetector.brewExecutableURL() else {
            onOutput("Homebrew not found at /opt/homebrew/bin/brew or /usr/local/bin/brew.\n")
            onFinish(-1)
            return nil
        }

        let process = Process()
        process.executableURL = brew
        process.arguments = ["install", "--cask", "exo"]

        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        pipe.fileHandleForReading.readabilityHandler = { handle in
            let data = handle.availableData
            guard !data.isEmpty, let text = String(data: data, encoding: .utf8) else { return }
            Task { @MainActor in onOutput(text) }
        }
        process.terminationHandler = { proc in
            pipe.fileHandleForReading.readabilityHandler = nil
            Task { @MainActor in onFinish(proc.terminationStatus) }
        }
        do {
            try process.run()
        } catch {
            pipe.fileHandleForReading.readabilityHandler = nil
            onOutput("Failed to launch brew: \(error.localizedDescription)\n")
            onFinish(-1)
            return nil
        }
        return process
    }

    /// Launches the installed Exo.app the normal macOS way. The caller's
    /// existing ExoHealthMonitor poll loop is the confirmation mechanism —
    /// no separate "did it actually start" check is needed here.
    static func launchApp() {
        guard let path = ExoDetector.installedAppPath() else { return }
        NSWorkspace.shared.openApplication(at: URL(fileURLWithPath: path), configuration: .init(), completionHandler: nil)
    }
}
