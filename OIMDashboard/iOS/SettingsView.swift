import SwiftUI

struct SettingsView: View {
    @Environment(TopologyStore.self) private var store

    @State private var urlDraft = NetworkClient.directoryURL
    @State private var showSavedFeedback = false

    private let defaultURL = "http://localhost:9100"

    var body: some View {
        Form {
            Section {
                HStack {
                    TextField("http://hostname:9100", text: $urlDraft)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .font(.system(.body, design: .monospaced))
                }
            } header: {
                Text("Directory URL(s)")
            } footer: {
                Text("The oim-directory service endpoint. Coordinators and nodes are discovered automatically from here. Comma-separate multiple endpoints (e.g. \"https://directory.mlxmesh.net,https://directory2.mlxmesh.net\") to keep working if one is unreachable — tried in order, first to answer wins. Use https:// for any endpoint outside your local network — plaintext http is permitted only for LAN/local hosts. For a self-signed dev cert, install its CA on this device first (see scripts/gen-dev-certs.sh).")
            }

            Section {
                Button("Reset to Default") {
                    urlDraft = defaultURL
                }
                .foregroundStyle(.orange)
            }

            if showSavedFeedback {
                Section {
                    Label("Saved — reconnecting…", systemImage: "checkmark.circle.fill")
                        .foregroundStyle(NodeStatus.live.color)
                }
            }
        }
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Save") { applyURL() }
                    .disabled(urlDraft.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
    }

    private func applyURL() {
        let trimmed = urlDraft.trimmingCharacters(in: .whitespaces)
        NetworkClient.directoryURL = trimmed
        showSavedFeedback = true
        Task { await store.refresh() }
    }
}
