import SwiftUI

@main
struct LSMApp: App {
    @State private var session = Session()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(session)
                .tint(Theme.accent)
        }
    }
}

enum Theme {
    /// Hurricanes red, as on the web pages.
    static let accent = Color(red: 0.78, green: 0.06, blue: 0.18)
}

struct RootView: View {
    @Environment(Session.self) private var session
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        Group {
            switch session.phase {
            case .starting:
                ProgressView()
            case .locked:
                UnlockView()
            case .signedOut:
                SignInView()
            case .mustChangePassword:
                ChangePasswordView()
            case .signedIn:
                MainTabView()
            }
        }
        .task {
            session.start()
            if session.phase == .locked { await session.unlock() }
        }
    }
}

// MARK: - Sign-in

struct SignInView: View {
    @Environment(Session.self) private var session
    @State private var email = ""
    @State private var password = ""
    @State private var error: String?
    @State private var busy = false
    @State private var showServer = false

    var body: some View {
        @Bindable var session = session
        NavigationStack {
            Form {
                Section {
                    TextField("Email", text: $email)
                        .textContentType(.username)
                        .keyboardType(.emailAddress)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    SecureField("Password", text: $password)
                        .textContentType(.password)
                        .onSubmit(submit)
                } footer: {
                    Text("You sign in once. After that, \(TokenStore.biometryName()) unlocks the app.")
                }
                if let error {
                    Section { Text(error).foregroundStyle(.red) }
                }
                Section {
                    Button(action: submit) {
                        HStack {
                            Text("Sign in").bold()
                            Spacer()
                            if busy { ProgressView() }
                        }
                    }
                    .disabled(busy || email.isEmpty || password.isEmpty)
                }
                Section {
                    DisclosureGroup("Server", isExpanded: $showServer) {
                        TextField("http://laptop.local:8080", text: $session.serverURL)
                            .keyboardType(.URL)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    }
                } footer: {
                    Text(session.serverURL).font(.caption2)
                }
            }
            .navigationTitle("LSM")
        }
    }

    private func submit() {
        guard !busy, !email.isEmpty, !password.isEmpty else { return }
        busy = true
        error = nil
        Task {
            do {
                try await session.signIn(email: email, password: password)
            } catch {
                self.error = error.localizedDescription
            }
            busy = false
        }
    }
}

struct ChangePasswordView: View {
    @Environment(Session.self) private var session
    @State private var current = ""
    @State private var new = ""
    @State private var again = ""
    @State private var error: String?
    @State private var busy = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    SecureField("Current password", text: $current).textContentType(.password)
                    SecureField("New password", text: $new).textContentType(.newPassword)
                    SecureField("New password again", text: $again).textContentType(.newPassword)
                } header: {
                    Text("Choose a new password")
                } footer: {
                    Text("Your account still has its starting password. Use at least 8 characters.")
                }
                if let error { Section { Text(error).foregroundStyle(.red) } }
                Section {
                    Button("Change password") {
                        guard new == again else {
                            error = "The new passwords do not match."
                            return
                        }
                        busy = true
                        Task {
                            do { try await session.changePassword(current: current, new: new) } catch {
                                self.error = session.handle(error)
                            }
                            busy = false
                        }
                    }
                    .disabled(busy || current.isEmpty || new.count < 8)
                }
                Section { Button("Sign out", role: .destructive) { Task { await session.signOut() } } }
            }
            .navigationTitle("New password")
        }
    }
}

struct UnlockView: View {
    @Environment(Session.self) private var session
    @State private var busy = false

    var body: some View {
        VStack(spacing: 20) {
            Spacer()
            Image(systemName: "lock.shield")
                .font(.system(size: 56))
                .foregroundStyle(Theme.accent)
            Text("LSM is locked").font(.title2.bold())
            if let e = session.lastError {
                Text(e).font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center)
            }
            Button {
                busy = true
                Task {
                    await session.unlock()
                    busy = false
                }
            } label: {
                Label("Unlock with \(TokenStore.biometryName())", systemImage: "faceid")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(busy)
            Button("Sign in with a password instead") {
                TokenStore.delete()
                session.phase = .signedOut
            }
            .font(.footnote)
            Spacer()
        }
        .padding(32)
    }
}
