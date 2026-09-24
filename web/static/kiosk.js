// /kiosk: a badge scanner at a non-reception entrance (SPEC §3).
//
// One text field with permanent focus; the scanner types the badge and
// Enter. The screen shows who, the time and the one thing that happened,
// then resets. No mouse. The kiosk holds a station token, paired once by
// an admin, that can scan badges and nothing else.

import { h, put, request, ApiError } from "./lib.js";

const STATION_KEY = "lsm.kiosk.station";
const RESET_MS = { ok: 2200, attn: 3500, bad: 3500 };
const view = document.getElementById("view");

let station = localStorage.getItem(STATION_KEY);
let event = null;
let resetTimer;

const params = new URLSearchParams(location.search);

function kioskApi(method, path, body) {
  return request(method, path, body, station);
}

// ------------------------------------------------------------ pairing --

function renderSetup(message) {
  const err = h("p", { class: "err" }, message || "");
  const form = h("form", { class: "setup", onsubmit: async (ev) => {
    ev.preventDefault();
    err.textContent = "";
    const fd = new FormData(form);
    try {
      const s = await request("POST", "/session", { username: fd.get("username"), password: fd.get("password") });
      const pair = await request("POST", "/stations", { name: fd.get("name") }, s.token);
      const events = await request("GET", "/reception/events", undefined, s.token).catch(() => null);
      await request("DELETE", "/session", undefined, s.token).catch(() => {});
      localStorage.setItem(STATION_KEY, pair.token);
      station = pair.token;
      if (events && !events.today.length && events.nearby.length) return renderPickEvent(events.nearby);
      start();
    } catch (e) {
      err.textContent = e.code === "forbidden" ? "Pairing needs an admin account." : e.message;
    }
  } },
    h("h1", {}, "Set up this kiosk"),
    h("p", { class: "hint" }, "An admin pairs the kiosk once. It then scans badges without anyone signed in."),
    h("label", {}, "Kiosk name", h("input", { name: "name", placeholder: "East entrance", required: true })),
    h("label", {}, "Admin email", h("input", { name: "username", type: "email", autocomplete: "username", required: true })),
    h("label", {}, "Admin password", h("input", { name: "password", type: "password", autocomplete: "current-password", required: true })),
    err,
    h("button", { class: "primary", type: "submit" }, "Pair kiosk"));
  put(view, form);
  form.querySelector("input").focus();
}

// With no event today (a demo, or setting up the day before), choose one;
// it is carried in the URL so a reload keeps it.
function renderPickEvent(events) {
  const sel = h("select", {}, events.map((e) => h("option", { value: e.id }, `${e.date} · ${e.name}`)));
  put(view, h("div", { class: "setup" },
    h("h1", {}, "No event today"),
    h("p", { class: "hint" }, "Choose the event this kiosk checks people in for."),
    sel,
    h("button", { class: "primary", onclick: () => { location.search = "?event=" + sel.value; } }, "Use this event")));
}

// ------------------------------------------------------------- running --

async function start() {
  if (!station) return renderSetup();
  let info;
  try {
    info = await kioskApi("GET", "/kiosk");
    if (params.get("event")) {
      event = await kioskApi("GET", "/kiosk/event/" + encodeURIComponent(params.get("event")));
    } else {
      event = info.today[0] || null;
    }
  } catch (e) {
    if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
      localStorage.removeItem(STATION_KEY);
      station = null;
      return renderSetup("This kiosk's pairing was revoked. Pair it again.");
    }
    return renderOffline(e);
  }
  if (!event) {
    put(view, h("div", { class: "stage" }, h("p", { class: "prompt" }, "No event today"),
      h("p", { class: "hint" }, info.station)));
    setTimeout(start, 60_000);
    return;
  }
  renderScanner(info.station);
}

function renderOffline(e) {
  put(view, h("div", { class: "stage" }, h("p", { class: "prompt" }, "Kiosk offline"),
    h("p", { class: "hint" }, `${e.message}. Retrying…`)));
  setTimeout(start, 5000);
}

let input, stage;

function renderScanner(stationName) {
  const clock = h("span", { class: "clock" });
  const tick = () => (clock.textContent = new Date().toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }));
  tick();
  setInterval(tick, 10_000);

  input = h("input", {
    class: "scan", autocomplete: "off", autocapitalize: "off", spellcheck: "false",
    "aria-label": "Badge", placeholder: "Badge number",
  });
  input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") {
      ev.preventDefault();
      const badge = input.value.trim();
      input.value = "";
      if (badge) scan(badge);
    }
  });
  // Permanent focus: whatever happens, the next keystrokes land here.
  input.addEventListener("blur", () => setTimeout(() => input.focus(), 0));
  document.addEventListener("pointerdown", () => setTimeout(() => input.focus(), 0));

  stage = h("div", { class: "stage", "aria-live": "assertive" });
  put(view,
    h("header", { class: "kbar" }, h("span", {}, h("b", {}, event.name), " · ", stationName), clock),
    stage);
  idle();
  input.focus();
}

function idle() {
  clearTimeout(resetTimer);
  put(stage, h("p", { class: "prompt" }, "Scan your badge"), input, h("p", { class: "hint" }, "Check in on arrival, check out when you leave"));
  input.focus();
}

async function scan(badge) {
  clearTimeout(resetTimer);
  let r;
  try {
    r = await kioskApi("POST", "/kiosk/scan", { event_id: event.id, badge });
  } catch (e) {
    if (e.status === 401 || e.status === 403) return start();
    r = { outcome: "error", heading: "Not recorded", message: "Can't reach the server — see reception", see_reception: true };
  }
  const tone = r.outcome === "unknown_badge" || r.outcome === "error" ? "bad" : r.see_reception ? "attn" : "ok";
  const who = r.person
    ? h("div", { class: "who" },
        r.person.photo ? h("img", { class: "avatar", src: r.person.photo, alt: "" }) : h("span", { class: "avatar", "aria-hidden": "true" }, r.person.initials),
        h("span", {}, r.person.name))
    : null;
  put(stage,
    h("div", { class: "result " + tone },
      h("h2", {}, r.heading),
      who,
      h("p", { class: "msg" }, r.message),
      r.time && !r.heading.includes(r.time) ? h("span", { class: "time" }, r.time) : null),
    input);
  input.focus();
  resetTimer = setTimeout(idle, RESET_MS[tone]);
}

start();
