import Foundation

// MARK: - Models (mirror the server's JSON; keys are converted from snake_case)

struct SignInResponse: Decodable {
    let token: String
    let mustChangePassword: Bool
}

struct Me: Decodable, Equatable {
    let id: String
    let name: String
    let badge: String
    let email: String
    let tier: String
    let role: String
    let department: String
    let tenureYears: String
    let mustChangePassword: Bool

    var isSupervisor: Bool { tier == "supervisor" || tier == "admin" }
}

struct Banner: Decodable, Equatable {
    let text: String
}

struct Shift: Decodable, Identifiable, Equatable {
    let eventId: String
    let event: String
    let date: String // YYYY-MM-DD, venue calendar
    let start: String
    let end: String
    let timeLabel: String
    let startsAt: String
    let endsAt: String
    let area: String
    let status: String // "rostered" or "worked"
    let notes: String

    var id: String { eventId }
    var day: Date { Dates.day(date) }
}

struct Home: Decodable {
    let name: String
    let banner: Banner?
    let upcoming: [Shift]
}

struct MonthSummary: Decodable, Identifiable {
    let month: String
    let status: String
    let events: Int
    var id: String { month }
}

struct WindowView: Decodable, Identifiable, Equatable {
    let id: String
    let start: String
    let end: String
    let label: String
}

struct Selection: Codable, Equatable {
    var status: String // "" (unanswered), not_available, all_shifts, window
    var windowId: String?
}

struct EventCard: Decodable, Identifiable, Equatable {
    let id: String
    let name: String
    let date: String
    let windows: [WindowView]
    var selection: Selection
}

struct Rate: Decodable, Equatable {
    let available: Int
    let events: Int
    let percent: Double
}

struct MonthView: Decodable {
    let month: String
    let status: String
    let expectationPercent: Double
    var rate: Rate
    let editable: Bool
    var events: [EventCard]?
}

struct AvailabilityUpdate: Decodable {
    let event: EventCard
    let rate: Rate
}

// MARK: - Client

struct APIError: LocalizedError {
    let status: Int
    let code: String
    let message: String
    var errorDescription: String? { message }
}

/// Talks to the LSM server. Every request carries the session token; the
/// server's error envelope becomes an APIError.
struct APIClient {
    var baseURL: URL
    var token: String?

    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }()

    private static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.keyEncodingStrategy = .convertToSnakeCase
        return e
    }()

    func send<T: Decodable>(_ method: String, _ path: String, body: (any Encodable)? = nil, as: T.Type = T.self) async throws -> T {
        var req = URLRequest(url: baseURL.appending(path: "api/v1" + path), timeoutInterval: 15)
        req.httpMethod = method
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try Self.encoder.encode(body)
        }
        return try Self.decoder.decode(T.self, from: await perform(req))
    }

    func get<T: Decodable>(_ path: String, query: [String: String] = [:], as: T.Type = T.self) async throws -> T {
        let items = query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
        var url = baseURL.appending(path: "api/v1" + path)
        if !items.isEmpty { url.append(queryItems: items) }
        var req = URLRequest(url: url, timeoutInterval: 15)
        req.httpMethod = "GET"
        return try Self.decoder.decode(T.self, from: await perform(req))
    }

    private struct Envelope: Decodable {
        struct Detail: Decodable { let code: String; let message: String }
        let error: Detail
    }

    private func perform(_ request: URLRequest) async throws -> Data {
        var req = request
        if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        let (data, response) = try await URLSession.shared.data(for: req)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            if let e = try? Self.decoder.decode(Envelope.self, from: data) {
                throw APIError(status: status, code: e.error.code, message: e.error.message)
            }
            throw APIError(status: status, code: "http_\(status)", message: "The server answered \(status).")
        }
        return data
    }
}

// MARK: - Dates

/// Server dates are venue-calendar days ("2026-10-03"). They are handled
/// as noon UTC on that day so no time zone can move them across midnight.
enum Dates {
    static let utc: Calendar = {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }()

    static func day(_ s: String) -> Date {
        let p = s.split(separator: "-").compactMap { Int($0) }
        guard p.count == 3 else { return .distantPast }
        return utc.date(from: DateComponents(year: p[0], month: p[1], day: p[2], hour: 12)) ?? .distantPast
    }

    static func string(_ d: Date) -> String {
        let c = utc.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d-%02d-%02d", c.year!, c.month!, c.day!)
    }

    /// Today on the device's calendar, as a server day.
    static func today() -> Date {
        let c = Calendar.current.dateComponents([.year, .month, .day], from: .now)
        return utc.date(from: DateComponents(year: c.year, month: c.month, day: c.day, hour: 12))!
    }

    static func month(_ d: Date) -> String { String(string(d).prefix(7)) }

    static func firstOfMonth(_ month: String) -> Date { day(month + "-01") }

    static func addMonths(_ month: String, _ n: Int) -> String {
        self.month(utc.date(byAdding: .month, value: n, to: firstOfMonth(month))!)
    }

    static func format(_ d: Date, _ template: String) -> String {
        let f = DateFormatter()
        f.timeZone = TimeZone(identifier: "UTC")
        f.setLocalizedDateFormatFromTemplate(template)
        return f.string(from: d)
    }

    static func instant(_ s: String) -> Date? {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.date(from: s) ?? ISO8601DateFormatter().date(from: s)
    }

    /// "today", "tomorrow", "in 12 days".
    static func relative(_ d: Date) -> String {
        let n = utc.dateComponents([.day], from: today(), to: d).day ?? 0
        switch n {
        case 0: return "today"
        case 1: return "tomorrow"
        case ..<0: return n == -1 ? "yesterday" : "\(-n) days ago"
        default: return "in \(n) days"
        }
    }
}
