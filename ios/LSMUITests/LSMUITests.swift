import XCTest

/// Walks the app against a running server: sign in, change the starting
/// password if asked, then visit every tab. Screenshots go to
/// $LSM_SHOTS when set (the Simulator shares the Mac's file system).
///
///     TEST_RUNNER_LSM_SERVER=http://127.0.0.1:8080 \
///     TEST_RUNNER_LSM_EMAIL=… TEST_RUNNER_LSM_PASSWORD=… \
///     TEST_RUNNER_LSM_NEW_PASSWORD=… xcodebuild test …
final class LSMUITests: XCTestCase {
    private let env = ProcessInfo.processInfo.environment

    override func setUp() {
        continueAfterFailure = false
    }

    func testSignInAndTabs() throws {
        guard let server = env["LSM_SERVER"], let email = env["LSM_EMAIL"], let password = env["LSM_PASSWORD"] else {
            throw XCTSkip("set LSM_SERVER, LSM_EMAIL and LSM_PASSWORD to run against a server")
        }
        let app = XCUIApplication()
        app.launchArguments += ["-serverURL", server, "--reset-session"]
        app.launch()

        let emailField = app.textFields["Email"]
        if !emailField.waitForExistence(timeout: 5) {
            shot("00-unexpected-launch")
            XCTFail("no sign-in screen:\n\(app.debugDescription)")
        }
        shot("01-signin")
        emailField.tap()
        emailField.typeText(email)
        let pw = app.secureTextFields["Password"]
        pw.tap()
        pw.typeText(password)
        app.buttons["Sign in"].tap()

        let current = app.secureTextFields["Current password"]
        if current.waitForExistence(timeout: 5) {
            shot("02-change-password")
            let next = try XCTUnwrap(env["LSM_NEW_PASSWORD"], "account must change its password: set LSM_NEW_PASSWORD")
            // Password AutoFill overlays can make the fields report "not
            // hittable"; tapping their centre works regardless.
            sleep(1) // let the navigation transition settle
            for (label, text) in [("Current password", password), ("New password", next), ("New password again", next)] {
                let field = app.secureTextFields[label]
                for _ in 0..<3 where !(field.value(forKey: "hasKeyboardFocus") as? Bool ?? false) {
                    field.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
                    sleep(1)
                }
                field.typeText(text)
            }
            shot("02b-change-password-filled")
            app.buttons["Change password"].tap()
        }

        expect(app.staticTexts["Your upcoming shifts"], 10)
        sleep(2)
        shot("03-home")

        tab(app, "Schedule", until: app.staticTexts["My shifts"])
        sleep(2)
        shot("04-schedule")
        app.buttons["Next month"].tap()
        sleep(2)
        shot("04b-schedule-next-month")

        tab(app, "Availability", until: app.staticTexts["Open for signup"])
        sleep(1)
        shot("05-availability")

        // Change the first event's answer and see it write through.
        let menu = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Availability for'")).firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 5))
        menu.tap()
        app.buttons["All Shifts"].firstMatch.tap()
        expect(app.buttons.matching(NSPredicate(format: "label ENDSWITH ': All Shifts'")).firstMatch, 5)
        shot("06-availability-saved")

        tab(app, "Time", until: app.staticTexts["Coming soon"])
        app.buttons["Profile"].firstMatch.tap()
        expect(app.staticTexts["Badge"], 3)
        shot("07-profile")
    }

    /// Switches tab, retrying: a tap that lands while the tab bar is still
    /// animating is dropped.
    private func tab(_ app: XCUIApplication, _ name: String, until element: XCUIElement, line: UInt = #line) {
        for _ in 0..<3 {
            app.tabBars.buttons[name].tap()
            if element.waitForExistence(timeout: 4) { return }
        }
        expect(element, 1, line: line)
    }

    /// Waits for an element; on failure keeps a screenshot and the
    /// accessibility tree so the failure can be read without a rerun.
    private func expect(_ element: XCUIElement, _ timeout: TimeInterval, line: UInt = #line) {
        if element.waitForExistence(timeout: timeout) { return }
        shot("failure-line-\(line)")
        XCTFail("missing \(element):\n\(XCUIApplication().debugDescription)", line: line)
    }

    private func shot(_ name: String) {
        let png = XCUIScreen.main.screenshot().pngRepresentation
        let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = env["LSM_SHOTS"] {
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }
}
