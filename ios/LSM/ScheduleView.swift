import EventKit
import SwiftUI

struct ScheduleView: View {
    @Environment(Session.self) private var session
    @State private var month = Dates.month(Dates.today())
    @State private var asList = false
    @State private var shifts: [Shift] = []
    @State private var selectedDay: String?
    @State private var expanded: Set<String> = []
    @State private var error: String?
    @State private var calendarMessage: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                header
                if let error { ErrorBanner(message: error) { Task { await load() } } }
                if !asList { MonthGrid(month: month, shiftDays: Set(shifts.map(\.date)), selected: $selectedDay) }

                HStack {
                    Text("My shifts").font(.headline)
                    Spacer()
                    if selectedDay != nil && !asList {
                        Button("Show all") { selectedDay = nil }.font(.footnote)
                    }
                }
                let visible = shifts.filter { asList || selectedDay == nil || $0.date == selectedDay }
                if visible.isEmpty {
                    Text(shifts.isEmpty ? "No shifts this month." : "No shift that day.")
                        .font(.subheadline).foregroundStyle(.secondary)
                }
                ForEach(visible) { shift in
                    shiftCard(shift)
                }

                notes
            }
            .padding()
        }
        .background(Color(.systemGroupedBackground))
        .task(id: month) { await load() }
        .refreshable { await load() }
        .alert("Calendar", isPresented: Binding(get: { calendarMessage != nil }, set: { if !$0 { calendarMessage = nil } })) {
            Button("OK") {}
        } message: {
            Text(calendarMessage ?? "")
        }
    }

    private var header: some View {
        HStack {
            Button { month = Dates.addMonths(month, -1); selectedDay = nil } label: { Image(systemName: "chevron.left") }
                .accessibilityLabel("Previous month")
            Text(Dates.format(Dates.firstOfMonth(month), "MMMM yyyy")).font(.title3.bold()).frame(maxWidth: .infinity)
            Button { month = Dates.addMonths(month, 1); selectedDay = nil } label: { Image(systemName: "chevron.right") }
                .accessibilityLabel("Next month")
            Picker("View", selection: $asList) {
                Image(systemName: "calendar").tag(false).accessibilityLabel("Calendar")
                Image(systemName: "list.bullet").tag(true).accessibilityLabel("List")
            }
            .pickerStyle(.segmented)
            .frame(width: 96)
        }
    }

    private func shiftCard(_ shift: Shift) -> some View {
        let open = Binding(
            get: { expanded.contains(shift.id) },
            set: { if $0 { expanded.insert(shift.id) } else { expanded.remove(shift.id) } })
        return DisclosureGroup(isExpanded: open) {
            VStack(alignment: .leading, spacing: 8) {
                LabeledContent("Time", value: shift.timeLabel)
                LabeledContent("Area", value: shift.area)
                LabeledContent("Status", value: shift.status == "worked" ? "Worked" : "On the roster")
                if !shift.notes.isEmpty { Text(shift.notes).font(.footnote) }
                if shift.day >= Dates.today() {
                    Button { Task { await addToCalendar(shift) } } label: {
                        Label("Add to Calendar", systemImage: "calendar.badge.plus")
                    }
                    .buttonStyle(.bordered)
                }
            }
            .font(.subheadline)
            .padding(.top, 8)
        } label: {
            HStack(spacing: 12) {
                DateBlock(date: shift.day)
                VStack(alignment: .leading, spacing: 2) {
                    Text(shift.event).font(.subheadline.bold()).foregroundStyle(.primary).lineLimit(2)
                    Text(shift.timeLabel).font(.caption).foregroundStyle(.secondary)
                }
            }
        }
        .padding(12)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }

    private var notes: some View {
        let withNotes = shifts.filter { !$0.notes.isEmpty && $0.day >= Dates.today() }
        return VStack(alignment: .leading, spacing: 6) {
            Label("Department notes", systemImage: "note.text").font(.headline)
            if withNotes.isEmpty {
                Text("No notes from your department for your upcoming shifts.")
                    .font(.subheadline).foregroundStyle(.secondary)
            }
            ForEach(withNotes) { s in
                Text("\(Dates.format(s.day, "MMM d")): \(s.notes)").font(.subheadline)
            }
        }
        .padding()
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }

    private func load() async {
        do {
            shifts = try await session.api.get("/me/shifts", query: ["month": month])
            error = nil
        } catch {
            shifts = []
            self.error = session.handle(error)
        }
    }

    /// Writes one event to the default calendar. Write-only access: the
    /// app never reads the person's calendar.
    private func addToCalendar(_ shift: Shift) async {
        let store = EKEventStore()
        do {
            guard try await store.requestWriteOnlyAccessToEvents() else {
                calendarMessage = "Allow LSM to add events in Settings › Privacy › Calendars."
                return
            }
            guard let start = Dates.instant(shift.startsAt), let end = Dates.instant(shift.endsAt) else { return }
            let event = EKEvent(eventStore: store)
            event.title = "Work: \(shift.event)"
            event.startDate = start
            event.endDate = end
            event.location = "Lenovo Center"
            event.notes = [shift.area, shift.notes].filter { !$0.isEmpty }.joined(separator: "\n")
            event.calendar = store.defaultCalendarForNewEvents
            try store.save(event, span: .thisEvent)
            calendarMessage = "Added \(shift.event) to your calendar."
        } catch {
            calendarMessage = error.localizedDescription
        }
    }
}

/// A month calendar with the days holding a shift marked.
struct MonthGrid: View {
    let month: String
    let shiftDays: Set<String>
    @Binding var selected: String?

    var body: some View {
        let first = Dates.firstOfMonth(month)
        let cal = Dates.utc
        let days = cal.range(of: .day, in: .month, for: first)?.count ?? 30
        let lead = (cal.component(.weekday, from: first) - cal.firstWeekday + 7) % 7
        let symbols = cal.veryShortWeekdaySymbols
        let ordered = Array(symbols[(cal.firstWeekday - 1)...] + symbols[..<(cal.firstWeekday - 1)])
        let today = Dates.string(Dates.today())

        // One ForEach over uniquely keyed cells: separate ForEach blocks in
        // one grid would reuse the ids 0…6 and drop cells.
        let cells: [(id: String, day: Int?, heading: String?)] =
            ordered.enumerated().map { ("h\($0.offset)", nil, $0.element) }
            + (0..<lead).map { ("b\($0)", nil, nil) }
            + (1...days).map { ("d\($0)", $0, nil) }

        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 4), count: 7), spacing: 6) {
            ForEach(cells, id: \.id) { cell in
                if let heading = cell.heading {
                    Text(heading).font(.caption2.bold()).foregroundStyle(.secondary)
                } else if let d = cell.day {
                    dayCell(d, today: today)
                } else {
                    Color.clear.frame(height: 40)
                }
            }
        }
        .padding(10)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }

    private func dayCell(_ d: Int, today: String) -> some View {
        let key = "\(month)-\(String(format: "%02d", d))"
        let has = shiftDays.contains(key)
        return Button {
            selected = has ? (selected == key ? nil : key) : nil
        } label: {
            VStack(spacing: 3) {
                Text("\(d)")
                    .font(.subheadline.weight(key == today ? .bold : .regular))
                    .foregroundStyle(key == today ? Theme.accent : .primary)
                Circle().fill(has ? Theme.accent : .clear).frame(width: 6, height: 6)
            }
            .frame(maxWidth: .infinity, minHeight: 40)
            .background(selected == key ? Theme.accent.opacity(0.15) : .clear, in: RoundedRectangle(cornerRadius: 8))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(d)\(has ? ", shift" : "")")
    }
}
