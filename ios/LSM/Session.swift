import Foundation
import Observation

/// The app's sign-in state. Sign in once with email and password; after
/// that the token lives in the Keychain and each launch unlocks it with
/// biometrics.
@Observable
final class Session {
    enum Phase: Equatable {
        case starting
        case locked // a token is stored; unlock to use it
        case signedOut
        case mustChangePassword
        case signedIn
    }

    var phase: Phase = .starting
    var me: Me?
    var serverURL: String {
        didSet { UserDefaults.standard.set(serverURL, forKey: "serverURL") }
    }
    /// Why the Keychain could not keep the token, if it could not; the
    /// session then lasts only until the app is closed.
    var keychainWarning: String?

    private var token: String?

    init() {
        serverURL = UserDefaults.standard.string(forKey: "serverURL") ?? "http://localhost:8080"
    }

    var api: APIClient {
        APIClient(baseURL: URL(string: serverURL) ?? URL(string: "http://localhost:8080")!, token: token)
    }

    func start() {
        #if DEBUG
        // UI tests start from a clean slate.
        if ProcessInfo.processInfo.arguments.contains("--reset-session") { TokenStore.delete() }
        #endif
        phase = TokenStore.exists() ? .locked : .signedOut
    }

    /// Reads the token behind Face ID / Touch ID, then loads the profile.
    func unlock() async {
        let reason = "Unlock your LSM account"
        do {
            let t = try await Task.detached { try TokenStore.load(reason: reason) }.value
            guard let t else {
                phase = .signedOut
                return
            }
            token = t
            try await loadMe()
        } catch TokenStore.Failure.cancelled {
            phase = .locked
        } catch let e as APIError where e.status == 401 {
            signOutLocally()
        } catch {
            // Offline or server unreachable: stay locked so the next
            // attempt can retry without signing in again.
            phase = .locked
            lastError = error.localizedDescription
        }
    }

    var lastError: String?

    func signIn(email: String, password: String) async throws {
        var client = api
        client.token = nil
        struct Body: Encodable { let username: String; let password: String }
        let res: SignInResponse = try await client.send("POST", "/session", body: Body(username: email, password: password))
        token = res.token
        do {
            try await Task.detached { [token = res.token] in try TokenStore.save(token) }.value
            keychainWarning = nil
        } catch {
            keychainWarning = "This device has no passcode, so you will need to sign in again next time."
        }
        try await loadMe()
    }

    func changePassword(current: String, new: String) async throws {
        struct Body: Encodable { let currentPassword: String; let newPassword: String }
        let me: Me = try await api.send("POST", "/me/password", body: Body(currentPassword: current, newPassword: new))
        self.me = me
        phase = .signedIn
    }

    func loadMe() async throws {
        let me: Me = try await api.get("/me")
        self.me = me
        phase = me.mustChangePassword ? .mustChangePassword : .signedIn
    }

    func signOut() async {
        struct Out: Decodable {}
        _ = try? await api.send("DELETE", "/session", as: Out.self)
        signOutLocally()
    }

    /// Any 401 means the session is gone (revoked, expired, or signed out
    /// elsewhere): forget it and ask for a password again.
    func handle(_ error: Error) -> String {
        if let e = error as? APIError, e.status == 401 {
            signOutLocally()
        }
        return error.localizedDescription
    }

    private func signOutLocally() {
        TokenStore.delete()
        token = nil
        me = nil
        phase = .signedOut
    }
}
