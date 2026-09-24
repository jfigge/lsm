import SwiftUI

struct AvailabilityView: View {
    enum Sub: String, CaseIterable { case events = "Events", exceptions = "Exceptions", general = "General" }

    @State private var sub: Sub = .events

    var body: some View {
        VStack(spacing: 0) {
            Picker("Section", selection: $sub) {
                ForEach(Sub.allCases, id: \.self) { Text($0.rawValue) }
            }
            .pickerStyle(.segmented)
            .padding([.horizontal, .top])
            switch sub {
            case .events: EventsAvailability()
            case .exceptions: ComingSoon(what: "Dates you can never work, such as a standing commitment.")
            case .general: ComingSoon(what: "Your usual availability by day of the week.")
            }
        }
        .background(Color(.systemGroupedBackground))
    }
}

/// One card per event in an opened month, each with a menu offering Not
/// Available, All Shifts, or a named shift window. A choice is saved the
/// moment it is made.
struct EventsAvailability: View {
    @Environment(Session.self) private var session
    @State private var months: [MonthSummary] = []
    @State private var month: String?
    @State private var view: MonthView?
    @State private var saving: Set<String> = []
    @State private var error: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                if let error { ErrorBanner(message: error) { Task { await loadMonths() } } }
                if months.isEmpty && error == nil {
                    Text("No months are open for signup yet.").foregroundStyle(.secondary)
                }
                if let month {
                    monthHeader(month)
                    if let view {
                        policyCard(view)
                        ForEach(view.events ?? []) { card in eventCard(card, editable: view.editable) }
                        if (view.events ?? []).isEmpty {
                            Text("No events this month.").foregroundStyle(.secondary)
                        }
                    } else {
                        ProgressView().frame(maxWidth: .infinity)
                    }
                }
            }
            .padding()
        }
        .task { await loadMonths() }
        .task(id: month) { await loadMonth() }
        .refreshable { await loadMonth() }
    }

    private func monthHeader(_ m: String) -> some View {
        let idx = months.firstIndex { $0.month == m } ?? 0
        return HStack {
            Button { month = months[idx - 1].month } label: { Image(systemName: "chevron.left") }
                .disabled(idx == 0)
                .accessibilityLabel("Previous month")
            VStack {
                Text(Dates.format(Dates.firstOfMonth(m), "MMMM yyyy")).font(.title3.bold())
                if let s = months.first(where: { $0.month == m })?.status {
                    Text(s == "open" ? "Open for signup" : "Closed").font(.caption)
                        .foregroundStyle(s == "open" ? .green : .secondary)
                }
            }
            .frame(maxWidth: .infinity)
            Button { month = months[idx + 1].month } label: { Image(systemName: "chevron.right") }
                .disabled(idx >= months.count - 1)
                .accessibilityLabel("Next month")
        }
    }

    /// The published policy and where the person stands against it.
    private func policyCard(_ v: MonthView) -> some View {
        let meets = v.rate.percent >= v.expectationPercent
        return VStack(alignment: .leading, spacing: 8) {
            Text("Staff are expected to sign up for at least \(Int(v.expectationPercent))% of events each month.")
                .font(.subheadline)
            HStack(alignment: .firstTextBaseline) {
                Text("\(Int(v.rate.percent.rounded()))%").font(.title.bold())
                    .foregroundStyle(meets ? .green : .orange)
                Text("this month — \(v.rate.available) of \(v.rate.events) events").font(.subheadline).foregroundStyle(.secondary)
            }
            ProgressView(value: min(v.rate.percent, 100), total: 100)
                .tint(meets ? .green : .orange)
        }
        .padding()
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }

    private func eventCard(_ card: EventCard, editable: Bool) -> some View {
        HStack(spacing: 12) {
            DateBlock(date: Dates.day(card.date))
            VStack(alignment: .leading, spacing: 6) {
                Text(card.name).font(.subheadline.bold()).lineLimit(2)
                if editable {
                    Menu {
                        Button("Not Available") { choose(card, Selection(status: "not_available", windowId: nil)) }
                        Button("All Shifts") { choose(card, Selection(status: "all_shifts", windowId: nil)) }
                        Section("Shift window") {
                            ForEach(card.windows) { w in
                                Button(w.label) { choose(card, Selection(status: "window", windowId: w.id)) }
                            }
                        }
                    } label: {
                        HStack {
                            Text(label(card))
                            Image(systemName: "chevron.up.chevron.down").font(.caption)
                            if saving.contains(card.id) { ProgressView().controlSize(.small) }
                        }
                        .font(.subheadline)
                        .padding(.horizontal, 10).padding(.vertical, 6)
                        .background(.fill.tertiary, in: RoundedRectangle(cornerRadius: 8))
                    }
                    .disabled(saving.contains(card.id))
                    .accessibilityLabel("Availability for \(card.name): \(label(card))")
                } else {
                    Text(label(card)).font(.subheadline).foregroundStyle(.secondary)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(12)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }

    private func label(_ card: EventCard) -> String {
        switch card.selection.status {
        case "not_available": return "Not Available"
        case "all_shifts": return "All Shifts"
        case "window": return card.windows.first { $0.id == card.selection.windowId }?.label ?? "One window"
        default: return "Choose…"
        }
    }

    /// Writes through immediately; on failure the card shows the server's
    /// last known answer again.
    private func choose(_ card: EventCard, _ sel: Selection) {
        saving.insert(card.id)
        Task {
            do {
                let res: AvailabilityUpdate = try await session.api.send("PUT", "/events/\(card.id)/availability", body: sel)
                if let i = view?.events?.firstIndex(where: { $0.id == card.id }) {
                    view?.events?[i] = res.event
                }
                view?.rate = res.rate
                error = nil
            } catch {
                self.error = session.handle(error)
            }
            saving.remove(card.id)
        }
    }

    private func loadMonths() async {
        do {
            months = try await session.api.get("/months")
            if month == nil {
                month = months.first { $0.status == "open" }?.month ?? months.last?.month
            }
            error = nil
        } catch {
            self.error = session.handle(error)
        }
    }

    private func loadMonth() async {
        guard let month else { return }
        view = nil
        do {
            view = try await session.api.get("/availability/\(month)")
            error = nil
        } catch {
            self.error = session.handle(error)
        }
    }
}
