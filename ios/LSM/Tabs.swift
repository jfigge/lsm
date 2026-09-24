import SwiftUI

/// Bottom tabs in ABI's order: Home, Schedule, Availability, Time, More.
/// The supervisor tab joins later (build step 11).
struct MainTabView: View {
    var body: some View {
        // tabItem rather than Tab(...) keeps iOS 17 supported.
        TabView {
            Screen(title: "Home") { HomeView() }
                .tabItem { Label("Home", systemImage: "house") }
            Screen(title: "Schedule") { ScheduleView() }
                .tabItem { Label("Schedule", systemImage: "calendar") }
            Screen(title: "Availability") { AvailabilityView() }
                .tabItem { Label("Availability", systemImage: "checklist") }
            Screen(title: "Time") { ComingSoon(what: "Pay periods with in, out and hours per event, from your check-ins.") }
                .tabItem { Label("Time", systemImage: "clock") }
            Screen(title: "More") { ComingSoon(what: "Training records and policy documents.") }
                .tabItem { Label("More", systemImage: "ellipsis.circle") }
        }
    }
}

/// Every tab shares the header: messaging on the left, profile on the right.
struct Screen<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content
    @State private var showMessages = false
    @State private var showProfile = false

    var body: some View {
        NavigationStack {
            content
                .navigationTitle(title)
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button { showMessages = true } label: { Image(systemName: "bubble.left.and.bubble.right") }
                            .accessibilityLabel("Messages")
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { showProfile = true } label: { Image(systemName: "person.crop.circle") }
                            .accessibilityLabel("Profile")
                    }
                }
                .sheet(isPresented: $showMessages) {
                    NavigationStack {
                        ComingSoon(what: "Messages, contacting the scheduler, and shift reminders.")
                            .navigationTitle("Messages")
                            .toolbar { Button("Done") { showMessages = false } }
                    }
                }
                .sheet(isPresented: $showProfile) { ProfileView() }
        }
    }
}

struct ComingSoon: View {
    let what: String
    var body: some View {
        ContentUnavailableView {
            Label("Coming soon", systemImage: "hammer")
        } description: {
            Text(what)
        }
    }
}

struct ProfileView: View {
    @Environment(Session.self) private var session
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                if let me = session.me {
                    Section {
                        LabeledContent("Name", value: me.name)
                        LabeledContent("Badge", value: me.badge)
                        LabeledContent("Department", value: me.department)
                        if me.role != me.department { LabeledContent("Role", value: me.role) }
                        LabeledContent("Tenure", value: "\(me.tenureYears) years")
                        LabeledContent("Email", value: me.email)
                    }
                }
                if let w = session.keychainWarning {
                    Section { Text(w).font(.footnote).foregroundStyle(.secondary) }
                }
                Section {
                    Button("Sign out", role: .destructive) {
                        dismiss()
                        Task { await session.signOut() }
                    }
                }
            }
            .navigationTitle("Profile")
            .toolbar { Button("Done") { dismiss() } }
        }
    }
}

/// A date block: weekday over day number, as on ABI's shift cards.
struct DateBlock: View {
    let date: Date
    var body: some View {
        VStack(spacing: 0) {
            Text(Dates.format(date, "EEE").uppercased()).font(.caption2.bold()).foregroundStyle(Theme.accent)
            Text(Dates.format(date, "d")).font(.title2.bold())
            Text(Dates.format(date, "MMM")).font(.caption2).foregroundStyle(.secondary)
        }
        .frame(width: 48)
        .padding(.vertical, 6)
        .background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 10))
    }
}

struct ErrorBanner: View {
    let message: String
    var retry: (() -> Void)?
    var body: some View {
        HStack {
            Image(systemName: "exclamationmark.triangle")
            Text(message).font(.footnote)
            Spacer()
            if let retry { Button("Retry", action: retry).font(.footnote.bold()) }
        }
        .padding(12)
        .background(.red.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
    }
}
