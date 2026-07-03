import SwiftUI

// MARK: - Persistent anonymous user ID
// (Balance model now lives in Shared/Models.swift — NetworkClient references it.)

private let userIdKey = "oim_user_id"

func getOrCreateUserId() -> String {
    if let id = UserDefaults.standard.string(forKey: userIdKey) { return id }
    let id = UUID().uuidString
    UserDefaults.standard.set(id, forKey: userIdKey)
    return id
}

// MARK: - Gauge arc helpers

private func arcPath(cx: Double, cy: Double, r: Double,
                     startDeg: Double, endDeg: Double) -> Path {
    var p = Path()
    let start = CGFloat(startDeg * .pi / 180)
    let end   = CGFloat(endDeg   * .pi / 180)
    p.addArc(center: CGPoint(x: cx, y: cy), radius: CGFloat(r),
             startAngle: .radians(Double(start)), endAngle: .radians(Double(end)),
             clockwise: false)
    return p
}

// MARK: - Credit Gauge

struct CreditGauge: View {
    let earned: Double
    let grant: Double

    private let cx = 110.0, cy = 90.0, r = 72.0
    private let startDeg = 140.0, sweep = 260.0

    private var total: Double { earned + grant }
    private var earnedFrac: Double { total > 0 ? earned / total : 0 }
    private var grantFrac:  Double { total > 0 ? grant  / total : 0 }

    var body: some View {
        Canvas { ctx, _ in
            let lineWidth: CGFloat = 11

            // Track
            ctx.stroke(arcPath(cx: cx, cy: cy, r: r, startDeg: startDeg, endDeg: startDeg + sweep),
                       with: .color(Color(.systemGray5)), style: StrokeStyle(lineWidth: lineWidth, lineCap: .round))

            // Earned (green)
            let earnedEnd = startDeg + sweep * earnedFrac
            if earnedFrac > 0 {
                ctx.stroke(arcPath(cx: cx, cy: cy, r: r, startDeg: startDeg, endDeg: earnedEnd),
                           with: .color(NodeStatus.live.color), style: StrokeStyle(lineWidth: lineWidth, lineCap: .round))
            }

            // Grant (amber)
            let grantEnd = earnedEnd + sweep * grantFrac
            if grantFrac > 0 {
                ctx.stroke(arcPath(cx: cx, cy: cy, r: r, startDeg: earnedEnd, endDeg: grantEnd),
                           with: .color(NodeStatus.degraded.color), style: StrokeStyle(lineWidth: lineWidth, lineCap: .round))
            }
        }
        .frame(width: 220, height: 140)
        .overlay(alignment: .center) {
            VStack(spacing: 2) {
                Text(total, format: .number.precision(.fractionLength(1)))
                    .font(.system(size: 26, weight: .bold, design: .rounded))
                    .monospacedDigit()
                Text("CREDITS")
                    .font(.system(size: 9, weight: .semibold))
                    .foregroundStyle(.secondary)
                    .kerning(0.8)
            }
            .offset(y: -8)
        }
    }
}

// MARK: - Account View

struct AccountView: View {
    @Environment(TopologyStore.self) private var store
    @Environment(WalletStore.self) private var wallet

    @State private var userId = getOrCreateUserId()
    @State private var balance: Balance?
    @State private var loading = false
    @State private var error: String?
    @State private var claiming = false
    @State private var claimMsg: String?

    private var coordinatorURL: String? { store.pods.first?.coordinatorEndpoint }

    // The identity credits key on: the wallet account address when a wallet
    // exists (so the balance is consolidated across every device linked to it),
    // otherwise the legacy anonymous per-device UUID.
    private var identityId: String { wallet.address ?? userId }

    var body: some View {
        List {
            // ── Wallet / identity ──
            WalletSection(coordinatorURL: coordinatorURL) { await loadBalance() }

            // ── Credit Gauge ──
            Section("Credit Balance") {
                let b = balance ?? Balance(grantBalance: 0, earnedBalance: 0, total: 0)

                HStack {
                    Spacer()
                    CreditGauge(earned: b.earnedBalance, grant: b.grantBalance)
                    Spacer()
                }
                .listRowInsets(EdgeInsets(top: 12, leading: 0, bottom: 8, trailing: 0))

                CreditRow(label: "Earned from contribution",
                          value: b.earnedBalance,
                          color: NodeStatus.live.color,
                          subtitle: "From verified inference work")
                CreditRow(label: "Startup grant",
                          value: b.grantBalance,
                          color: NodeStatus.degraded.color,
                          subtitle: "One-time bootstrap allocation")
                CreditRow(label: "Total available",
                          value: b.total,
                          color: .primary,
                          subtitle: "Spendable on inference jobs",
                          bold: true)

                if let err = error {
                    Label(err, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(.red)
                }

                HStack(spacing: 12) {
                    Button {
                        Task { await loadBalance() }
                    } label: {
                        Label("Refresh", systemImage: "arrow.clockwise")
                            .font(.subheadline)
                            .symbolEffect(.rotate, isActive: loading)
                    }
                    .disabled(loading || coordinatorURL == nil)

                    if b.grantBalance == 0 {
                        Button {
                            Task { await claimGrant() }
                        } label: {
                            Label(claiming ? "Claiming…" : "Claim startup grant",
                                  systemImage: "gift.fill")
                                .font(.subheadline)
                                .foregroundStyle(NodeStatus.degraded.color)
                        }
                        .disabled(claiming || coordinatorURL == nil)
                    } else {
                        Label("Grant claimed", systemImage: "checkmark.circle.fill")
                            .font(.subheadline)
                            .foregroundStyle(NodeStatus.live.color)
                    }
                }
                .padding(.vertical, 4)

                if let msg = claimMsg {
                    Text(msg)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }

            // ── How credits work ──
            Section("How Credits Work") {
                InfoRow(icon: "bolt.fill", color: .yellow,
                        text: "Run inference on the mesh → earn credits proportional to tokens delivered")
                InfoRow(icon: "lock.fill", color: .blue,
                        text: "Measurement is cryptographically signed — reflects actual throughput, not self-declared specs")
                InfoRow(icon: "gift.fill", color: NodeStatus.degraded.color,
                        text: "Startup grant lets you use the network before you've contributed")
                InfoRow(icon: "scalemass.fill", color: .purple,
                        text: "Spend credits to submit jobs. No native token. No external conversion.")
            }
        }
        .navigationTitle("Account")
        .task { await loadBalance() }
    }

    private func loadBalance() async {
        guard let url = coordinatorURL else { return }
        loading = true
        error = nil
        defer { loading = false }
        do {
            // Routed through NetworkClient so the coordinator's loopback host is
            // rewritten to the reachable directory host on a physical device.
            balance = try await NetworkClient.fetchBalance(coordinatorURL: url, userId: identityId)
        } catch {
            self.error = error.localizedDescription
        }
    }

    private func claimGrant() async {
        guard let url = coordinatorURL else { return }
        claiming = true
        defer { claiming = false }
        do {
            let base = NetworkClient.resolvedCoordinator(url)
            var req = URLRequest(url: URL(string: "\(base)/users/\(identityId)/startup-grant")!)
            req.httpMethod = "POST"
            let (_, _) = try await URLSession.shared.data(for: req)
            claimMsg = "Grant claimed successfully"
            await loadBalance()
        } catch {
            claimMsg = error.localizedDescription
        }
    }
}

// MARK: - Helpers

struct CreditRow: View {
    let label: String
    let value: Double
    let color: Color
    let subtitle: String
    var bold: Bool = false

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Text(label)
                    .font(bold ? .subheadline.bold() : .subheadline)
                Spacer()
                Text(value, format: .number.precision(.fractionLength(2)))
                    .font(.system(size: bold ? 17 : 15, weight: .bold, design: .rounded))
                    .foregroundStyle(color)
                    .monospacedDigit()
            }
            Text(subtitle)
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
    }
}

// MARK: - Wallet Section

/// Wallet UI folded into the Account screen: create/import a portable account,
/// reveal the recovery key, prove ownership to the coordinator, and link other
/// devices (e.g. a Mac Studio node) so their earnings consolidate here.
struct WalletSection: View {
    @Environment(WalletStore.self) private var wallet
    let coordinatorURL: String?
    let onIdentityChanged: () async -> Void

    @State private var showRecovery = false
    @State private var showImport = false
    @State private var importText = ""
    @State private var linkNodeID = ""
    @State private var linkMsg: String?
    @State private var busy = false

    private var resolvedURL: String? { coordinatorURL.map { NetworkClient.resolvedCoordinator($0) } }

    var body: some View {
        if !wallet.hasWallet {
            Section("Wallet") {
                Text("Create a wallet to consolidate credits across your devices and recover your balance if a device is lost. Your key is stored in iCloud Keychain and never leaves your devices.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Button {
                    wallet.createWallet()
                    Task { await onIdentityChanged() }
                } label: {
                    Label("Create wallet", systemImage: "wallet.pass.fill")
                }
                Button {
                    showImport = true
                } label: {
                    Label("Restore from recovery key", systemImage: "arrow.down.doc.fill")
                        .foregroundStyle(.secondary)
                }
            }
            .alert("Restore wallet", isPresented: $showImport) {
                TextField("Recovery key", text: $importText)
                    .textInputAutocapitalization(.characters)
                Button("Restore") {
                    if wallet.importWallet(recoveryKey: importText) {
                        Task { await onIdentityChanged() }
                    }
                    importText = ""
                }
                Button("Cancel", role: .cancel) { importText = "" }
            } message: {
                Text("Enter the recovery key you saved when creating the wallet.")
            }
        } else {
            Section("Wallet") {
                VStack(alignment: .leading, spacing: 6) {
                    HStack {
                        Label("Account", systemImage: "wallet.pass.fill")
                            .font(.subheadline)
                        Spacer()
                        if wallet.isAuthenticated {
                            Label("Verified", systemImage: "checkmark.seal.fill")
                                .font(.caption2).foregroundStyle(NodeStatus.live.color)
                        }
                    }
                    Text(wallet.address ?? "")
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .lineLimit(2)
                        .truncationMode(.middle)
                }
                .padding(.vertical, 2)

                Button {
                    guard let url = resolvedURL else { return }
                    busy = true
                    Task { await wallet.authenticate(coordinatorURL: url); busy = false }
                } label: {
                    Label(wallet.isAuthenticated ? "Re-verify ownership" : "Verify ownership",
                          systemImage: "person.badge.key.fill")
                        .font(.subheadline)
                }
                .disabled(busy || resolvedURL == nil)

                if let err = wallet.authError {
                    Label(err, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.red)
                }

                Button {
                    showRecovery.toggle()
                } label: {
                    Label(showRecovery ? "Hide recovery key" : "Show recovery key",
                          systemImage: "key.fill")
                        .font(.subheadline).foregroundStyle(NodeStatus.degraded.color)
                }
                if showRecovery, let rk = wallet.recoveryKey {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(rk)
                            .font(.system(size: 12, design: .monospaced))
                            .textSelection(.enabled)
                            .padding(8)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 8))
                        Text("Write this down and keep it safe. Anyone with this key controls your credits, and it is the only backup outside iCloud Keychain.")
                            .font(.caption2).foregroundStyle(.secondary)
                    }
                }
            }

            // ── Link another device ──
            Section("Consolidate a device") {
                Text("Link a Mac / node you run so its earnings credit this account. Enter its node ID (shown in that node's setup).")
                    .font(.caption).foregroundStyle(.secondary)
                TextField("Device / node ID", text: $linkNodeID)
                    .font(.system(size: 13, design: .monospaced))
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                Button {
                    guard let url = resolvedURL else { return }
                    busy = true
                    Task {
                        linkMsg = await wallet.linkDevice(linkNodeID, coordinatorURL: url) ?? "Linked — earnings now consolidate here."
                        if wallet.linkedDevices.contains(linkNodeID.trimmingCharacters(in: .whitespacesAndNewlines)) { linkNodeID = "" }
                        busy = false
                    }
                } label: {
                    Label("Link device", systemImage: "link")
                }
                .disabled(busy || resolvedURL == nil || linkNodeID.trimmingCharacters(in: .whitespaces).isEmpty)

                if let msg = linkMsg {
                    Text(msg).font(.caption).foregroundStyle(.secondary)
                }
                ForEach(wallet.linkedDevices, id: \.self) { dev in
                    Label(dev, systemImage: "checkmark.circle.fill")
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundStyle(NodeStatus.live.color)
                }
            }
        }
    }
}

struct InfoRow: View {
    let icon: String
    let color: Color
    let text: String

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: icon)
                .foregroundStyle(color)
                .frame(width: 20)
            Text(text)
                .font(.subheadline)
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
    }
}
