import Foundation
import Observation

/// Thin glue between the reused `WalletStore` and the real oim node identity
/// (NodeIdentityReader) — deliberately no new crypto/network code, and no
/// separate device-identity scheme (see NodeIdentityReader's doc comment).
///
/// There is no read-only "is this node linked" endpoint on the coordinator
/// (confirmed: only POST /account/challenge, /account/auth, and
/// /account/{address}/link-device exist). WalletStore's local `linkedDevices`
/// list is advisory only per its own doc comment — "the authoritative binding
/// lives in the coordinator's wallet Manager." So rather than trust local
/// state as ground truth, this coordinator re-POSTs link-device idempotently
/// once per node-start (confirmed safe: `Manager.LinkDevice` is a plain
/// `m.deviceLinks[deviceNodeID] = accountAddress` map write — repeating it is
/// a no-op, not a duplicate-record bug).
@Observable
@MainActor
final class WalletLinkCoordinator {
    let walletStore: WalletStore

    private(set) var isLinking = false
    private(set) var lastLinkError: String?
    /// Set true once link-device has succeeded THIS session for the current
    /// node ID — the proactive "you're contributing for free" banner clears
    /// on this, not on WalletStore.linkedDevices (which may be stale/empty on
    /// a fresh install even though the coordinator-side binding already
    /// exists from a previous run).
    private(set) var linkedThisSession = false

    // `walletStore: WalletStore = WalletStore()` as a parameter default would
    // fail to compile: Swift evaluates default-argument expressions in a
    // context it does NOT treat as inheriting the enclosing @MainActor
    // isolation (unlike a stored property's own default-value initializer,
    // which does) — so constructing another @MainActor type there is
    // rejected as "main actor-isolated initializer in a synchronous
    // nonisolated context." Passing `nil` and constructing inside the
    // (actor-isolated) init body sidesteps it.
    init(walletStore: WalletStore? = nil) {
        self.walletStore = walletStore ?? WalletStore()
    }

    /// Links `nodeID` to the current wallet account. Call this once a node
    /// has reached NodeProcessController.State.running and a wallet exists —
    /// safe to call repeatedly across app launches for the same node ID.
    func linkCurrentNode(nodeID: String, coordinatorURL: String) async {
        guard walletStore.hasWallet else {
            lastLinkError = "No wallet set up on this Mac yet"
            return
        }
        isLinking = true
        defer { isLinking = false }

        if let error = await walletStore.linkDevice(nodeID, coordinatorURL: coordinatorURL) {
            lastLinkError = error
            linkedThisSession = false
        } else {
            lastLinkError = nil
            linkedThisSession = true
        }
    }

    func resetSessionLinkState() {
        linkedThisSession = false
        lastLinkError = nil
    }
}
