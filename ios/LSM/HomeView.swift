import SwiftUI

struct HomeView: View {
    @Environment(Session.self) private var session
    @State private var home: Home?
    @State private var error: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let error { ErrorBanner(message: error) { Task { await load() } } }

                // Welcome and the current banner.
                VStack(alignment: .leading, spacing: 8) {
                    Text("Welcome back,").foregroundStyle(.secondary)
                    Text(home?.name ?? session.me?.name ?? "").font(.title.bold())
                    if let banner = home?.banner {
                        Label(banner.text, systemImage: "megaphone")
                            .font(.subheadline.weight(.semibold))
                            .padding(12)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Theme.accent.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
                            .foregroundStyle(.primary)
                    }
                }
                .padding()
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))

                Text("Your upcoming shifts").font(.headline)
                if let home {
                    if home.upcoming.isEmpty {
                        Text("No shifts on your roster yet. Once a month's roster is confirmed, your shifts appear here.")
                            .font(.subheadline).foregroundStyle(.secondary)
                            .padding()
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
                    }
                    ForEach(home.upcoming) { ShiftRow(shift: $0) }
                } else if error == nil {
                    ProgressView().frame(maxWidth: .infinity)
                }

                // Placeholder until training records exist (SPEC §10.1a).
                VStack(alignment: .leading, spacing: 6) {
                    Label("Training", systemImage: "graduationcap").font(.headline)
                    HStack {
                        Text("Crowd management refresher")
                        Spacer()
                        Text("Expires soon").font(.caption.bold())
                            .padding(.horizontal, 8).padding(.vertical, 3)
                            .background(.orange.opacity(0.2), in: Capsule())
                    }
                    .font(.subheadline)
                    Text("Sample record: training tracking is not built yet.").font(.caption).foregroundStyle(.secondary)
                }
                .padding()
                .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
            }
            .padding()
        }
        .background(Color(.systemGroupedBackground))
        .refreshable { await load() }
        .task { await load() }
    }

    private func load() async {
        do {
            home = try await session.api.get("/me/home")
            error = nil
        } catch {
            self.error = session.handle(error)
        }
    }
}

struct ShiftRow: View {
    let shift: Shift

    var body: some View {
        HStack(spacing: 14) {
            DateBlock(date: shift.day)
            VStack(alignment: .leading, spacing: 3) {
                Text(shift.event).font(.subheadline.bold()).lineLimit(2)
                Text(shift.timeLabel).font(.subheadline)
                Text(shift.area).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            Text(Dates.relative(shift.day)).font(.caption).foregroundStyle(.secondary)
        }
        .padding(12)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }
}
