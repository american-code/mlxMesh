import SwiftUI

struct ExoStatusRow: View {
    @Bindable var appState: AppState
    @State private var installOutput: [String] = []
    @State private var isInstalling = false
    @State private var launchError: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Image(systemName: statusSymbol)
                    .foregroundStyle(statusColor)
                Text("Exo")
                    .fontWeight(.medium)
                Spacer()
                Text(statusLabel)
                    .foregroundStyle(.secondary)
                    .font(.caption)
            }
            actionButton
            if isInstalling {
                ScrollView {
                    Text(installOutput.joined())
                        .font(.system(size: 10, design: .monospaced))
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(height: 60)
                .background(.quaternary.opacity(0.3))
            }
            // launchApp already self-heals a Gatekeeper "damaged" quarantine
            // error automatically (strip + retry) — this only ever appears
            // if that retry itself failed, a genuine second problem.
            if let launchError {
                Text(launchError)
                    .font(.caption)
                    .foregroundStyle(.red)
            }
        }
    }

    private var statusSymbol: String {
        switch appState.exoMonitor.health {
        case .healthy: return "checkmark.circle.fill"
        case .unreachable: return ExoDetector.isInstalled() ? "moon.zzz.fill" : "questionmark.circle"
        case .checking: return "ellipsis.circle"
        }
    }

    private var statusColor: Color {
        switch appState.exoMonitor.health {
        case .healthy: return .green
        case .unreachable: return ExoDetector.isInstalled() ? .orange : .secondary
        case .checking: return .secondary
        }
    }

    private var statusLabel: String {
        switch appState.exoMonitor.health {
        case .healthy: return "Healthy"
        case .checking: return "Checking…"
        case .unreachable: return ExoDetector.isInstalled() ? "Not running" : "Not installed"
        }
    }

    @ViewBuilder
    private var actionButton: some View {
        switch appState.exoMonitor.health {
        case .healthy:
            EmptyView()
        case .checking:
            EmptyView()
        case .unreachable:
            if ExoDetector.isInstalled() {
                Button("Launch Exo") { startLaunch() }
                    .controlSize(.small)
            } else {
                HStack {
                    // Seamless path: installs Homebrew (one admin prompt,
                    // only if not already present) then Exo via the cask —
                    // no separate "do you have Homebrew?" step for the user.
                    Button("Install Exo") { startInstall() }
                        .controlSize(.small)
                        .disabled(isInstalling)
                    // Manual fallback for anyone who'd rather not grant
                    // administrator access to this app.
                    Button("Download Exo") { ExoLauncher.openDownloadPage() }
                        .controlSize(.small)
                }
            }
        }
    }

    private func startInstall() {
        isInstalling = true
        installOutput.removeAll()
        launchError = nil
        ExoLauncher.installHomebrewThenExo(
            onOutput: { chunk in installOutput.append(chunk) },
            onFinish: { _ in isInstalling = false }
        )
    }

    private func startLaunch() {
        launchError = nil
        ExoLauncher.launchApp(
            onOutput: { _ in }, // self-heal retry is transparent; only a final failure needs a UI
            onFailure: { message in launchError = message }
        )
    }
}
