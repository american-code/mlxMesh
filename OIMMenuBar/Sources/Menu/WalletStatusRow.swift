import AppKit
import SwiftUI

struct WalletStatusRow: View {
    @Bindable var appState: AppState
    @State private var showingImportSheet = false
    @State private var recoveryKeyInput = ""
    @State private var showFullAddress = false
    @State private var showRecoveryKey = false
    @State private var copiedAddress = false
    @State private var copiedRecoveryKey = false
    @State private var showingCreateConfirm = false

    private var wallet: WalletStore { appState.walletCoordinator.walletStore }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Image(systemName: statusSymbol)
                    .foregroundStyle(statusColor)
                Text("Wallet")
                    .fontWeight(.medium)
                Spacer()
                actionButton
            }
            Text(statusLabel)
                .font(.caption)
                .foregroundStyle(.secondary)
            if let error = appState.walletCoordinator.lastLinkError {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(.red)
            }
            // The multiple-wallets trap: on a 2nd/3rd Mac, hitting "Create Wallet"
            // instead of "Import…" silently makes a brand-new, separate account —
            // this Mac's earnings would then never consolidate with the others.
            // Surfaced here (not just as a confirmation dialog) since it's the
            // single most consequential wrong tap in this whole popover.
            if !wallet.hasWallet {
                Text("Already running mlxMesh on another Mac? Use Import, not Create — Create makes a new, separate wallet.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            // Same account address/recovery key an iPad running mlxMesh needs to
            // either (a) just sign into the same iCloud account — the seed is
            // iCloud-Keychain-synced, so it appears there with zero action — or
            // (b) manually restore via "Restore from recovery key" on the iPad's
            // Account screen if it's on a different Apple ID / has Keychain sync
            // off. Only the recovery key round-trips a wallet across accounts;
            // the address alone can't recreate it.
            if wallet.hasWallet {
                walletDetails
            }
        }
        .sheet(isPresented: $showingImportSheet) {
            VStack(spacing: 12) {
                Text("Import Existing Wallet")
                    .font(.headline)
                Text("Paste the recovery key shown by mlxMesh on another device.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                TextField("XXXX-XXXX-XXXX-…", text: $recoveryKeyInput)
                    .textFieldStyle(.roundedBorder)
                HStack {
                    Button("Cancel") { showingImportSheet = false }
                    Spacer()
                    Button("Import") {
                        if wallet.importWallet(recoveryKey: recoveryKeyInput) {
                            showingImportSheet = false
                            recoveryKeyInput = ""
                        }
                    }
                    .keyboardShortcut(.defaultAction)
                }
            }
            .padding()
            .frame(width: 320)
        }
    }

    private var statusSymbol: String {
        if !wallet.hasWallet { return "wallet.pass" }
        return appState.walletCoordinator.linkedThisSession ? "checkmark.circle.fill" : "exclamationmark.triangle.fill"
    }

    private var statusColor: Color {
        if !wallet.hasWallet { return .secondary }
        return appState.walletCoordinator.linkedThisSession ? .green : .orange
    }

    private var statusLabel: String {
        guard wallet.hasWallet else { return "Not set up" }
        guard let address = wallet.address else { return "Not set up" }
        let short = "\(address.prefix(10))…"
        return appState.walletCoordinator.linkedThisSession ? "Linked as \(short)" : "\(short) — not linked yet"
    }

    @ViewBuilder
    private var walletDetails: some View {
        VStack(alignment: .leading, spacing: 4) {
            Button(showFullAddress ? "Hide address" : "Show full address") {
                showFullAddress.toggle()
            }
            .font(.caption)
            .buttonStyle(.plain)
            .foregroundStyle(.blue)

            if showFullAddress, let address = wallet.address {
                copyableBlock(text: address, copied: copiedAddress) { copy(address, flag: $copiedAddress) }
            }

            Button(showRecoveryKey ? "Hide recovery key" : "Show recovery key (to link iPad/iPhone)") {
                showRecoveryKey.toggle()
            }
            .font(.caption)
            .buttonStyle(.plain)
            .foregroundStyle(.orange)

            if showRecoveryKey, let recoveryKey = wallet.recoveryKey {
                copyableBlock(text: recoveryKey, copied: copiedRecoveryKey) { copy(recoveryKey, flag: $copiedRecoveryKey) }
                Text("Paste this into \"Restore from recovery key\" on the other device's Account screen. Anyone with this key controls these credits.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private func copyableBlock(text: String, copied: Bool, onCopy: @escaping () -> Void) -> some View {
        HStack(alignment: .top, spacing: 6) {
            Text(text)
                .font(.system(size: 11, design: .monospaced))
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
            Button {
                onCopy()
            } label: {
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
            }
            .buttonStyle(.plain)
            .help("Copy")
        }
        .padding(6)
        .background(Color.secondary.opacity(0.1), in: RoundedRectangle(cornerRadius: 5))
    }

    private func copy(_ text: String, flag: Binding<Bool>) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        flag.wrappedValue = true
        Task {
            try? await Task.sleep(for: .seconds(2))
            flag.wrappedValue = false
        }
    }

    @ViewBuilder
    private var actionButton: some View {
        if !wallet.hasWallet {
            HStack {
                Button("Create Wallet") { showingCreateConfirm = true }
                    .controlSize(.small)
                    .confirmationDialog(
                        "Create a new wallet?",
                        isPresented: $showingCreateConfirm,
                        titleVisibility: .visible
                    ) {
                        Button("Create New Wallet") { wallet.createWallet() }
                        Button("Cancel", role: .cancel) {}
                    } message: {
                        Text("Only do this if you don't already have an mlxMesh wallet on another device. If you do, use Import instead so this Mac's earnings consolidate into that same account.")
                    }
                Button("Import…") { showingImportSheet = true }
                    .controlSize(.small)
            }
        } else if !appState.walletCoordinator.linkedThisSession {
            Button("Link this Mac") {
                Task { await appState.linkNodeIfPossible() }
            }
            .controlSize(.small)
            .disabled(!appState.isNodeRunning || appState.walletCoordinator.isLinking)
        }
    }
}
