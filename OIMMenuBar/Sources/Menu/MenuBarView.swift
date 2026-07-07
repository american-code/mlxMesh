import SwiftUI

struct MenuBarView: View {
    @Bindable var appState: AppState

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            header

            if appState.showsUnlinkedWarning {
                unlinkedBanner
            }

            Divider()
            ExoStatusRow(appState: appState)
            Divider()
            NodeStatusRow(appState: appState)
            Divider()
            WalletStatusRow(appState: appState)
            Divider()

            SettingsSection(appState: appState)
            LogViewerView(lines: appState.nodeController.recentLogLines)

            Divider()
            Button("Quit mlxMesh") {
                NSApplication.shared.terminate(nil)
            }
        }
        .padding(12)
        .frame(width: 320)
        // The node reaching .running is the trigger to (idempotently) link it
        // to the wallet and start the local/coordinator status pollers — see
        // AppState.nodeStateChanged's doc comment.
        .onChange(of: appState.nodeController.state) { _, newState in
            appState.nodeStateChanged(newState)
        }
    }

    private var header: some View {
        HStack {
            Image(systemName: appState.iconState.symbolName)
                .foregroundStyle(appState.iconState.tint ?? .primary)
            VStack(alignment: .leading, spacing: 0) {
                Text("mlxMesh")
                    .fontWeight(.semibold)
                Text(appState.iconState.summary)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
        }
    }

    private var unlinkedBanner: some View {
        HStack(alignment: .top, spacing: 6) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
            VStack(alignment: .leading, spacing: 2) {
                Text("Not linked — contributing for free right now")
                    .font(.caption)
                    .fontWeight(.medium)
                Text("Set up a wallet and link this Mac below to earn credits.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(8)
        .background(.orange.opacity(0.12))
        .clipShape(RoundedRectangle(cornerRadius: 6))
    }
}
