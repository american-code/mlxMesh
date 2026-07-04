import Foundation

/// A stable, per-install device identifier.
///
/// The SAME id is used when this device announces itself as a coordination
/// participant (`/coordination/announce`) AND when the user links the device to
/// a wallet account (`/account/{addr}/link-device`). It MUST be stable across
/// launches: the coordinator credits a device's served-pointer work to the
/// linked account by matching this exact id, so an ephemeral (per-launch) id
/// could never be linked and would never earn — which is why participating
/// devices previously showed 0 credits forever.
enum DeviceIdentity {
    private static let key = "oim.device.id.v1"

    /// The stable coordination device id for this install. Generated once and
    /// persisted; identical on every subsequent launch.
    static var current: String {
        if let existing = UserDefaults.standard.string(forKey: key), !existing.isEmpty {
            return existing
        }
        let (_, pub) = Ed25519.generate()
        let id = pub.prefix(16).hexString
        UserDefaults.standard.set(id, forKey: key)
        return id
    }
}
