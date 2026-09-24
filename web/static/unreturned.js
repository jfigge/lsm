// /unreturned: the end-of-night reconciliation (SPEC §3 Reports). Laid
// out the way the radio rack is checked — every number in rack order,
// the gaps named — then the list of who still has what.

import { h, put, request, ApiError } from "./lib.js";

// Shares the reception sign-in: the same people reconcile the rack.
const TOKEN_KEY = "lsm.reception.token";
const view = document.getElementById("view");
const who = document.getElementById("who");
const eventPick = document.getElementById("eventpick");
let eventID = new URLSearchParams(location.search).get("event");
let refreshTimer;

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
  toastTimer = setTimeout(() => (t.className = ""), 3000);
}

function renderSignIn(message) {
  clearInterval(refreshTimer);
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
    } catch (e) { err.textContent = e.message; }
  } },
    h("label", { class: "field" }, "Email", h("input", { name: "username", type: "email", autocomplete: "username", required: true })),
    h("label", { class: "field" }, "Password", h("input", { name: "password", type: "password", autocomplete: "current-password", required: true })),
    err, h("button", { class: "primary", type: "submit" }, "Sign in"));
  put(view, h("div", { class: "signin panel stack" }, h("h1", {}, "End of night"), form));
  form.querySelector("input").focus();
}

async function start() {
  if (!sessionStorage.getItem(TOKEN_KEY)) return renderSignIn();
  let me;
  try {
    me = await api("GET", "/me");
  } catch (e) {
    if (e.code === "password_change_required") return renderSignIn("Change your starting password in the LSM app first.");
    return toast(e.message, true);
  }
  if (me.tier !== "supervisor" && me.tier !== "admin") {
    sessionStorage.removeItem(TOKEN_KEY);
    return renderSignIn("This report is for supervisors and admins.");
  }
  put(who, h("span", {}, me.name), h("button", { class: "link", onclick: async () => {
    await api("DELETE", "/session").catch(() => {});
    sessionStorage.removeItem(TOKEN_KEY);
    renderSignIn();
  } }, "Sign out"));

  const ev = await api("GET", "/reception/events");
  const nearby = ev.nearby.filter((n) => !ev.today.some((t) => t.id === n.id));
  if (!eventID) eventID = (ev.today[0] || {}).id || null;
  const sel = h("select", { "aria-label": "Event" },
    !eventID ? h("option", { value: "" }, "Choose an event…") : null,
    ev.today.length ? h("optgroup", { label: "Today" }, ev.today.map(opt)) : null,
    h("optgroup", { label: "Nearby" }, nearby.map(opt)));
  sel.onchange = () => {
    eventID = sel.value;
    history.replaceState(null, "", "?event=" + eventID);
    load();
  };
  put(eventPick, sel);
  load();
  // The rack changes as people come back: keep the page current while
  // it is on screen.
  clearInterval(refreshTimer);
  refreshTimer = setInterval(() => document.visibilityState === "visible" && load(true), 20_000);
}

function opt(e) {
  return h("option", { value: e.id, selected: e.id === eventID }, `${e.date} · ${e.name}`);
}

async function load(quiet = false) {
  if (!eventID) return put(view, h("p", { class: "muted" }, "Choose an event above."));
  try {
    render(await api("GET", `/events/${eventID}/unreturned`));
  } catch (e) {
    if (!quiet && !(e instanceof ApiError && e.code === "unauthenticated")) toast(e.message, true);
  }
}

async function markReturned(o) {
  try {
    await api("POST", `/events/${o.event_id}/desk/${o.person_id}/issues/${o.issue_id}/return`);
    toast(`${o.resource}${o.number ? " " + o.number : ""} returned`);
    load();
  } catch (e) { toast(e.message, true); }
}

function render(r) {
  const allBack = r.out === 0;
  put(view,
    h("div", { class: "pagehead" },
      h("div", {}, h("h1", {}, r.event),
        h("p", { class: "muted" }, `${r.date} · as of ${r.generated}`)),
      h("div", { class: "row noprint" },
        h("a", { href: `/reception?event=${r.event_id}` }, "Reception"),
        h("button", { onclick: () => load() }, "Refresh"),
        h("button", { class: "primary", onclick: () => window.print() }, "Print"))),
    h("div", { class: "totals" },
      h("div", { class: "total" }, h("div", { class: "n" }, r.issued), h("div", { class: "l" }, "issued tonight")),
      h("div", { class: "total" }, h("div", { class: "n" }, r.returned), h("div", { class: "l" }, "returned")),
      h("div", { class: "total " + (allBack ? "clear" : "alert") }, h("div", { class: "n" }, r.out),
        h("div", { class: "l" }, allBack ? "everything is back" : "still out"))),
    h("div", { class: "stack", style: "margin-top:20px" },
      r.resources.map(resourceSection),
      r.earlier.length ? earlierSection(r.earlier) : null));
}

function resourceSection(rr) {
  const out = rr.outstanding.length;
  const rack = rr.tracked && rr.rack.length
    ? h("div", { class: "stack" },
        h("div", { class: "legend2" },
          h("span", {}, h("i", { class: "in" }), "on the rack"),
          h("span", {}, h("i", { class: "out" }), "out tonight"),
          h("span", {}, h("i", { class: "earlier" }), "out since an earlier night")),
        h("div", { class: "rack", role: "list", "aria-label": `${rr.resource} rack` }, rr.rack.map(slot)))
    : null;
  return h("section", { class: "panel stack" },
    h("header", {}, h("h2", {}, rr.resource),
      h("span", { class: out ? "pill bad" : "pill open" }, out ? `${out} of ${rr.issued} still out` : `all ${rr.issued} returned`)),
    rack,
    out ? outstandingTable(rr.outstanding, false) : null);
}

function slot(s) {
  const label = s.holder ? `${s.number}: ${s.holder.name}${s.state === "earlier" ? " (" + s.holder.event_date + ")" : ""}` : `${s.number}: ${s.state}`;
  return h("div", { class: "slot " + s.state, role: "listitem", title: label },
    h("span", { class: "no" }, s.number),
    s.holder ? h("span", { class: "holder" }, s.holder.name) : null,
    s.state === "earlier" ? h("span", { class: "holder" }, s.holder.event_date) : null);
}

function outstandingTable(items, showEvent) {
  return h("div", { class: "tablewrap" }, h("table", {},
    h("thead", {}, h("tr", {},
      showEvent ? h("th", {}, "Night") : null,
      h("th", {}, "Item"), h("th", {}, "Held by"), h("th", {}, "Post"), h("th", {}, "Issued"), h("th", {}, "Status"),
      h("th", { class: "noprint" }, ""))),
    h("tbody", {}, items.map((o) => h("tr", {},
      showEvent ? h("td", { class: "small" }, o.event_date, h("div", { class: "muted" }, o.event)) : null,
      h("td", {}, h("b", {}, o.resource, o.number ? " " + o.number : ""),
        !o.registered && o.number ? h("div", { class: "muted small" }, "not on register") : null),
      h("td", {}, o.name, h("div", { class: "muted small" }, o.badge)),
      h("td", { class: "small" }, o.post || h("span", { class: "muted" }, "—")),
      h("td", { class: "small" }, o.issued, o.issued_by ? h("div", { class: "muted" }, "by " + o.issued_by) : null),
      h("td", { class: "small" }, o.checked_out
        ? h("span", { class: "left" }, `Left at ${o.checked_out} without returning`)
        : o.event
          ? h("span", { class: "left" }, "Never checked out that night")
          : h("span", { class: "muted" }, "Still on site")),
      h("td", { class: "noprint" }, h("button", { onclick: () => markReturned(o) }, "Returned")))))));
}

function earlierSection(items) {
  return h("section", { class: "panel stack" },
    h("header", {}, h("h2", {}, "Still out from earlier nights"),
      h("span", { class: "pill warn" }, `${items.length} item${items.length > 1 ? "s" : ""}`)),
    outstandingTable(items, true));
}

start();
