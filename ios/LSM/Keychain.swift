import Foundation
import LocalAuthentication
import Security

/// Keeps the session token in the Keychain behind user presence: reading
/// it asks for Face ID or Touch ID, falling back to the device passcode.
/// The token never leaves this device and is only readable while the
/// device is unlocked and has a passcode set.
nonisolated enum TokenStore {
    private static let service = "com.jasonfigge.lsm.session"
    private static let account = "token"

    enum Failure: Error { case unavailable(OSStatus), cancelled }

    static func save(_ token: String) throws {
        delete()
        var error: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(
            nil, kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly, .userPresence, &error)
        else { throw Failure.unavailable(errSecParam) }
        let status = SecItemAdd([
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrAccount: account,
            kSecValueData: Data(token.utf8),
            kSecAttrAccessControl: access,
        ] as CFDictionary, nil)
        guard status == errSecSuccess else { throw Failure.unavailable(status) }
    }

    /// Reads the token, prompting for biometrics. Blocks while the prompt
    /// is up, so never call it on the main actor.
    static func load(reason: String) throws -> String? {
        let context = LAContext()
        context.localizedReason = reason
        var out: CFTypeRef?
        let status = SecItemCopyMatching([
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrAccount: account,
            kSecReturnData: true,
            kSecUseAuthenticationContext: context,
        ] as CFDictionary, &out)
        switch status {
        case errSecSuccess:
            return (out as? Data).flatMap { String(data: $0, encoding: .utf8) }
        case errSecItemNotFound:
            return nil
        case errSecUserCanceled, errSecAuthFailed:
            throw Failure.cancelled
        default:
            throw Failure.unavailable(status)
        }
    }

    /// Whether a token is stored, without prompting.
    static func exists() -> Bool {
        let context = LAContext()
        context.interactionNotAllowed = true
        let status = SecItemCopyMatching([
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrAccount: account,
            kSecUseAuthenticationContext: context,
        ] as CFDictionary, nil)
        // Protected items report "interaction not allowed" when they exist.
        return status == errSecSuccess || status == errSecInteractionNotAllowed
    }

    static func delete() {
        SecItemDelete([
            kSecClass: kSecClassGenericPassword,
            kSecAttrService: service,
            kSecAttrAccount: account,
        ] as CFDictionary)
    }

    /// Face ID, Touch ID or none, for button labels.
    static func biometryName() -> String {
        let c = LAContext()
        _ = c.canEvaluatePolicy(.deviceOwnerAuthentication, error: nil)
        switch c.biometryType {
        case .faceID: return "Face ID"
        case .touchID: return "Touch ID"
        case .opticID: return "Optic ID"
        default: return "passcode"
        }
    }
}
