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
    ///
    /// After a successful cask install, this also strips the quarantine
    /// attribute Gatekeeper attaches to the downloaded app — otherwise the
    /// very next "Launch Exo" click can show "EXO.app is damaged and can't
    /// be opened," Gatekeeper's generic message for an unnotarized/quarantined
    /// app, NOT actual corruption. A real user can't be expected to open
    /// Terminal to fix that themselves; see launchApp's doc comment for the
    /// matching self-heal on the launch side (for apps installed before this
    /// existed, or installed by dragging the DMG manually).
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
            if proc.terminationStatus == 0, let path = ExoDetector.installedAppPath() {
                Task { @MainActor in onOutput("Clearing macOS's quarantine flag so Exo can launch without a Gatekeeper warning...\n") }
                _ = stripQuarantine(at: path)
            }
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

    /// Recursively clears extended attributes (including com.apple.quarantine)
    /// at path. Never needs elevation — an app placed under /Applications by
    /// a non-root install (Homebrew cask, or a user dragging a DMG) is owned
    /// by the current user, who already has write permission on it.
    @discardableResult
    private static func stripQuarantine(at path: String) -> Bool {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/xattr")
        process.arguments = ["-cr", path]
        process.standardOutput = Pipe()
        process.standardError = Pipe()
        do {
            try process.run()
            process.waitUntilExit()
            return process.terminationStatus == 0
        } catch {
            return false
        }
    }

    /// Launches the installed Exo.app. Gatekeeper's "damaged and can't be
    /// opened" block is enforced by a separate system process (syspolicyd),
    /// and its own alert can appear independent of whatever
    /// NSWorkspace.openApplication's completion handler reports back to
    /// us — there is no reliable guarantee a Gatekeeper rejection surfaces
    /// as an `error` there at all. So the quarantine strip runs
    /// unconditionally, BEFORE every launch attempt (installViaHomebrew
    /// also runs it right after a fresh cask install, so this is normally a
    /// no-op double-check — but covers an app that was already sitting
    /// there from an older manual DMG install), rather than reactively
    /// after a signal that may never arrive.
    ///
    /// Deliberately does NOT re-sign the app — Exo's official release may
    /// already be validly signed/notarized by its developers, and
    /// overwriting that with a local ad-hoc signature would destroy a
    /// working signature instead of fixing a broken one. If quarantine
    /// removal isn't enough, that's a real, different problem worth
    /// diagnosing properly (`spctl -a -vvv --type execute`), not papering
    /// over with a blind re-sign.
    static func launchApp(onOutput: @escaping (String) -> Void = { _ in }, onFailure: @escaping (String) -> Void = { _ in }) {
        guard let path = ExoDetector.installedAppPath() else { return }
        _ = stripQuarantine(at: path)
        let url = URL(fileURLWithPath: path)
        NSWorkspace.shared.openApplication(at: url, configuration: .init()) { _, error in
            if let error {
                Task { @MainActor in
                    onFailure("Exo still won't launch: \(error.localizedDescription). Try reinstalling via \"Install Exo.\"")
                }
            }
        }
    }

    /// Official Homebrew install one-liner (brew.sh) — mirrored exactly so
    /// the running script is the one users already trust, not a fork.
    private static let homebrewInstallScriptURL =
        "https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh"

    /// The seamless "no Homebrew yet" path: gets Homebrew installed with a
    /// single native admin-password prompt, then installs Exo via the cask.
    /// Falls straight through to installViaHomebrew if brew is already present.
    ///
    /// Why this ISN'T just "run the whole installer with administrator
    /// privileges": Homebrew's own install.sh explicitly refuses to run as
    /// root/UID 0 ("Don't run this as root!") — confirmed against the live
    /// script. Wrapping the entire script in `osascript ... with
    /// administrator privileges` would execute it AS root and trip that
    /// check every time.
    ///
    /// What actually works: install.sh calls `sudo` itself, internally, for
    /// the handful of steps that need root (creating /opt/homebrew, chown).
    /// `NONINTERACTIVE=1` makes those internal sudo calls non-prompting
    /// (`sudo -n`) — which only succeeds if a sudo credential is already
    /// cached for the current user. So the flow is:
    ///   1. One `osascript ... with administrator privileges` prompt runs
    ///      `sudo -u <current user> -v` AS root (via Authorization
    ///      Services) — this caches sudo credentials for the CURRENT user
    ///      (not for root), since `sudo -u X -v` run by root refreshes X's
    ///      own timestamp without needing X's password.
    ///   2. The installer then runs as the current (non-root) user with
    ///      NONINTERACTIVE=1 — its internal `sudo -n` calls succeed against
    ///      that freshly-cached credential instead of prompting (which would
    ///      otherwise just fail outright under NONINTERACTIVE).
    /// Both steps run without a controlling terminal, which is what makes
    /// sudo treat them as the same credential-cache session.
    @discardableResult
    static func installHomebrewThenExo(onOutput: @escaping (String) -> Void, onFinish: @escaping (Int32) -> Void) -> Process? {
        if ExoDetector.brewExecutableURL() != nil {
            return installViaHomebrew(onOutput: onOutput, onFinish: onFinish)
        }
        onOutput("Homebrew not found — requesting administrator access to install it...\n")
        authorizeSudoForCurrentUser(onOutput: onOutput) { authorized in
            guard authorized else {
                onOutput("Administrator access was not granted; Homebrew was not installed.\n")
                onFinish(-1)
                return
            }
            onOutput("Downloading and running the official Homebrew installer...\n")
            runHomebrewInstaller(onOutput: onOutput) { status in
                guard status == 0 else {
                    onOutput("Homebrew installer exited with status \(status); Exo was not installed.\n")
                    onFinish(status)
                    return
                }
                onOutput("Homebrew installed. Installing Exo...\n")
                installViaHomebrew(onOutput: onOutput, onFinish: onFinish)
            }
        }
        return nil
    }

    /// Shows exactly one native macOS admin-password dialog and uses it to
    /// cache a sudo credential for the CURRENT user (see
    /// installHomebrewThenExo's doc comment for why `-u <user>`, not a bare
    /// `sudo -v`, is required here).
    private static func authorizeSudoForCurrentUser(onOutput: @escaping (String) -> Void, completion: @escaping (Bool) -> Void) {
        let user = NSUserName()
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        process.arguments = [
            "-e",
            "do shell script \"/usr/bin/sudo -u \(user) -v\" with administrator privileges with prompt \"mlxMesh needs administrator access to install Homebrew.\"",
        ]
        process.standardOutput = Pipe()
        process.standardError = Pipe()
        process.terminationHandler = { proc in
            Task { @MainActor in completion(proc.terminationStatus == 0) }
        }
        do {
            try process.run()
        } catch {
            onOutput("Failed to request administrator access: \(error.localizedDescription)\n")
            completion(false)
        }
    }

    /// Runs the official Homebrew installer as the CURRENT (non-root) user
    /// with NONINTERACTIVE=1 — never elevated; see installHomebrewThenExo's
    /// doc comment for why elevating this specific step would break it.
    private static func runHomebrewInstaller(onOutput: @escaping (String) -> Void, onFinish: @escaping (Int32) -> Void) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/bash")
        process.arguments = ["-c", "$(/usr/bin/curl -fsSL \(homebrewInstallScriptURL))"]
        var env = ProcessInfo.processInfo.environment
        env["NONINTERACTIVE"] = "1"
        process.environment = env

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
            onOutput("Failed to launch Homebrew installer: \(error.localizedDescription)\n")
            onFinish(-1)
        }
    }
}
