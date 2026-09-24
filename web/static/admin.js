// LSM admin: months and signups, ranking rules, headcount (SPEC §8 step 4).
// Plain DOM, no framework. Every value from the server is inserted as
// text, never as HTML.

import { h, put, ApiError, request } from "./lib.js";

const TOKEN_KEY = "lsm.admin.token";
const view = document.getElementById("view");
const nav = document.getElementById("nav");
const who = document.getElementById("who");

const RULE_NAMES = {
  availability_rate: "Availability rate",
  performance_rating: "Performance rating",
  capability_match: "Capability match",
  season_load: "Season load",
  recency: "Recency",
  tenure: "Tenure",
};
const RULE_ORDER = Object.keys(RULE_NAMES);

let me = null;

// ------------------------------------------------------------ helpers --

async function api(method, path, body) {
  try {
    return await request(method, path, body, sessionStorage.getItem(TOKEN_KEY));
  } catch (e) {
    if (e.code === "unauthenticated") {
      sessionStorage.removeItem(TOKEN_KEY);
      me = null;
      renderSignIn();
    } else if (e.code === "password_change_required") {
      renderPasswordChange();
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
  toastTimer = setTimeout(() => (t.className = ""), isError ? 5000 : 2500);
}

function fail(e) {
  if (e instanceof ApiError && (e.code === "unauthenticated" || e.code === "password_change_required")) return;
  console.error(e);
  toast(e.message || "Something went wrong", true);
}

const monthName = (m) =>
  new Date(m + "-01T12:00:00").toLocaleDateString(undefined, { month: "long", year: "numeric" });
const dayName = (d) =>
  new Date(d + "T12:00:00").toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
const fmt = (n, dp = 1) => (Math.round(n * 10 ** dp) / 10 ** dp).toString();

function requiredText(req) {
  if (req.headcount === null || req.headcount === undefined) return "no requirement";
  return String(req.headcount);
}

function sourceText(req) {
  switch (true) {
    case req.source === "event":
      return `set for this event (posts: ${req.posts})`;
    case req.source.startsWith("event_type:"):
      return `default for ${req.source.slice(11)} (posts: ${req.posts})`;
    case req.source === "posts":
      return "from the posts that apply";
    default:
      return "no posts: every signup kept";
  }
}

// An inline confirmation replaces the triggering button until answered.
function confirmInline(anchor, message, label, onConfirm) {
  const box = h("div", { class: "confirm" },
    h("span", {}, message),
    h("button", { class: "primary", onclick: async () => {
      box.querySelectorAll("button").forEach((b) => (b.disabled = true));
      try { await onConfirm(); } catch (e) { fail(e); box.replaceWith(anchor); }
    } }, label),
    h("button", { onclick: () => box.replaceWith(anchor) }, "Cancel"),
  );
  anchor.replaceWith(box);
  box.querySelector("button.primary").focus();
}

// ------------------------------------------------------------- session --

function renderSignIn(message) {
  nav.hidden = true;
  put(who);
  const err = h("p", { class: "err" }, message || "");
  const form = h("form", { onsubmit: async (ev) => {
    ev.preventDefault();
    err.textContent = "";
    const fd = new FormData(form);
    try {
      const res = await fetch("/api/v1/session", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username: fd.get("username"), password: fd.get("password") }),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error?.message || "Sign-in failed");
      sessionStorage.setItem(TOKEN_KEY, data.token);
      start();
    } catch (e) {
      err.textContent = e.message;
    }
  } },
    h("label", { class: "field" }, "Email", h("input", { name: "username", type: "email", autocomplete: "username", required: true })),
    h("label", { class: "field" }, "Password", h("input", { name: "password", type: "password", autocomplete: "current-password", required: true })),
    err,
    h("button", { class: "primary", type: "submit" }, "Sign in"),
  );
  put(view, h("div", { class: "signin panel stack" }, h("h1", {}, "Sign in"), form));
  form.querySelector("input").focus();
}

function renderPasswordChange() {
  nav.hidden = true;
  const err = h("p", { class: "err" });
  const form = h("form", { onsubmit: async (ev) => {
    ev.preventDefault();
    const fd = new FormData(form);
    if (fd.get("new") !== fd.get("again")) {
      err.textContent = "The new passwords do not match.";
      return;
    }
    try {
      await api("POST", "/me/password", { current_password: fd.get("current"), new_password: fd.get("new") });
      toast("Password changed");
      start();
    } catch (e) {
      err.textContent = e.message;
    }
  } },
    h("p", { class: "muted small" }, "Your account still has its initial password. Choose a new one to continue."),
    h("label", { class: "field" }, "Current password", h("input", { name: "current", type: "password", autocomplete: "current-password", required: true })),
    h("label", { class: "field" }, "New password (8 or more characters)", h("input", { name: "new", type: "password", autocomplete: "new-password", minlength: 8, required: true })),
    h("label", { class: "field" }, "New password again", h("input", { name: "again", type: "password", autocomplete: "new-password", minlength: 8, required: true })),
    err,
    h("button", { class: "primary", type: "submit" }, "Change password"),
  );
  put(view, h("div", { class: "signin panel stack" }, h("h1", {}, "Change your password"), form));
  form.querySelector("input").focus();
}

async function signOut() {
  try { await api("DELETE", "/session"); } catch (e) { /* signed out either way */ }
  sessionStorage.removeItem(TOKEN_KEY);
  me = null;
  renderSignIn();
}

async function start() {
  if (!sessionStorage.getItem(TOKEN_KEY)) return renderSignIn();
  try {
    me = await api("GET", "/me");
  } catch (e) {
    return fail(e);
  }
  if (me.must_change_password) return renderPasswordChange();
  if (me.tier !== "admin") {
    sessionStorage.removeItem(TOKEN_KEY);
    return renderSignIn("The admin pages are for admin accounts.");
  }
  put(who, h("span", {}, me.name), h("button", { class: "link", onclick: signOut }, "Sign out"));
  nav.hidden = false;
  route();
}

// -------------------------------------------------------------- router --

function route() {
  if (!me) return;
  const [section, arg] = location.hash.slice(1).split("/");
  for (const a of nav.querySelectorAll("a")) {
    a.classList.toggle("active", a.dataset.section === (section === "event" || section === "assign" ? "months" : section || "months"));
  }
  const run = {
    event: () => renderEvent(arg),
    assign: () => renderAssign(arg),
    rules: renderRules,
    headcount: renderHeadcount,
  }[section] || (() => renderMonths(arg));
  run().catch(fail);
}
window.addEventListener("hashchange", route);

// -------------------------------------------------------------- months --

async function renderMonths(selected) {
  const months = await api("GET", "/months");
  if (!selected) {
    selected = (months.find((m) => m.status === "open") || months[months.length - 1] || {}).month;
  }

  const cards = months.map((m) => {
    const status = m.status || "unopened";
    const actions = h("div", { class: "row" });
    const act = (label, next, message, cls) => {
      const btn = h("button", { class: cls, onclick: (ev) => {
        ev.stopPropagation();
        confirmInline(btn, message, label, async () => {
          await api("PUT", `/months/${m.month}`, { status: next });
          toast(`${monthName(m.month)} ${next === "open" ? "opened" : "closed"}`);
          renderMonths(m.month).catch(fail);
        });
      } }, label);
      actions.append(btn);
    };
    if (status === "open") act("Close signups", "closed", "Staff will no longer be able to change their availability.", "");
    else if (status === "closed") act("Reopen", "open", "Staff can change their availability again.", "");
    else act("Open for signups", "open", "Staff will be emailed that the month is open.", "primary");
    return h("div", {
      class: "month" + (m.month === selected ? " selected" : ""),
      tabindex: 0,
      onclick: () => (location.hash = `months/${m.month}`),
      onkeydown: (ev) => ev.key === "Enter" && ev.target === ev.currentTarget && (location.hash = `months/${m.month}`),
    },
      h("div", { class: "row spread" }, h("span", { class: "name" }, monthName(m.month)),
        h("span", { class: "pill " + status }, { open: "Open", closed: "Closed", unopened: "Not opened" }[status])),
      h("div", { class: "muted small" }, `${m.events} event${m.events === 1 ? "" : "s"}`),
      actions,
    );
  });

  const monthInput = h("input", { type: "month", "aria-label": "Month to open" });
  const extra = h("div", { class: "row small muted" }, "Open a month with no events yet:", monthInput,
    h("button", { onclick: async () => {
      if (!monthInput.value) return;
      try {
        await api("PUT", `/months/${monthInput.value}`, { status: "open" });
        location.hash = `months/${monthInput.value}`;
      } catch (e) { fail(e); }
    } }, "Open"));

  const eventsPanel = h("div", {}, h("p", { class: "muted" }, "Loading events…"));
  put(view,
    h("div", { class: "pagehead" },
      h("div", {}, h("h1", {}, "Months & signups"),
        h("p", { class: "muted" }, "Open a month to let staff mark their availability; close it to freeze signups."))),
    h("div", { class: "months" }, cards),
    extra,
    h("div", { style: "margin-top:24px" }, eventsPanel),
  );
  if (selected) renderMonthEvents(eventsPanel, selected).catch(fail);
}

async function renderMonthEvents(panel, month) {
  const events = await api("GET", `/months/${month}/events`);
  if (!events.length) {
    put(panel, h("p", { class: "muted" }, `No events in ${monthName(month)}.`));
    return;
  }
  const rows = events.map((e) => {
    // Capped departments get a line each; uncapped ones (no posts, no
    // requirement) keep every signup and share one line.
    const uncapped = e.departments.filter((d) => d.required.headcount === null);
    const depts = e.departments.filter((d) => d.required.headcount !== null).map((d) => {
      const req = d.required.headcount;
      const chips = [];
      if (d.shortfall > 0) chips.push(h("span", { class: "pill bad" }, `short ${d.shortfall}`));
      if (d.below_line > 0) chips.push(h("span", { class: "pill info" }, `${d.below_line} below line`));
      if (d.overrides > 0) chips.push(h("span", { class: "pill warn" }, `${d.overrides} override${d.overrides > 1 ? "s" : ""}`));
      return h("div", { class: "deptline" },
        h("span", { class: "dn" }, d.department),
        h("span", { class: "num" }, `${d.signups} signed up`),
        h("span", { class: "muted" }, req === null ? "· no requirement" : `· needs ${req}`),
        chips);
    });
    if (uncapped.length) {
      depts.push(h("div", { class: "deptline" }, h("span", { class: "dn" }, "No requirement"),
        h("span", { class: "muted" }, uncapped.map((d) => `${d.department} ${d.signups}`).join(" · "))));
    }
    return h("tr", {},
      h("td", { class: "small", style: "white-space:nowrap" }, dayName(e.date)),
      h("td", {}, h("a", { href: `#event/${e.id}` }, e.name), h("div", { class: "muted small" }, e.event_type,
        e.roster_size ? [" · ", h("a", { href: `#assign/${e.id}` }, "assignments")] : null)),
      h("td", {}, depts),
      h("td", { class: "small" }, e.roster_size
        ? h("span", { class: "pill open" }, `${e.roster_size} on roster`)
        : h("span", { class: "muted" }, "not committed")),
    );
  });
  put(panel, h("div", { class: "panel flush" },
    h("header", { style: "padding:12px 16px" }, h("h2", {}, monthName(month)),
      h("span", { class: "muted small" }, "Signups against each department's required headcount")),
    h("div", { class: "tablewrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Date"), h("th", {}, "Event"), h("th", {}, "Departments"), h("th", {}, "Roster"))),
      h("tbody", {}, rows)))));
}

// --------------------------------------------------------------- event --

const eventState = { expanded: new Set(), filter: "" };

async function renderEvent(id, keepState = false) {
  if (!keepState) {
    eventState.expanded.clear();
    eventState.filter = "";
  }
  const [{ ranking, windows }, departments] = await Promise.all([
    api("GET", `/events/${id}/ranking`),
    api("GET", "/departments"),
  ]);
  const windowLabel = Object.fromEntries(windows.map((w) => [w.id, w.label]));
  const refresh = () => renderEvent(id, true).catch(fail);

  const pending = ranking.departments.reduce(
    (n, d) => n + d.candidates.filter((c) => c.above_line !== c.on_roster).length, 0);
  const commit = h("button", { class: "primary" }, ranking.roster_size ? "Re-commit roster" : "Commit roster");
  commit.onclick = () => confirmInline(commit,
    "Write the people above the line to the event's roster. The matcher assigns from it.",
    "Commit", async () => {
      const r = await api("POST", `/events/${id}/roster`);
      toast(`Roster committed: ${r.added} added, ${r.removed} removed, ${r.kept} unchanged`);
      refresh();
    });
  const rosterState = !ranking.roster_size
    ? h("span", { class: "pill" }, "Roster not committed")
    : pending
      ? h("span", { class: "pill warn" }, `Roster committed · ${pending} change${pending > 1 ? "s" : ""} since`)
      : h("span", { class: "pill open" }, `Roster committed · ${ranking.roster_size} people`);

  const filter = h("input", { type: "search", placeholder: "Find a person", value: eventState.filter,
    "aria-label": "Filter by name or badge" });
  const sections = h("div", { class: "stack" });
  // Expanding a row or filtering redraws from the loaded ranking; only
  // changes refetch it.
  const draw = () => put(sections, ranking.departments.map((d) => deptSection(id, d, windowLabel, refresh, draw, ranking.roster_size)));
  filter.oninput = () => { eventState.filter = filter.value.trim().toLowerCase(); draw(); };
  draw();

  const month = ranking.date.slice(0, 7);
  put(view,
    h("a", { class: "back", href: `#months/${month}` }, `← ${monthName(month)}`),
    h("div", { class: "pagehead" },
      h("div", {}, h("h1", {}, ranking.event),
        h("p", { class: "muted" }, `${dayName(ranking.date)} · ranked with the saved weights · `,
          h("a", { href: "#rules" }, "change weights"))),
      h("div", { class: "row" }, rosterState, commit,
        ranking.roster_size ? h("a", { class: "btn", href: `#assign/${id}` }, "Assignments →") : null)),
    h("div", { class: "row spread", style: "margin-bottom:12px" }, filter, ruleLegend()),
    sections,
  );
  if (eventState.filter) filter.focus();
}

function ruleLegend() {
  return h("div", { class: "legend" }, RULE_ORDER.map((r, i) => h("span", {}, h("i", { class: `rule-${i}` }), RULE_NAMES[r])));
}

function deptSection(eventID, d, windowLabel, refresh, redraw, rosterSize) {
  const q = eventState.filter;
  const visible = d.candidates.filter((c) => !q || c.name.toLowerCase().includes(q) || c.badge.includes(q));
  const maxTotal = Math.max(1, ...d.candidates.map((c) => c.total));

  // Headcount for this event.
  const hcBox = h("span", { class: "row small" });
  const showHc = () => {
    const change = h("button", { class: "link", onclick: editHc }, "change");
    const reset = d.required.source === "event"
      ? h("button", { class: "link", onclick: async () => {
          await api("PUT", `/events/${eventID}/headcount`, { department_id: d.department_id, headcount: null }).catch(fail);
          refresh();
        } }, "use default")
      : null;
    put(hcBox, h("span", { class: "muted" }, sourceText(d.required)), change, reset);
  };
  const editHc = () => {
    const input = h("input", { type: "number", min: 0, value: d.required.headcount ?? d.required.posts, "aria-label": "Required headcount" });
    put(hcBox, h("span", {}, "Needs"), input,
      h("button", { class: "primary", onclick: async () => {
        try {
          await api("PUT", `/events/${eventID}/headcount`, { department_id: d.department_id, headcount: Number(input.value) });
          toast(`${d.department} needs ${input.value} for this event`);
          refresh();
        } catch (e) { fail(e); }
      } }, "Save for this event"),
      h("button", { onclick: showHc }, "Cancel"));
    input.focus();
  };
  showHc();

  const rows = [];
  let lineDrawn = false;
  visible.forEach((c) => {
    if (!q && !lineDrawn && !c.above_line) {
      rows.push(h("tr", { class: "cutoff" }, h("td", { colspan: 7 }, h("span", {}, `Cutoff · ${d.above_line} above the line`))));
      lineDrawn = true;
    }
    rows.push(...candidateRows(eventID, c, windowLabel, maxTotal, refresh, redraw, rosterSize));
  });
  if (!visible.length) rows.push(h("tr", {}, h("td", { colspan: 7, class: "muted" }, q ? "No match." : "No signups.")));

  return h("section", { class: "panel flush dept" },
    h("header", {},
      h("div", { class: "stack", style: "--gap:4px" },
        h("h2", {}, d.department),
        h("div", { class: "stats" },
          h("span", {}, "Required ", h("b", {}, requiredText(d.required))),
          h("span", {}, "Signed up ", h("b", {}, d.signups)),
          h("span", {}, "Above line ", h("b", {}, d.above_line)),
          h("span", {}, "Below ", h("b", {}, d.signups - d.above_line)),
          d.shortfall ? h("span", { class: "pill bad" }, `Short ${d.shortfall}`) : null),
        hcBox)),
    h("div", { class: "tablewrap" }, h("table", {},
      h("thead", {}, h("tr", {},
        h("th", { class: "num" }, "#"), h("th", {}, "Name"), h("th", {}, "Signed up for"),
        h("th", { class: "num" }, "Score"), h("th", {}, "Rule contributions"), h("th", {}, "Status"), h("th", {}, ""))),
      h("tbody", {}, rows))));
}

function candidateRows(eventID, c, windowLabel, maxTotal, refresh, redraw, rosterSize) {
  const key = c.person_id;
  const open = eventState.expanded.has(key);
  const status = [];
  if (c.override) status.push(h("span", { class: "pill warn", title: `By ${c.override.by_name}` }, "Pulled above"));
  else if (c.above_line) status.push(h("span", { class: "pill open" }, "Above line"));
  else status.push(h("span", { class: "pill" }, "Below line"));
  if (c.displaced) status.push(" ", h("span", { class: "pill info", title: "Above the line on score; displaced by an override" }, "Displaced"));
  // Drift between the committed roster and the recommendation; only
  // meaningful once a roster has been committed.
  if (rosterSize && c.on_roster !== c.above_line) {
    status.push(" ", h("span", { class: "pill warn", title: "Commit the roster to apply" }, c.on_roster ? "On roster" : "Not on roster"));
  }

  const bar = h("div", { class: "bar", title: c.scores.map((s) => `${RULE_NAMES[s.rule]}: ${fmt(s.points, 2)}`).join("\n") },
    c.scores.map((s) => h("i", { class: `rule-${RULE_ORDER.indexOf(s.rule)}`, style: `width:${(100 * s.points) / maxTotal}%` })));

  let action = null;
  if (c.override) {
    action = h("button", { class: "link" }, "Withdraw");
    action.onclick = (ev) => {
      ev.stopPropagation();
      confirmInline(action, `Return ${c.name} to their ranked place?`, "Withdraw", async () => {
        await api("DELETE", `/events/${eventID}/overrides/${c.person_id}`);
        toast("Override withdrawn");
        refresh();
      });
    };
  } else if (!c.above_line) {
    action = h("button", {}, "Pull above line");
    action.onclick = (ev) => {
      ev.stopPropagation();
      const note = h("input", { placeholder: "Reason (recorded)", "aria-label": "Reason", size: 24 });
      const box = h("div", { class: "confirm", onclick: (e) => e.stopPropagation() },
        note,
        h("button", { class: "primary", onclick: async () => {
          try {
            await api("POST", `/events/${eventID}/overrides`, { person_id: c.person_id, note: note.value });
            toast(`${c.name} pulled above the line`);
            refresh();
          } catch (e) { fail(e); }
        } }, "Pull above"),
        h("button", { onclick: () => box.replaceWith(action) }, "Cancel"));
      action.replaceWith(box);
      note.focus();
    };
  }

  const signup = c.signup.status === "all_shifts" ? "All shifts" : windowLabel[c.signup.window_id] || "One window";
  const row = h("tr", {
    class: "cand" + (c.above_line ? "" : " below"),
    "data-person": key,
    tabindex: 0,
    "aria-expanded": open ? "true" : "false",
    onclick: () => toggle(),
    onkeydown: (ev) => ev.key === "Enter" && ev.target === ev.currentTarget && toggle(),
  },
    h("td", { class: "num" }, c.rank),
    h("td", { class: "name" }, c.name, h("div", { class: "muted small" }, c.badge)),
    h("td", { class: "small" }, signup),
    h("td", { class: "num" }, fmt(c.total, 1)),
    h("td", { style: "min-width:140px" }, bar),
    h("td", {}, status),
    h("td", {}, action),
  );
  const toggle = () => {
    eventState.expanded.has(key) ? eventState.expanded.delete(key) : eventState.expanded.add(key);
    redraw();
    document.querySelector(`tr[data-person="${key}"]`)?.focus();
  };
  if (!open) return [row];

  const detail = h("div", { class: "rules" },
    h("span", { class: "h" }, "Rule"), h("span", { class: "h num" }, "Weight"), h("span", { class: "h num" }, "Score"),
    h("span", { class: "h num" }, "Points"), h("span", { class: "h hd" }, "Why"),
    c.scores.map((s) => [
      h("span", {}, RULE_NAMES[s.rule]),
      h("span", { class: "num muted" }, s.weight),
      h("span", { class: "num" }, fmt(s.score)),
      h("span", { class: "num" }, fmt(s.points, 2)),
      h("span", { class: "d muted" }, s.detail),
    ]),
    h("span", {}, h("b", {}, "Total")), h("span", {}), h("span", {}), h("span", { class: "num" }, h("b", {}, fmt(c.total, 2))),
    h("span", { class: "d muted" }, `Signed up ${new Date(c.signup.signed_up_at).toLocaleString()}: the tiebreak between equal totals`),
  );
  const extra = c.override
    ? h("p", { class: "small", style: "margin:10px 0 0" },
        `Pulled above the line by ${c.override.by_name || "an admin"} on ${new Date(c.override.at).toLocaleString()}`,
        c.override.note ? ` — “${c.override.note}”` : "")
    : null;
  return [row, h("tr", { class: "detail" }, h("td", { colspan: 7 }, detail, extra))];
}

// --------------------------------------------------------------- rules --

async function renderRules() {
  const [saved, months] = await Promise.all([api("GET", "/ranking/rules"), api("GET", "/months")]);
  const weights = Object.fromEntries(saved.rules.map((r) => [r.rule, r.weight]));
  const draft = { ...weights };
  let expectation = saved.availability_expectation_percent;

  const saveBtn = h("button", { class: "primary", disabled: true }, "Save weights");
  const discardBtn = h("button", { disabled: true, onclick: () => renderRules().catch(fail) }, "Discard changes");
  const previewBtn = h("button", {}, "Preview");
  const results = h("div", {}, h("p", { class: "muted small" },
    "Preview ranks every event in the month under the saved and the proposed weights and lists who crosses the line in each direction. Nothing is saved until you press Save."));

  const dirty = () => RULE_ORDER.some((r) => draft[r] !== weights[r]) || expectation !== saved.availability_expectation_percent;
  const shares = {};
  const updateState = () => {
    const total = RULE_ORDER.reduce((n, r) => n + draft[r], 0) || 1;
    for (const r of RULE_ORDER) shares[r].textContent = draft[r] ? `${Math.round((100 * draft[r]) / total)}%` : "off";
    saveBtn.disabled = discardBtn.disabled = !dirty();
  };

  const grid = h("div", { class: "weights" });
  for (const r of saved.rules) {
    const num = h("input", { type: "number", min: 0, max: 1000, value: r.weight, "aria-label": `${RULE_NAMES[r.rule]} weight` });
    const range = h("input", { type: "range", min: 0, max: 50, value: Math.min(r.weight, 50), "aria-hidden": "true", tabindex: -1 });
    shares[r.rule] = h("span", { class: "num muted small" });
    const lbl = h("span", { class: "lbl" }, h("b", {}, RULE_NAMES[r.rule]));
    const set = (v) => {
      draft[r.rule] = Math.max(0, Math.min(1000, Math.round(Number(v) || 0)));
      num.value = draft[r.rule];
      range.value = Math.min(draft[r.rule], 50);
      lbl.classList.toggle("changed", draft[r.rule] !== weights[r.rule]);
      updateState();
    };
    num.oninput = () => set(num.value);
    range.oninput = () => set(range.value);
    grid.append(lbl, range, num, shares[r.rule], h("span", { class: "desc" }, r.description));
  }
  const expInput = h("input", { type: "number", min: 1, max: 100, value: expectation, "aria-label": "Availability expectation percent" });
  expInput.oninput = () => { expectation = Number(expInput.value); updateState(); };
  updateState();

  const openMonths = months.filter((m) => m.events > 0);
  const monthSel = h("select", { "aria-label": "Month to preview" },
    openMonths.map((m) => h("option", { value: m.month, selected: m.status === "open" }, `${monthName(m.month)}${m.status === "open" ? " (open)" : ""}`)));

  previewBtn.onclick = async () => {
    previewBtn.disabled = true;
    put(results, h("p", { class: "muted" }, "Ranking every event twice…"));
    try {
      const p = await api("POST", "/ranking/preview", {
        month: monthSel.value, weights: draft, availability_expectation_percent: expectation,
      });
      put(results, previewResults(p));
    } catch (e) { fail(e); put(results); }
    previewBtn.disabled = false;
  };
  saveBtn.onclick = () => confirmInline(saveBtn, "Every event is re-ranked with these weights. Committed rosters stay as they are until re-committed.",
    "Save", async () => {
      await api("PUT", "/ranking/rules", { weights: draft, availability_expectation_percent: expectation });
      toast("Ranking weights saved");
      renderRules().catch(fail);
    });

  put(view,
    h("div", { class: "pagehead" }, h("div", {}, h("h1", {}, "Ranking rules"),
      h("p", { class: "muted" }, "Each rule scores a person 0–100; the weights decide how much each counts. Weight 0 turns a rule off. Ties go to the earliest signup."))),
    h("div", { class: "panel stack" },
      grid,
      h("div", { class: "row" }, h("label", { class: "row" }, h("b", {}, "Availability expectation"), expInput, "% of each month's events"),
        h("span", { class: "muted small" }, "The published policy, shown on the staff Availability tab and used by the availability-rate rule.")),
      h("div", { class: "row" }, saveBtn, discardBtn)),
    h("div", { class: "panel stack", style: "margin-top:16px" },
      h("header", {}, h("h2", {}, "Before and after"), h("div", { class: "row" }, monthSel, previewBtn)),
      results),
  );
}

function previewResults(p) {
  const moved = p.events.reduce((n, e) => n + e.entering.length + e.leaving.length, 0);
  if (!moved) return h("p", {}, "No one crosses the line in either direction with these weights.");
  const list = (items, cls, verb) => items.length
    ? h("ul", {}, items.map((x) => h("li", {},
        h("span", { class: cls }, x.name), ` `,
        h("span", { class: "muted small" }, `${x.department} · rank ${x.rank_before} → ${x.rank_after} · ${fmt(x.total_before)} → ${fmt(x.total_after)}`))))
    : h("p", { class: "muted small" }, `No one ${verb}.`);
  return h("div", { class: "stack" },
    h("p", {}, `${moved} line crossing${moved > 1 ? "s" : ""} across ${p.events.filter((e) => e.entering.length + e.leaving.length).length} of ${p.events.length} events.`),
    p.events.filter((e) => e.entering.length || e.leaving.length).map((e) => h("div", {},
      h("h3", {}, h("a", { href: `#event/${e.event_id}` }, e.event), " ", h("span", { class: "muted small" }, dayName(e.date))),
      h("div", { class: "crossings" },
        h("div", {}, h("b", { class: "enter" }, `Move above the line (${e.entering.length})`), list(e.entering, "enter", "moves up")),
        h("div", {}, h("b", { class: "leave" }, `Drop below the line (${e.leaving.length})`), list(e.leaving, "leave", "drops"))))));
}

// ----------------------------------------------------------- headcount --

async function renderHeadcount() {
  const [types, departments] = await Promise.all([api("GET", "/headcount"), api("GET", "/departments")]);
  const refresh = () => renderHeadcount().catch(fail);

  const cell = (t, d) => {
    const td = h("td", { class: "hc" });
    const show = () => {
      const req = d.required;
      const src = d.override !== null ? "set here"
        : req.source.startsWith("event_type:") ? `from ${req.source.slice(11)}`
        : req.source === "posts" ? "from posts" : "no posts";
      put(td,
        h("span", { class: "v" }, req.headcount === null ? "—" : req.headcount),
        h("span", { class: "src" }, src, d.override !== null && req.posts ? ` (posts: ${req.posts})` : ""),
        h("button", { class: "link", onclick: edit }, "set"),
        d.override !== null ? h("button", { class: "link", onclick: () => save(null) }, "clear") : null);
    };
    const save = async (value) => {
      try {
        await api("PUT", `/event-types/${t.event_type_id}/headcount`, { department_id: d.department_id, headcount: value });
        toast(value === null ? `${t.name}: ${d.department} back to default` : `${t.name}: ${d.department} needs ${value}`);
        refresh();
      } catch (e) { fail(e); }
    };
    const edit = () => {
      const input = h("input", { type: "number", min: 0, value: d.required.headcount ?? d.required.posts, "aria-label": `${t.name} ${d.department} headcount` });
      input.onkeydown = (ev) => { if (ev.key === "Enter") save(Number(input.value)); if (ev.key === "Escape") show(); };
      put(td, input, h("div", { class: "row", style: "margin-top:4px" },
        h("button", { class: "primary", onclick: () => save(Number(input.value)) }, "Save"),
        h("button", { onclick: show }, "Cancel")));
      input.focus();
    };
    show();
    return td;
  };

  put(view,
    h("div", { class: "pagehead" }, h("div", {}, h("h1", {}, "Required headcount"),
      h("p", { class: "muted" }, "How many people each department needs per event type. Signups are capped at this number. Without a value set, it comes from the department's posts that apply to the type (second-shift posts excluded — they are filled by people redeploying). A subtype inherits its parent's value; a single event can override it from the event's page."))),
    h("div", { class: "panel flush" }, h("div", { class: "tablewrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Event type"), departments.map((d) => h("th", {}, d.name)))),
      h("tbody", {}, types.map((t) => h("tr", {},
        h("td", { style: `padding-left:${10 + 18 * t.depth}px` }, t.depth ? "↳ " : "", h("b", {}, t.name)),
        t.departments.map((d) => cell(t, d)))))))),
  );
}

// ---------------------------------------------------------- assignments --

const assignState = { filter: "", dept: null, editing: null };

async function renderAssign(id, keep = false) {
  if (!keep) Object.assign(assignState, { filter: "", dept: null, editing: null });
  const b = await api("GET", `/events/${id}/assignments`);
  const refresh = () => renderAssign(id, true).catch(fail);
  const depts = b.departments.filter((d) => d.posts.length);
  if (!assignState.dept || !depts.some((d) => d.department_id === assignState.dept)) {
    assignState.dept = (depts[0] || {}).department_id;
  }
  const d = depts.find((x) => x.department_id === assignState.dept);

  const present = h("input", { type: "checkbox", id: "present-only" });
  const run = h("button", { class: "primary" }, b.matched_at ? "Re-run matcher" : "Run matcher");
  run.onclick = () => confirmInline(run,
    b.matched_at ? "Re-running replaces every unpinned draft placement. Pinned and hand placements stay." : "Draft assignments for everyone on the roster.",
    "Run", async () => {
      const r = await api("POST", `/events/${id}/match`, { present_only: present.checked });
      toast(`Placed ${r.summary.placed} · kept ${r.summary.pinned} pinned · ${r.summary.empty} empty`);
      refresh();
    });
  const pub = h("button", {}, b.published_at ? "Back to draft" : "Publish");
  pub.onclick = () => confirmInline(pub,
    b.published_at ? "Mark the assignment as a draft again?" : "Publish: the sheet is final and goes to print. Changes afterwards are still recorded.",
    b.published_at ? "Back to draft" : "Publish", async () => {
      await api("PUT", `/events/${id}/published`, { published: !b.published_at });
      refresh();
    });

  const status = b.published_at
    ? h("span", { class: "pill open" }, `Published ${b.published_at}`)
    : b.matched_at ? h("span", { class: "pill warn" }, `Draft · matched ${b.matched_at}`) : h("span", { class: "pill" }, "Not matched yet");

  const filter = h("input", { type: "search", placeholder: "Find a post or person", value: assignState.filter, "aria-label": "Filter" });
  const body = h("div");
  const draw = () => put(body, d ? deptBoard(id, d, refresh) : h("p", { class: "muted" }, "No posts apply to this event."));
  filter.oninput = () => { assignState.filter = filter.value.trim().toLowerCase(); draw(); };
  draw();

  const month = b.date.slice(0, 7);
  put(view,
    h("a", { class: "back", href: `#event/${id}` }, "← Ranking"), " ",
    h("a", { class: "back", href: `#months/${month}` }, `· ${monthName(month)}`),
    h("div", { class: "pagehead" },
      h("div", {}, h("h1", {}, b.event, " — assignments"),
        h("p", { class: "muted" }, `${dayName(b.date)} · ${b.roster_size} on the roster · `, status)),
      h("div", { class: "row" },
        h("label", { class: "row small", for: "present-only" }, present, "only people checked in"),
        run, pub, h("a", { class: "btn", href: `/sheet?event=${id}` }, "Printed sheet"))),
    depts.length > 1 ? h("div", { class: "row", style: "margin-bottom:12px" }, depts.map((x) => h("button", {
      class: x.department_id === assignState.dept ? "primary" : "",
      onclick: () => { assignState.dept = x.department_id; renderAssign(id, true).catch(fail); },
    }, x.department))) : null,
    d ? deptStats(d) : null,
    h("div", { class: "row", style: "margin:12px 0" }, filter),
    body);
}

function deptStats(d) {
  return h("div", { class: "stats panel", style: "padding:10px 16px" },
    h("span", {}, "First shift ", h("b", {}, `${d.first_filled} of ${d.first_established}`)),
    h("span", {}, "Second shift ", h("b", {}, `${d.second_filled} of ${d.second_established}`)),
    d.empty_posts ? h("span", { class: "pill bad" }, `${d.empty_posts} empty`) : h("span", { class: "pill open" }, "every post filled"),
    d.unassigned ? h("span", { class: "pill warn" }, `${d.unassigned} on the roster without a post`) : null,
    d.no_second_post ? h("span", { class: "pill info" }, `${d.no_second_post} freed at second shift with no post (breakers)`) : null,
    d.needs_confirmation ? h("span", { class: "pill warn" }, `${d.needs_confirmation} roam placements to confirm`) : null);
}

const OVERRIDE_LABEL = {
  placement: "placed by hand", min_tenure: "below min tenure", restricted: "restricted override",
  lead: "lead chosen by hand", pairing: "pairing override",
};

function deptBoard(eventID, d, refresh) {
  const q = assignState.filter;
  const match = (p) => !q || p.name.toLowerCase().includes(q) || p.area.toLowerCase().includes(q) ||
    p.occupants.some((o) => o.name.toLowerCase().includes(q) || o.badge.includes(q));
  const sections = [];
  for (const shift of ["first", "second"]) {
    const posts = d.posts.filter((p) => p.shift === shift && match(p));
    if (!posts.length) continue;
    const rows = [];
    let area = null;
    for (const p of posts) {
      if (p.area !== area) {
        area = p.area;
        rows.push(h("tr", {}, h("th", { colspan: 5, style: "text-transform:none;letter-spacing:0;font-size:.85rem" }, area || "Other")));
      }
      rows.push(...postRows(eventID, d, p, refresh));
    }
    sections.push(h("section", { class: "panel flush" },
      h("header", { style: "padding:12px 16px" }, h("h2", {}, shift === "first" ? "First shift" : "Second shift"),
        h("span", { class: "muted small" }, shift === "first" ? "Door posts redeploy at second shift; full-session posts stay" : "Filled from people freed at second shift")),
      h("div", { class: "tablewrap" }, h("table", {},
        h("thead", {}, h("tr", {}, h("th", {}, "Post"), h("th", {}, "Fill"), h("th", {}, "Person"),
          h("th", {}, shift === "first" ? "Second shift" : "First shift"), h("th", {}, ""))),
        h("tbody", {}, rows)))));
  }
  const loose = d.people.filter((p) => !p.first || (p.freed && !p.second));
  if (loose.length) {
    sections.push(h("section", { class: "panel stack" },
      h("h2", {}, "People without a post"),
      h("ul", { class: "small", style: "columns:3 16em;margin:0" }, loose.map((p) => h("li", {},
        p.name, " ", h("span", { class: "muted" }, !p.first ? "no post" : `freed from ${p.first}`))))));
  }
  return h("div", { class: "stack" }, sections);
}

function postRows(eventID, d, p, refresh) {
  const tags = [
    p.is_lead ? h("span", { class: "pill info" }, "lead") : null,
    p.paired ? h("span", { class: "pill info" }, "pair") : null,
    p.min_tenure_half_years ? h("span", { class: "pill" }, `min ${p.min_tenure_half_years / 2}y`) : null,
    p.resources.length ? h("span", { class: "muted small" }, p.resources.join(", ")) : null,
  ];
  const fillCls = p.occupants.length < p.headcount ? "pill bad" : p.occupants.length > p.headcount ? "pill info" : "pill";
  const editing = assignState.editing === p.position_id;
  const people = p.occupants.length ? p.occupants.map((o) => occupant(eventID, o, refresh)) : [h("span", { class: "muted" }, "—")];
  const add = h("button", { class: "link", onclick: () => { assignState.editing = editing ? null : p.position_id; refresh(); } },
    editing ? "cancel" : p.occupants.length ? "add / change" : "assign");
  const rows = [h("tr", {},
    h("td", {}, h("b", {}, p.name), " ", tags),
    h("td", {}, h("span", { class: fillCls }, p.fill)),
    h("td", {}, people.map((x) => h("div", { style: "margin-bottom:4px" }, x.main))),
    h("td", { class: "small" }, people.map((x) => h("div", { style: "margin-bottom:4px" }, x.then || h("span", { class: "muted" }, "—")))),
    h("td", { style: "white-space:nowrap" }, add))];
  if (editing) rows.push(h("tr", { class: "detail" }, h("td", { colspan: 5 }, placeForm(eventID, d, p, refresh))));
  return rows;
}

function occupant(eventID, o, refresh) {
  const flags = [
    o.pinned ? h("span", { class: "pill", title: o.source === "manual" ? "Placed by hand" : "Pinned: kept on re-run" }, o.source === "manual" ? "by hand" : "pinned") : null,
    o.needs_confirmation ? h("span", { class: "pill warn", title: "A gender in this roam pair is not recorded: check the pair on the night" }, "confirm pair") : null,
    o.overrides.filter((x) => x.kind !== "placement").map((x) =>
      h("span", { class: "pill warn", title: `${x.by}, ${x.at}${x.note ? " — " + x.note : ""}` }, OVERRIDE_LABEL[x.kind] || x.kind)),
  ];
  const remove = h("button", { class: "link", title: "Remove from this post" }, "remove");
  remove.onclick = () => confirmInline(remove, `Take ${o.name} off this post?`, "Remove", async () => {
    await api("DELETE", `/events/${eventID}/assignments/${o.assignment_id}`);
    refresh();
  });
  const pin = o.source === "matcher"
    ? h("button", { class: "link", onclick: async () => {
        await api("PUT", `/events/${eventID}/assignments/${o.assignment_id}/pin`, { pinned: !o.pinned }).catch(fail);
        refresh();
      } }, o.pinned ? "unpin" : "pin")
    : null;
  return {
    main: h("span", {}, o.name, " ", h("span", { class: "muted small" }, `${o.badge} · ${o.tenure_years}y`), " ", flags, " ",
      h("span", { class: "muted small", title: "Why the matcher chose them" }, o.reason), " ", pin, remove),
    then: o.then,
  };
}

// placeForm puts anyone on the roster on a post by hand. Nothing is
// refused: overrides of min tenure, restrictions, the lead default or the
// pairing rule are recorded and shown.
function placeForm(eventID, d, p, refresh) {
  const shiftOf = p.shift;
  const sorted = [...d.people].sort((a, b) => {
    const free = (x) => (shiftOf === "first" ? !x.first : x.freed && !x.second);
    return (free(b) - free(a)) || a.name.localeCompare(b.name);
  });
  const sel = h("select", { "aria-label": "Person" }, h("option", { value: "" }, "Choose a person…"),
    sorted.map((x) => {
      const now = shiftOf === "first" ? x.first : x.second;
      return h("option", { value: x.id }, `${x.name} (${x.badge}, ${x.tenure_years}y)${now ? " — now " + now : " — free"}`);
    }));
  const note = h("input", { placeholder: "Note (recorded)", size: 28, "aria-label": "Note" });
  return h("div", { class: "row" }, h("b", {}, `Put someone on ${p.name}:`), sel, note,
    h("button", { class: "primary", onclick: async () => {
      if (!sel.value) return;
      try {
        await api("POST", `/events/${eventID}/assignments`, { position_id: p.position_id, person_id: sel.value, shift: shiftOf, note: note.value });
        assignState.editing = null;
        toast("Placed");
        refresh();
      } catch (e) { fail(e); }
    } }, "Place"),
    h("span", { class: "muted small" }, "They leave any other post they hold in this shift. Over-filling a post is allowed."));
}

start();
