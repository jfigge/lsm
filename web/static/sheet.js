// /sheet: the published assignment as it goes to print (SPEC §5): one row
// per post, grouped by department and area, with space to write pencil
// changes. Overrides are printed so they are traceable on the night.
// Gender never appears here.

import { h, put, request } from "./lib.js";

const view = document.getElementById("view");
const eventID = new URLSearchParams(location.search).get("event");
const KEYS = ["lsm.admin.token", "lsm.reception.token"];
const token = () => KEYS.map((k) => sessionStorage.getItem(k)).find(Boolean);

function signIn(message) {
  const err = h("p", { class: "err" }, message || "");
  const form = h("form", { onsubmit: async (ev) => {
    ev.preventDefault();
    const fd = new FormData(form);
    try {
      const s = await request("POST", "/session", { username: fd.get("username"), password: fd.get("password") });
      sessionStorage.setItem("lsm.reception.token", s.token);
      load();
    } catch (e) { err.textContent = e.message; }
  } },
    h("label", { class: "field" }, "Email", h("input", { name: "username", type: "email", autocomplete: "username", required: true })),
    h("label", { class: "field" }, "Password", h("input", { name: "password", type: "password", autocomplete: "current-password", required: true })),
    err, h("button", { class: "primary", type: "submit" }, "Sign in"));
  put(view, h("div", { class: "signin panel stack" }, h("h1", {}, "Deployment sheet"), form));
}

const KIND = { min_tenure: "below min tenure", restricted: "restricted override", lead: "lead chosen", pairing: "pairing override" };

async function load() {
  if (!eventID) return put(view, h("p", {}, "No event chosen."));
  if (!token()) return signIn();
  let b;
  try {
    b = await request("GET", `/events/${eventID}/assignments`, undefined, token());
  } catch (e) {
    if (e.status === 401) { KEYS.forEach((k) => sessionStorage.removeItem(k)); return signIn(); }
    return put(view, h("p", { class: "err" }, e.message));
  }
  document.title = `${b.event} · sheet`;
  put(view,
    h("div", { class: "row noprint", style: "margin-bottom:12px" },
      h("a", { href: `/admin#assign/${b.event_id}` }, "← Assignments"),
      h("button", { class: "primary", onclick: () => window.print() }, "Print")),
    b.departments.filter((d) => d.posts.some((p) => p.occupants.length || p.headcount)).map((d) => deptSheet(b, d)));
}

function deptSheet(b, d) {
  const rows = [];
  for (const shift of ["first", "second"]) {
    const posts = d.posts.filter((p) => p.shift === shift);
    if (!posts.length) continue;
    rows.push(h("tr", { class: "area" }, h("td", { colspan: 5 }, shift === "first" ? "FIRST SHIFT" : "SECOND SHIFT")));
    let area = null;
    for (const p of posts) {
      if (p.area !== area) {
        area = p.area;
        rows.push(h("tr", { class: "area" }, h("td", { colspan: 5 }, area || "Other")));
      }
      // One line per unit of headcount, plus any extra (over-filled) people.
      const n = Math.max(p.headcount, p.occupants.length);
      for (let i = 0; i < n; i++) {
        const o = p.occupants[i];
        const notes = o ? [
          ...o.overrides.filter((x) => x.kind !== "placement").map((x) => `${KIND[x.kind] || x.kind} — ${x.by}, ${x.at}`),
          o.source === "manual" ? `placed by hand${o.overrides[0] ? " — " + o.overrides[0].by : ""}` : null,
          o.needs_confirmation ? "confirm roam pair" : null,
        ].filter(Boolean) : [];
        rows.push(h("tr", {},
          h("td", {}, i === 0 ? h("b", {}, p.name) : h("span", { class: "muted" }, "〃"), i === 0 && p.resources.length ? h("span", { class: "muted" }, ` · ${p.resources.join(", ")}`) : null),
          h("td", {}, o ? [o.name, " ", h("span", { class: "muted" }, o.badge)] : h("span", { class: "empty" }, "EMPTY")),
          h("td", { class: "small" }, o && o.then ? (shift === "first" ? "→ " : "from ") + o.then : ""),
          h("td", {}, notes.map((t) => h("span", { class: "note" }, t))),
          h("td", { class: "write" }, "")));
      }
    }
  }
  return h("section", {},
    h("div", { class: "sheethead" },
      h("div", {}, h("h1", {}, `${b.event} — ${d.department}`),
        h("div", { class: "muted" }, `${b.date} · first shift ${d.first_filled} of ${d.first_established} · second shift ${d.second_filled} of ${d.second_established}`)),
      h("div", { class: "small" }, b.published_at ? `Published ${b.published_at}` : h("span", { class: "draft" }, "DRAFT — not published"))),
    h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Post"), h("th", {}, "Name"), h("th", {}, "Other shift"), h("th", {}, "Notes"), h("th", {}, "Changes"))),
      h("tbody", {}, rows)));
}

load();
