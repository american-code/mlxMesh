import SwiftUI

struct ExoStatusRow: View {
    @Bindable var appState: AppState
    @State private var installOutput: [String] = []
    @State private var isInstalling = false

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
                Button("Launch Exo") { ExoLauncher.launchApp() }
                    .controlSize(.small)
            } else {
                HStack {
                    Button("Download Exo") { ExoLauncher.openDownloadPage() }
                        .controlSize(.small)
                    Button("Install via Homebrew") { startHomebrewInstall() }
                        .controlSize(.small)
                        .disabled(isInstalling)
                }
            }
        }
    }

    private func startHomebrewInstall() {
        isInstalling = true
        installOutput.removeAll()
        ExoLauncher.installViaHomebrew(
            onOutput: { chunk in installOutput.append(chunk) },
            onFinish: { _ in isInstalling = false }
        )
    }
}
