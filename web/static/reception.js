// /reception: check-in with resource issue, and check-out with return
// (SPEC §3). One scan field that is always ready; typing a name instead
// looks the person up, for the badge that will not scan or was left at
// home (SPEC §7).

import { h, put, request, ApiError } from "./lib.js";

const TOKEN_KEY = "lsm.reception.token";
const view = document.getElementById("view");
const who = document.getElementById("who");
const eventPick = document.getElementById("eventpick");

let me = null;
let eventID = new URLSearchParams(location.search).get("event");
let desk = null; // the person currently on screen
let scan, results, panel;

async function api(method, path, body) {
  try {
    return await request(method, path, body, sessionStorage.getItem(TOKEN_KEY));
  } catch (e) {
    if (e.code === "unauthenticated") {
      sessionStorage.removeItem(TOKEN_KEY);
      renderSignIn();
    }
    throw e;
  }
}

let toastTimer;
function toast(msg, isError = false) {
  const t = document.getElementById("toast");
  t.textContent = msg;
  t.className = "show" + (isError ? " error" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.className = ""), isError ? 6000 : 3000);
}

function fail(e) {
  if (e instanceof ApiError && e.code === "unauthenticated") return;
  toast(e.message || "Something went wrong", true);
}

// ------------------------------------------------------------- sign-in --

function renderSignIn(message) {
  put(who);
  put(eventPick);
  const err = h("p", { class: "err" }, message || "");
  const form = h("form", { onsubmit: async (ev) => {
    ev.preventDefault();
    const fd = new FormData(form);
    try {
      const s = await request("POST", "/session", { username: fd.get("username"), password: fd.get("password") });
      sessionStorage.setItem(TOKEN_KEY, s.token);
      start();
    } catch (e) {
      err.textContent = e.message;
    }
  } },
    h("label", { class: "field" }, "Email", h("input", { name: "username", type: "email", autocomplete: "username", required: true })),
    h("label", { class: "field" }, "Password", h("input", { name: "password", type: "password", autocomplete: "current-password", required: true })),
    err,
    h("button", { class: "primary", type: "submit" }, "Sign in"));
  put(view, h("div", { class: "signin panel stack" }, h("h1", {}, "Reception"),
    h("p", { class: "muted small" }, "For supervisors and admins running the reception desk."), form));
  form.querySelector("input").focus();
}

async function start() {
  if (!sessionStorage.getItem(TOKEN_KEY)) return renderSignIn();
  try {
    me = await api("GET", "/me");
  } catch (e) {
    if (e.code === "password_change_required") {
      sessionStorage.removeItem(TOKEN_KEY);
      return renderSignIn("Change your starting password in the LSM app first, then sign in here.");
    }
    return fail(e);
  }
  if (me.tier !== "supervisor" && me.tier !== "admin") {
    sessionStorage.removeItem(TOKEN_KEY);
    return renderSignIn("Reception is for supervisors and admins.");
  }
  put(who, h("span", {}, me.name), h("button", { class: "link", onclick: signOut }, "Sign out"));
  await pickEvent();
}

async function signOut() {
  await api("DELETE", "/session").catch(() => {});
  sessionStorage.removeItem(TOKEN_KEY);
  renderSignIn();
}

// Today's event by default; any nearby event can be chosen (a demo, or
// reconciling the night after). The choice lives in the URL.
async function pickEvent() {
  const ev = await api("GET", "/reception/events");
  const all = [...ev.today, ...ev.nearby.filter((n) => !ev.today.some((t) => t.id === n.id))];
  if (!eventID || !all.some((e) => e.id === eventID)) eventID = (ev.today[0] || {}).id || null;
  const sel = h("select", { "aria-label": "Event" },
    !eventID ? h("option", { value: "" }, "Choose an event…") : null,
    ev.today.length ? h("optgroup", { label: "Today" }, ev.today.map((e) => opt(e))) : null,
    h("optgroup", { label: "Nearby" }, ev.nearby.filter((n) => !ev.today.some((t) => t.id === n.id)).map((e) => opt(e))));
  const link = document.getElementById("reportlink");
  const syncLink = () => (link.href = "/unreturned" + (eventID ? "?event=" + eventID : ""));
  sel.onchange = () => {
    eventID = sel.value;
    history.replaceState(null, "", "?event=" + eventID);
    desk = null;
    syncLink();
    renderDesk();
  };
  syncLink();
  put(eventPick, sel);
  renderDesk();
}

function opt(e) {
  return h("option", { value: e.id, selected: e.id === eventID }, `${e.date} · ${e.name}`);
}

// ---------------------------------------------------------------- desk --

function renderDesk() {
  if (!eventID) {
    put(view, h("div", { class: "empty" }, h("p", {}, "No event today. Choose one above.")));
    return;
  }
  scan = h("input", {
    class: "scan", autocomplete: "off", spellcheck: "false", placeholder: "Scan badge or type a name",
    "aria-label": "Scan badge or type a name",
  });
  results = h("div", { class: "results", role: "listbox" });
  panel = h("section", { class: "panel stack", "aria-live": "polite" });

  let searchTimer;
  scan.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") {
      ev.preventDefault();
      const v = scan.value.trim();
      if (!v) return;
      const first = results.querySelector("button");
      if (looksLikeBadge(v)) {
        scan.value = "";
        put(results);
        lookupBadge(v);
      } else if (first) {
        first.click();
      }
    } else if (ev.key === "ArrowDown") {
      results.querySelector("button")?.focus();
    }
  });
  scan.addEventListener("input", () => {
    clearTimeout(searchTimer);
    const v = scan.value.trim();
    if (looksLikeBadge(v) || v.length < 2) return put(results);
    searchTimer = setTimeout(() => search(v), 180);
  });

  put(view, h("div", { class: "desk" },
    h("div", { class: "scanbox" }, scan, results,
      h("p", { class: "muted small" }, "Scan a badge, or type part of a name and pick the person.")),
    panel));
  showDesk();
  scan.focus();
}

// A scanner types the badge in one burst: no spaces, at least one digit.
// Anything with a space or no digits is treated as a name.
function looksLikeBadge(v) {
  return /^\S+$/.test(v) && /\d/.test(v);
}

async function lookupBadge(badge) {
  try {
    desk = await api("GET", `/events/${eventID}/scan/${encodeURIComponent(badge)}`);
    showDesk();
  } catch (e) {
    if (e.code === "badge_unknown") {
      desk = null;
      put(panel, h("div", { class: "stack" },
        h("h2", {}, "Badge not recognised"),
        h("p", {}, `No one has badge “${badge}”. Type their name on the left to find them.`)));
    } else fail(e);
  }
  scan.focus();
}

async function search(q) {
  try {
    const people = await api("GET", "/reception/search?q=" + encodeURIComponent(q));
    put(results, people.length ? people.map((p) => h("button", {
      role: "option",
      onclick: async () => {
        scan.value = "";
        put(results);
        await load(p.id);
        scan.focus();
      },
      onkeydown: (ev) => {
        if (ev.key === "ArrowDown") ev.currentTarget.nextElementSibling?.focus();
        if (ev.key === "ArrowUp") (ev.currentTarget.previousElementSibling || scan).focus();
      },
    }, h("span", {}, p.name), h("span", { class: "muted small" }, `${p.badge} · ${p.department}`)))
      : h("p", { class: "muted small" }, "No one matches."));
  } catch (e) { fail(e); }
}

async function load(personID) {
  try {
    desk = (await api("GET", `/events/${eventID}/desk/${personID}`)).desk;
    showDesk();
  } catch (e) { fail(e); }
}

// act performs a desk action and redraws with the returned state.
async function act(path, body) {
  try {
    const r = await api("POST", `/events/${eventID}/desk/${desk.person.id}${path}`, body);
    desk = r.desk;
    showDesk();
    if (r.warning) toast(r.warning, true);
    return true;
  } catch (e) {
    fail(e);
    return false;
  } finally {
    scan.focus();
  }
}

function showDesk() {
  if (!desk) {
    put(panel, h("div", { class: "empty" }, h("div", {},
      h("p", { style: "font-size:1.3rem;margin:0" }, "Ready"),
      h("p", {}, "Scan a badge to check someone in or out."))));
    return;
  }
  const d = desk;
  const v = d.visit;
  const out = d.issued.filter((x) => !x.returned_at);

  const status = !v
    ? h("div", { class: "row" }, h("span", { class: "status" }, "Not checked in"),
        h("button", { class: "primary", onclick: () => act("/checkin") }, "Check in"))
    : v.checked_out
      ? h("div", { class: "status" }, "Checked in at ", h("b", {}, v.checked_in), " · checked out at ", h("b", {}, v.checked_out))
      : h("div", { class: "status" }, "Checked in at ", h("b", {}, v.checked_in),
          h("span", { class: "muted" }, v.in_location === "kiosk" ? ` at the kiosk${v.in_station ? " (" + v.in_station + ")" : ""}` : " at reception"));

  const post = d.post
    ? h("p", { style: "margin:0" }, h("b", {}, d.post.name), d.post.area && !d.post.name.startsWith(d.post.area) ? ` · ${d.post.area}` : "")
    : null;

  const required = d.fields.filter((f) => f.required);
  const others = d.fields.filter((f) => !f.required);
  const otherBox = h("div");
  const otherSel = h("select", { "aria-label": "Issue something else" },
    h("option", { value: "" }, "Issue something else…"), others.map((f) => h("option", { value: f.id }, f.name)));
  otherSel.onchange = () => put(otherBox, otherSel.value ? picker(others.find((f) => f.id === otherSel.value), d) : null);

  const checkedOut = v && v.checked_out;
  const checkout = v && !checkedOut ? checkoutButton(out) : null;

  put(panel,
    h("div", { class: "person" },
      d.person.photo ? h("img", { class: "avatar", src: d.person.photo, alt: "" }) : h("span", { class: "avatar", "aria-hidden": "true" }, d.person.initials),
      h("div", {}, h("h1", {}, d.person.name), h("div", { class: "muted" }, `${d.person.badge} · ${d.person.department}`))),
    d.banner ? h("div", { class: "banner" }, d.banner.text) : null,
    d.notices.length ? h("div", { class: "row" }, d.notices.map((n) => h("span", { class: "pill warn" }, n))) : null,
    post,
    status,
    checkedOut ? null : [
      required.map((f) => requiredField(f, d, out)),
      h("div", { class: "field" }, otherSel, otherBox),
    ],
    d.issued.length ? h("div", { class: "field issued" }, h("h3", {}, "Issued"), h("ul", {}, d.issued.map(issuedRow))) : null,
    checkout);
}

// A required resource shows as done once it is out with the person; a
// second one stays possible behind "issue another".
function requiredField(f, d, out) {
  const held = out.filter((x) => x.resource_id === f.id);
  if (!held.length) {
    return h("div", { class: "field" }, h("h3", {}, f.name, h("span", { class: "pill info" }, "required for this post")), picker(f, d));
  }
  const more = h("div");
  const toggle = h("button", { class: "link", onclick: () => { put(more, picker(f, d)); toggle.remove(); } }, "issue another");
  return h("div", { class: "field" },
    h("h3", {}, f.name, h("span", { class: "pill open" }, "issued: " + held.map((x) => `${x.resource} ${x.number}`.trim()).join(", ")), toggle),
    more);
}

// picker issues one resource. Tracked resources (radios) offer the first
// available number, a specific one, or a free-typed number; nothing is
// ever refused.
function picker(f, d) {
  const verb = d.visit ? "Issue" : "Check in & issue";
  if (!f.tracked) {
    return h("div", { class: "picker" },
      h("button", { class: "primary", onclick: () => act("/issues", { resource_id: f.id }) }, `${verb} ${f.name}`));
  }
  const first = f.available[0];
  const pick = h("select", { "aria-label": `${f.name} number` },
    h("option", { value: "" }, f.available.length ? `Pick a number (${f.available.length} free)` : "None free on the rack"),
    f.available.map((n) => h("option", { value: n }, n)));
  pick.onchange = () => pick.value && act("/issues", { resource_id: f.id, number: pick.value });
  const typed = h("input", { placeholder: "Number", "aria-label": `Type a ${f.name} number`, autocomplete: "off" });
  const issueTyped = () => typed.value.trim() && act("/issues", { resource_id: f.id, number: typed.value.trim() });
  typed.onkeydown = (ev) => { if (ev.key === "Enter") { ev.preventDefault(); issueTyped(); } };
  return h("div", { class: "picker" },
    h("button", { class: "primary", disabled: !first, onclick: () => act("/issues", { resource_id: f.id, first_available: true }) },
      first ? `${verb} ${f.name} ${first}` : "None free"),
    pick,
    h("span", { class: "muted small" }, "or"),
    typed,
    h("button", { onclick: issueTyped }, verb));
}

function issuedRow(x) {
  const label = `${x.resource}${x.number ? " " + x.number : ""}`;
  return h("li", { class: x.returned_at ? "returned" : "" },
    h("span", {}, h("span", { class: "what" }, label), !x.registered && x.number ? h("span", { class: "pill warn", style: "margin-left:6px" }, "not on register") : null,
      h("div", { class: "muted small" }, `issued ${x.issued}${x.issued_by ? " by " + x.issued_by : ""}${x.returned ? " · returned " + x.returned : ""}`)),
    x.returned_at ? h("span", { class: "muted small" }, "Returned")
      : h("button", { onclick: () => act(`/issues/${x.id}/return`) }, "Return"));
}

// Check-out shows what is still out so return can be confirmed; it never
// refuses — the end-of-night report catches anything missed.
function checkoutButton(out) {
  const btn = h("button", { class: out.length ? "" : "primary" }, "Check out");
  btn.onclick = () => {
    if (!out.length) return act("/checkout");
    const list = out.map((x) => `${x.resource}${x.number ? " " + x.number : ""}`).join(", ");
    const box = h("div", { class: "confirm" },
      h("span", {}, `Not returned: ${list}.`),
      h("button", { class: "primary", onclick: async () => {
        for (const x of out) if (!(await act(`/issues/${x.id}/return`))) return;
        act("/checkout");
      } }, "Return all & check out"),
      h("button", { onclick: () => act("/checkout") }, "Check out anyway"),
      h("button", { onclick: () => box.replaceWith(btn) }, "Cancel"));
    btn.replaceWith(box);
  };
  return h("div", { class: "field" }, btn);
}

start();
