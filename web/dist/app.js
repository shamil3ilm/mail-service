// mail-service dashboard — vanilla JS, no build step, no external deps.
// Kept intentionally small so it can be reviewed end-to-end.

const API = "/api/v1";

// ── tiny helpers ──────────────────────────────────────────────────
const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

async function api(path, opts = {}) {
  const r = await fetch(API + path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  if (r.status === 401) {
    showLogin();
    throw new Error("unauthorized");
  }
  if (!r.ok) {
    const body = await r.text();
    throw new Error(`${r.status}: ${body}`);
  }
  return r.status === 204 ? null : r.json();
}

function fmtDate(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  const now = new Date();
  const sameDay = d.toDateString() === now.toDateString();
  if (sameDay) return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const sameYear = d.getFullYear() === now.getFullYear();
  return d.toLocaleDateString([], sameYear
    ? { month: "short", day: "numeric" }
    : { year: "numeric", month: "short", day: "numeric" });
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => (
    { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]
  ));
}

// ── screen switching ──────────────────────────────────────────────
function showLogin() {
  $("#app").classList.add("hidden");
  $("#login").classList.remove("hidden");
  $("#login-form [name=email]")?.focus();
}
function showApp() {
  $("#login").classList.add("hidden");
  $("#app").classList.remove("hidden");
}

// ── application state ─────────────────────────────────────────────
const state = {
  user: null,
  mailboxes: [],
  labels: [],
  threads: [],
  selectedMailbox: "",   // "" = all mail
  selectedLabel: "",     // "" = no label filter
  selectedThread: null,  // { thread, messages[], labels[] }
  searchQuery: "",       // "" = not searching, else the current query string
  sse: null,
};

// ── initial boot ──────────────────────────────────────────────────
async function boot() {
  try {
    const me = await api("/auth/me");
    state.user = me.user;
    $("#user-email").textContent = me.user.email;
    showApp();
    await Promise.all([refreshMailboxes(), refreshLabels()]);
    await refreshThreads();
    connectStream();
  } catch (e) {
    showLogin();
  }
}

// ── login ─────────────────────────────────────────────────────────
$("#login-form").addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const email = ev.target.email.value.trim();
  const password = ev.target.password.value;
  $("#login-error").classList.add("hidden");
  try {
    await api("/auth/login", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    });
    ev.target.password.value = "";
    await boot();
  } catch (e) {
    $("#login-error").textContent = "Invalid email or password.";
    $("#login-error").classList.remove("hidden");
  }
});

// ── logout ────────────────────────────────────────────────────────
$("#logout-btn").addEventListener("click", async () => {
  try {
    await api("/auth/logout", { method: "POST" });
  } catch {}
  disconnectStream();
  showLogin();
});

// ── mailboxes ─────────────────────────────────────────────────────
async function refreshMailboxes() {
  const r = await api("/mailboxes");
  state.mailboxes = r.mailboxes || [];
  renderMailboxes();
}

function renderMailboxes() {
  const ul = $("#mailbox-list");
  const items = [{ id: "", address: "All mail" }, ...state.mailboxes];
  ul.innerHTML = items.map((m) => `
    <li data-id="${esc(m.id)}" class="${m.id === state.selectedMailbox ? "active" : ""}">
      <span>${esc(m.address || m.display_name || "(unnamed)")}</span>
    </li>
  `).join("");
  ul.querySelectorAll("li").forEach((li) => {
    li.addEventListener("click", () => {
      state.selectedMailbox = li.dataset.id;
      $("#inbox-title").textContent =
        li.dataset.id === "" ? "All mail" : (li.querySelector("span")?.textContent || "");
      renderMailboxes();
      refreshThreads();
      $("#sidebar").classList.remove("open");
    });
  });
}

// ── labels: sidebar list + create ─────────────────────────────────
async function refreshLabels() {
  const r = await api("/labels");
  state.labels = r.labels || [];
  renderLabelSidebar();
}

function renderLabelSidebar() {
  const ul = $("#label-list");
  if (!state.labels.length) {
    ul.innerHTML = '<li class="muted small" style="padding:6px 12px">no labels</li>';
    return;
  }
  ul.innerHTML = state.labels.map((l) => `
    <li data-id="${esc(l.id)}" class="${l.id === state.selectedLabel ? "active" : ""}">
      <span class="label-dot" style="background:${esc(l.color)}"></span>
      <span>${esc(l.name)}</span>
    </li>
  `).join("");
  ul.querySelectorAll("li[data-id]").forEach((li) => {
    li.addEventListener("click", () => {
      // Selecting a label overrides the mailbox filter.
      state.selectedLabel = state.selectedLabel === li.dataset.id ? "" : li.dataset.id;
      state.selectedMailbox = "";
      $("#inbox-title").textContent = state.selectedLabel
        ? state.labels.find(l => l.id === state.selectedLabel)?.name || "Label"
        : "All mail";
      renderMailboxes();
      renderLabelSidebar();
      refreshThreads();
      $("#sidebar").classList.remove("open");
    });
  });
}

$("#new-label-btn").addEventListener("click", async () => {
  const name = await promptInline("New label name:", "");
  if (!name) return;
  try {
    await api("/labels", { method: "POST", body: JSON.stringify({ name }) });
    await refreshLabels();
  } catch (err) {
    alert("Could not create label: " + (err.message || err));
  }
});

// promptInline is a stand-in for window.prompt using our modal so browsers
// that block prompts still work. Reuses the compose modal transiently would
// be overkill; a tiny inline replacement is fine.
async function promptInline(message, initial) {
  return new Promise((resolve) => {
    const value = window.prompt(message, initial);
    resolve(value);
  });
}

// ── thread list ───────────────────────────────────────────────────
async function refreshThreads() {
  if (state.searchQuery) {
    const r = await api("/search?q=" + encodeURIComponent(state.searchQuery));
    state.threads = r.threads || [];
    renderThreads();
    return;
  }
  const parts = [];
  if (state.selectedLabel) {
    parts.push(`label_id=${encodeURIComponent(state.selectedLabel)}`);
  } else if (state.selectedMailbox) {
    parts.push(`mailbox_id=${encodeURIComponent(state.selectedMailbox)}`);
  }
  const qs = parts.length ? "?" + parts.join("&") : "";
  const r = await api("/threads" + qs);
  state.threads = r.threads || [];
  renderThreads();
}

function renderThreads() {
  const ul = $("#message-list");
  const empty = $("#inbox-empty");
  if (!state.threads.length) {
    ul.innerHTML = "";
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");
  ul.innerHTML = state.threads.map((t) => {
    const last = t.last_message || {};
    const from = esc(last.from_addr || (t.participants && t.participants[0]) || "");
    const subject = esc(last.subject || t.subject_normalized || "(no subject)");
    const preview = esc((last.to_addrs || []).join(", "));
    const countTag = t.message_count > 1 ? ` <span class="count">${t.message_count}</span>` : "";
    const labelsHTML = (t.labels || []).map((l) =>
      `<span class="label-chip" style="background:${esc(l.color)}">${esc(l.name)}</span>`
    ).join("");
    const labelsRow = labelsHTML ? `<div class="thread-labels">${labelsHTML}</div>` : "";
    return `
      <li data-id="${esc(t.id)}" class="${state.selectedThread?.thread.id === t.id ? "active" : ""}">
        <div class="msg-from">${from}${countTag} <span class="msg-date">${esc(fmtDate(t.last_at))}</span></div>
        <div class="msg-subject">${subject}</div>
        <div class="msg-preview">${preview}</div>
        ${labelsRow}
      </li>
    `;
  }).join("");
  ul.querySelectorAll("li").forEach((li) => {
    li.addEventListener("click", () => openThread(li.dataset.id));
  });
}

// Mailpit-style view state (persists across thread opens in-session).
// viewTab: "html" | "source" | "text" | "headers" | "raw"
// viewport: "mobile" | "tablet" | "desktop"
if (!("viewTab"    in state)) state.viewTab    = "html";
if (!("viewport"   in state)) state.viewport   = "desktop";
if (!("showImages" in state)) state.showImages = false;

function fmtSize(n) {
  if (n == null) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

// ── thread detail ─────────────────────────────────────────────────
async function openThread(id) {
  try {
    const thread = state.threads.find((t) => t.id === id);
    const r = await api(`/threads/${encodeURIComponent(id)}/messages`);
    const messages = r.messages || [];
    state.selectedThread = { thread, messages, contents: null };
    renderThreads();

    // Detail header shows the (Gmail-style) latest subject + participants.
    const last = messages[messages.length - 1] || {};
    $("#detail-subject").textContent = last.subject || "(no subject)";
    $("#detail-from").textContent = (thread?.participants || []).slice(0, 6).join(", ");
    $("#detail-to").textContent = `${messages.length} message${messages.length === 1 ? "" : "s"}`;
    $("#detail-date").textContent = new Date(last.received_at).toLocaleString();
    $("#detail-raw").href = `${API}/messages/${encodeURIComponent(last.id)}/raw`;

    // Fetch parsed content per message (text + sanitised HTML in one call).
    const contents = await Promise.all(messages.map((m) =>
      api(`/messages/${encodeURIComponent(m.id)}/content`)
    ));
    state.selectedThread.contents = contents;

    // Size shown in the tab bar (sum across all messages in the thread).
    const totalSize = contents.reduce((n, c) => n + (c.size || 0), 0);
    $("#detail-size").textContent = fmtSize(totalSize);

    // If none of the messages have HTML, disable the HTML + Source tabs
    // and land the user on Text by default.
    const anyHTML = contents.some((c) => c.has_html);
    const htmlTab   = document.querySelector('.tab[data-tab="html"]');
    const sourceTab = document.querySelector('.tab[data-tab="source"]');
    htmlTab.disabled   = !anyHTML;
    sourceTab.disabled = !anyHTML;
    if (!anyHTML && (state.viewTab === "html" || state.viewTab === "source")) {
      state.viewTab = "text";
    }

    updateImagesToggleVisibility();
    applyViewTabActive();
    applyViewportActive();
    renderThreadIframe();

    // Load and render labels attached to this thread.
    const lr = await api(`/threads/${encodeURIComponent(id)}/labels`);
    state.selectedThread.labels = lr.labels || [];
    renderDetailLabels();

    // Aggregate attachments across every message in the thread.
    renderThreadAttachments(messages);

    $("#detail").classList.remove("hidden");
    $("#detail").classList.add("open");
  } catch (err) {
    console.error("openThread failed", err);
    alert("Failed to open thread: " + (err.message || err));
  }
}

// renderThreadIframe (re-)renders the iframe body from state.selectedThread
// using the currently-selected tab (state.viewTab). Called on open and
// whenever the user switches tabs / toggles images / swaps viewport.
function renderThreadIframe() {
  const t = state.selectedThread;
  if (!t) return;

  const stacked = t.messages.map((m, i) => {
    const c = t.contents?.[i] || {};
    const heading =
      `From: ${esc(m.from_addr)}<br>` +
      `To: ${esc((m.to_addrs || []).join(", "))}<br>` +
      `Date: ${esc(new Date(m.received_at).toLocaleString())}<br>` +
      `Subject: ${esc(m.subject || "(no subject)")}`;

    const bodyHTML = renderMessageBody(c);
    return (
      '<section class="thread-msg">' +
        '<header>' + heading + '</header>' +
        '<div class="body">' + bodyHTML + '</div>' +
      '</section>'
    );
  }).join("");

  const iframeCSS =
    'html,body{margin:0;padding:0;background:#fff;color:#111;font:13px system-ui,Segoe UI,Roboto,sans-serif;}' +
    '@media (prefers-color-scheme:dark){html,body{background:#14171c;color:#e7ebf0;}' +
      '.thread-msg{border-color:#232830 !important;}' +
      '.thread-msg header{background:#0b0d10 !important;}' +
    '}' +
    '.thread-msg{margin:12px;border:1px solid #e5e7eb;border-radius:6px;overflow:hidden;}' +
    '.thread-msg header{padding:10px 14px;background:#f7f8fa;font-size:12px;line-height:1.5;color:#6b7280;}' +
    '.thread-msg .body{padding:14px;line-height:1.5;}' +
    '.thread-msg .body pre{font:12px ui-monospace,Menlo,Consolas,monospace;white-space:pre-wrap;word-break:break-word;margin:0;}' +
    '.thread-msg .body img{max-width:100%;height:auto;}' +
    '.thread-msg .body table{max-width:100%;}' +
    '.thread-msg .body blockquote{border-left:3px solid #d1d5db;margin:0 0 0 4px;padding:0 0 0 12px;color:#6b7280;}' +
    'table.headers{border-collapse:collapse;width:100%;font-size:12px;}' +
    'table.headers td{padding:4px 8px;border-bottom:1px solid #e5e7eb;vertical-align:top;}' +
    'table.headers td:first-child{font-weight:600;white-space:nowrap;color:#6b7280;}';

  $("#detail-frame").srcdoc =
    '<!doctype html><html><head><meta charset="utf-8"><style>' + iframeCSS + '</style></head>' +
    '<body>' + stacked + '</body></html>';
}

// renderMessageBody picks the view based on state.viewTab:
//   html    → sanitised HTML (with image un-block honored)
//   source  → HTML source code as text
//   text    → text/plain alternative
//   headers → parsed headers table
//   raw     → full raw .eml as text
function renderMessageBody(content) {
  if (!content) return '<pre></pre>';
  switch (state.viewTab) {
    case "source": {
      const src = content.html_source || content.html || "";
      return '<pre>' + esc(src) + '</pre>';
    }
    case "text":
      return '<pre>' + esc(content.text || "") + '</pre>';
    case "headers": {
      const rows = (content.headers || []).map((h) =>
        `<tr><td>${esc(h.name)}</td><td>${esc(h.value)}</td></tr>`
      ).join("");
      return '<table class="headers"><tbody>' + rows + '</tbody></table>';
    }
    case "raw":
      return '<pre>' + esc(content.raw || "") + '</pre>';
    case "html":
    default: {
      if (!content.has_html || !content.html) {
        // Fall through to text if HTML isn't available in this message.
        return '<pre>' + esc(content.text || "") + '</pre>';
      }
      let html = content.html;
      if (state.showImages) {
        html = html.replace(
          /<img\b[^>]*\bdata-blocked-src="([^"]*)"[^>]*>/gi,
          (_m, src) => `<img src="${esc(src)}">`
        );
      }
      return html;
    }
  }
}

function updateImagesToggleVisibility() {
  const contents = state.selectedThread?.contents || [];
  const anyBlocked = contents.some((c) =>
    c.has_html && (c.html || "").includes("data-blocked-src")
  );
  const btn = $("#detail-images-toggle");
  btn.classList.toggle("hidden", !anyBlocked || state.viewTab !== "html");
  btn.textContent = state.showImages ? "Hide images" : "Show images";
}

function applyViewTabActive() {
  document.querySelectorAll(".detail-tabs .tab").forEach((el) => {
    el.classList.toggle("active", el.dataset.tab === state.viewTab);
  });
}

function applyViewportActive() {
  document.querySelectorAll(".viewport-picker .vp").forEach((el) => {
    el.classList.toggle("active", el.dataset.vp === state.viewport);
  });
  const body = document.querySelector(".detail-body");
  if (body) {
    body.classList.remove("vp-mobile", "vp-tablet", "vp-desktop");
    body.classList.add("vp-" + state.viewport);
  }
}

document.querySelectorAll(".detail-tabs .tab").forEach((btn) => {
  btn.addEventListener("click", () => {
    if (btn.disabled) return;
    state.viewTab = btn.dataset.tab;
    applyViewTabActive();
    updateImagesToggleVisibility();
    renderThreadIframe();
  });
});

document.querySelectorAll(".viewport-picker .vp").forEach((btn) => {
  btn.addEventListener("click", () => {
    state.viewport = btn.dataset.vp;
    applyViewportActive();
  });
});

$("#detail-images-toggle").addEventListener("click", () => {
  state.showImages = !state.showImages;
  updateImagesToggleVisibility();
  renderThreadIframe();
});

function renderThreadAttachments(messages) {
  const strip = $("#thread-attach-strip");
  const chips = [];
  for (const m of messages || []) {
    for (const a of m.attachments || []) {
      chips.push(`
        <a class="att-chip"
           href="${API}/messages/${encodeURIComponent(m.id)}/attachments/${encodeURIComponent(a.id)}"
           download="${esc(a.filename)}">
          📎 ${esc(a.filename)}
          <span class="att-size">${esc(fmtBytes(a.size))}</span>
        </a>
      `);
    }
  }
  strip.innerHTML = chips.join("");
}

function renderDetailLabels() {
  const container = $("#detail-labels");
  const labels = state.selectedThread?.labels || [];
  container.innerHTML = labels.map((l) => `
    <span class="label-chip" style="background:${esc(l.color)}" data-id="${esc(l.id)}">
      ${esc(l.name)}
      <span class="x" title="Remove">×</span>
    </span>
  `).join("");
  container.querySelectorAll(".label-chip .x").forEach((x) => {
    x.addEventListener("click", async (e) => {
      const chip = e.target.closest(".label-chip");
      await removeLabelFromCurrentThread(chip.dataset.id);
    });
  });
}

// ── label picker popover (per-thread) ─────────────────────────────
let pickerEl = null;
$("#detail-label-btn").addEventListener("click", (ev) => {
  if (pickerEl) { closePicker(); return; }
  openPicker(ev.currentTarget);
});
document.addEventListener("click", (ev) => {
  if (pickerEl && !pickerEl.contains(ev.target) && ev.target.id !== "detail-label-btn") {
    closePicker();
  }
});

function closePicker() { pickerEl?.remove(); pickerEl = null; }

function openPicker(anchor) {
  closePicker();
  const active = new Set((state.selectedThread?.labels || []).map((l) => l.id));
  const el = document.createElement("div");
  el.id = "label-picker";
  el.innerHTML = state.labels.length
    ? state.labels.map((l) => `
        <div class="picker-item ${active.has(l.id) ? "on" : ""}" data-id="${esc(l.id)}">
          <span class="label-dot" style="background:${esc(l.color)}"></span>
          <span>${esc(l.name)}</span>
          <span class="checkmark">✓</span>
        </div>
      `).join("")
    : '<div class="muted small" style="padding:6px 10px">no labels — create one from the sidebar</div>';

  const rect = anchor.getBoundingClientRect();
  el.style.top = (rect.bottom + 4) + "px";
  el.style.left = Math.max(8, rect.right - 200) + "px";
  document.body.appendChild(el);
  pickerEl = el;

  el.querySelectorAll(".picker-item").forEach((item) => {
    item.addEventListener("click", async () => {
      const id = item.dataset.id;
      if (active.has(id)) {
        await removeLabelFromCurrentThread(id);
      } else {
        await addLabelToCurrentThread(id);
      }
      closePicker();
    });
  });
}

async function addLabelToCurrentThread(labelID) {
  const t = state.selectedThread?.thread;
  if (!t) return;
  await api(`/threads/${encodeURIComponent(t.id)}/labels/${encodeURIComponent(labelID)}`, {
    method: "POST",
  });
  const label = state.labels.find((l) => l.id === labelID);
  if (label) state.selectedThread.labels = [...(state.selectedThread.labels || []), label];
  renderDetailLabels();
  refreshThreads(); // updates chips in the inbox list
}

async function removeLabelFromCurrentThread(labelID) {
  const t = state.selectedThread?.thread;
  if (!t) return;
  await api(`/threads/${encodeURIComponent(t.id)}/labels/${encodeURIComponent(labelID)}`, {
    method: "DELETE",
  });
  state.selectedThread.labels = (state.selectedThread.labels || []).filter((l) => l.id !== labelID);
  renderDetailLabels();
  refreshThreads();
}

$("#back-btn").addEventListener("click", () => {
  $("#detail").classList.remove("open");
});

// ── reply / reply-all / forward ────────────────────────────────────
// All three operate on the LATEST message in the open thread — matches
// Gmail. If you want to reply to an older message specifically, open its
// raw .eml — a per-message reply UI is a v2 refinement.

function latestInThread() {
  const msgs = state.selectedThread?.messages || [];
  return msgs[msgs.length - 1] || null;
}

function ensureRePrefix(subject) {
  if (!subject) return "Re: (no subject)";
  return /^re:\s*/i.test(subject) ? subject : "Re: " + subject;
}
function ensureFwdPrefix(subject) {
  if (!subject) return "Fwd: (no subject)";
  return /^(fwd?|fw):\s*/i.test(subject) ? subject : "Fwd: " + subject;
}

function quoteBody(m, rawText) {
  const when = new Date(m.received_at).toLocaleString();
  const quoted = String(rawText || "")
    .split("\n")
    .map((line) => "> " + line)
    .join("\n");
  return `\n\nOn ${when}, ${m.from_addr} wrote:\n${quoted}\n`;
}

// buildReferences returns the References header value for a reply. Per
// RFC 5322 §3.6.4: append the parent's Message-ID to the parent's
// References list (creating one if absent).
function buildReferences(parent) {
  const existing = (parent.references || []).slice();
  if (parent.message_id) existing.push(parent.message_id);
  return existing;
}

async function replyToLatest(replyAll) {
  const parent = latestInThread();
  if (!parent) return;
  const raw = await fetch(`${API}/messages/${encodeURIComponent(parent.id)}/raw`, {
    credentials: "same-origin",
  }).then((r) => r.text());
  // Extract the body portion of the raw MIME for a cleaner quote.
  const bodyText = raw.includes("\r\n\r\n") ? raw.split(/\r?\n\r?\n/).slice(1).join("\n\n") : raw;

  const me = state.user?.email || "";
  const to = [parent.from_addr];
  let cc = [];
  if (replyAll) {
    // Include everyone else, filter out the current user's address so we
    // don't reply-to-ourselves.
    const everyone = [...(parent.to_addrs || []), ...(parent.cc_addrs || [])];
    cc = Array.from(new Set(everyone.map((a) => a.toLowerCase())))
      .filter((a) => a && a !== me.toLowerCase() && a !== parent.from_addr.toLowerCase());
  }

  openCompose({
    from:       me || parent.to_addrs?.[0] || "",
    to:         to.join(", "),
    cc:         cc.join(", "),
    subject:    ensureRePrefix(parent.subject),
    text:       quoteBody(parent, bodyText),
    inReplyTo:  parent.message_id,
    references: buildReferences(parent),
    focus:      "body",
  });
}

async function forwardLatest() {
  const parent = latestInThread();
  if (!parent) return;
  const raw = await fetch(`${API}/messages/${encodeURIComponent(parent.id)}/raw`, {
    credentials: "same-origin",
  }).then((r) => r.text());
  const bodyText = raw.includes("\r\n\r\n") ? raw.split(/\r?\n\r?\n/).slice(1).join("\n\n") : raw;

  const header =
    `\n\n---------- Forwarded message ----------\n` +
    `From: ${parent.from_addr}\n` +
    `Date: ${new Date(parent.received_at).toLocaleString()}\n` +
    `Subject: ${parent.subject || "(no subject)"}\n` +
    `To: ${(parent.to_addrs || []).join(", ")}\n\n` +
    bodyText + "\n";

  openCompose({
    from:    state.user?.email || parent.to_addrs?.[0] || "",
    to:      "",
    subject: ensureFwdPrefix(parent.subject),
    text:    header,
    // No In-Reply-To / References — forwards intentionally start a new thread.
    focus:   "to",
  });
}

$("#detail-reply").addEventListener("click", () => replyToLatest(false));
$("#detail-reply-all").addEventListener("click", () => replyToLatest(true));
$("#detail-forward").addEventListener("click", forwardLatest);

$("#detail-delete").addEventListener("click", async () => {
  if (!state.selectedThread) return;
  const msgs = state.selectedThread.messages;
  if (!msgs.length) return;
  if (!confirm(`Delete this thread's ${msgs.length} message(s)?`)) return;
  await Promise.all(msgs.map((m) =>
    api("/messages/" + encodeURIComponent(m.id), { method: "DELETE" })
  ));
  state.selectedThread = null;
  $("#detail").classList.add("hidden");
  $("#detail").classList.remove("open");
  await refreshThreads();
});

$("#refresh-btn").addEventListener("click", refreshThreads);

// ── search input ──────────────────────────────────────────────────
// Debounced search-as-you-type. 250ms is short enough to feel live and
// long enough to skip the "typing storm" that happens per keystroke.
const searchInput = $("#search-input");
const searchClear = $("#search-clear");
let searchTimer = null;

searchInput.addEventListener("input", () => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => {
    const q = searchInput.value.trim();
    state.searchQuery = q;
    searchClear.classList.toggle("hidden", q === "");
    refreshThreads();
  }, 250);
});

searchInput.addEventListener("keydown", (ev) => {
  if (ev.key === "Escape") {
    searchInput.value = "";
    state.searchQuery = "";
    searchClear.classList.add("hidden");
    refreshThreads();
    searchInput.blur();
  }
});

searchClear.addEventListener("click", () => {
  searchInput.value = "";
  state.searchQuery = "";
  searchClear.classList.add("hidden");
  refreshThreads();
  searchInput.focus();
});

// ── sidebar toggle (mobile) ───────────────────────────────────────
$("#menu-toggle").addEventListener("click", () => {
  $("#sidebar").classList.toggle("open");
});

// ── compose modal ─────────────────────────────────────────────────
const composeEl = $("#compose");
const composeForm = $("#compose-form");
const composeErr = $("#compose-error");
const composeStatus = $("#compose-status");
const composeSend = $("#compose-send");

// stagedFiles is the list of File objects the user has attached but not yet
// sent. Reset each time compose opens.
let stagedFiles = [];

function openCompose(prefill = {}) {
  composeErr.classList.add("hidden");
  composeStatus.textContent = "";
  composeForm.reset();
  stagedFiles = [];
  renderStagedAttachments();
  // Threading metadata isn't a visible field; stash on the form's dataset so
  // the submit handler can inject In-Reply-To / References headers.
  composeForm.dataset.inReplyTo = prefill.inReplyTo || "";
  composeForm.dataset.references = (prefill.references || []).join(" ");

  if (prefill.from)    composeForm.from.value = prefill.from;
  if (prefill.to)      composeForm.to.value = prefill.to;
  if (prefill.cc)      composeForm.cc.value = prefill.cc;
  if (prefill.subject) composeForm.subject.value = prefill.subject;
  if (prefill.text)    composeForm.text.value = prefill.text;

  composeEl.classList.remove("hidden");
  // Focus intelligently: reply → body, new/forward → recipient.
  if (prefill.focus === "body") {
    composeForm.text.focus();
    composeForm.text.setSelectionRange(0, 0); // put cursor at top
  } else {
    composeForm.to.focus();
  }
}
function closeCompose() { composeEl.classList.add("hidden"); }

$("#compose-btn").addEventListener("click", () => openCompose());
$("#compose-close").addEventListener("click", closeCompose);
composeEl.addEventListener("click", (e) => { if (e.target === composeEl) closeCompose(); });
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !composeEl.classList.contains("hidden")) closeCompose();
});

// ── settings drawer + api keys ────────────────────────────────────
const settingsEl = $("#settings");
$("#settings-btn").addEventListener("click", () => openSettings());
$("#settings-close").addEventListener("click", () => settingsEl.classList.add("hidden"));
settingsEl.addEventListener("click", (e) => { if (e.target === settingsEl) settingsEl.classList.add("hidden"); });

async function openSettings() {
  settingsEl.classList.remove("hidden");
  await Promise.all([refreshKeys(), refreshDomains(), refreshSuppressions()]);
}

async function refreshKeys() {
  const r = await api("/keys");
  const ul = $("#key-list");
  const empty = $("#key-empty");
  const keys = r.keys || [];
  if (!keys.length) {
    ul.innerHTML = "";
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");
  ul.innerHTML = keys.map((k) => `
    <li data-id="${esc(k.id)}">
      <div class="key-main">
        <div class="key-name">${esc(k.name)}</div>
        <div class="key-meta">
          <code>${esc(k.prefix)}…</code>
          · created ${esc(fmtDate(k.created_at))}
          ${k.last_used_at ? "· last used " + esc(fmtDate(k.last_used_at)) : "· never used"}
        </div>
      </div>
      <button class="danger-btn" data-action="revoke">Revoke</button>
    </li>
  `).join("");
  ul.querySelectorAll('[data-action="revoke"]').forEach((btn) => {
    btn.addEventListener("click", async (e) => {
      const li = e.target.closest("li");
      if (!confirm(`Revoke this key? It will stop working immediately.`)) return;
      await api("/keys/" + encodeURIComponent(li.dataset.id), { method: "DELETE" });
      await refreshKeys();
    });
  });
}

const newKeyForm = $("#new-key-form");
const newKeyErr = $("#new-key-error");
newKeyForm.addEventListener("submit", async (ev) => {
  ev.preventDefault();
  newKeyErr.classList.add("hidden");
  const name = newKeyForm.name.value.trim();
  if (!name) return;
  try {
    const resp = await api("/keys", {
      method: "POST",
      body: JSON.stringify({ name }),
    });
    newKeyForm.reset();
    await refreshKeys();
    revealKey(resp.token);
  } catch (err) {
    newKeyErr.textContent = String(err.message || err).replace(/^\d+:\s*/, "");
    newKeyErr.classList.remove("hidden");
  }
});

function revealKey(token) {
  $("#key-reveal-token").textContent = token;
  $("#key-copy-status").textContent = "";
  $("#key-reveal").classList.remove("hidden");
}
$("#key-reveal-close").addEventListener("click", () => $("#key-reveal").classList.add("hidden"));
$("#key-copy-btn").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText($("#key-reveal-token").textContent);
    $("#key-copy-status").textContent = "Copied ✓";
  } catch {
    $("#key-copy-status").textContent = "Copy failed — select the text and copy manually.";
  }
});

// ── domains ────────────────────────────────────────────────────────
const newDomainForm = $("#new-domain-form");
const newDomainErr = $("#new-domain-error");

newDomainForm.addEventListener("submit", async (ev) => {
  ev.preventDefault();
  newDomainErr.classList.add("hidden");
  const name = newDomainForm.name.value.trim().toLowerCase();
  if (!name) return;
  try {
    await api("/domains", { method: "POST", body: JSON.stringify({ name }) });
    newDomainForm.reset();
    await refreshDomains();
  } catch (err) {
    newDomainErr.textContent = String(err.message || err).replace(/^\d+:\s*/, "");
    newDomainErr.classList.remove("hidden");
  }
});

async function refreshDomains() {
  const r = await api("/domains");
  const ul = $("#domain-list");
  const empty = $("#domain-empty");
  const domains = r.domains || [];
  if (!domains.length) {
    ul.innerHTML = "";
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");
  ul.innerHTML = domains.map(renderDomain).join("");
  ul.querySelectorAll('[data-action="verify"]').forEach((btn) => {
    btn.addEventListener("click", () => verifyDomain(btn.dataset.id));
  });
  ul.querySelectorAll('[data-action="delete-domain"]').forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm(`Delete ${btn.dataset.name}? DKIM key will be lost.`)) return;
      await api("/domains/" + encodeURIComponent(btn.dataset.id), { method: "DELETE" });
      await refreshDomains();
    });
  });
  ul.querySelectorAll(".copy-btn").forEach((btn) => {
    btn.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(btn.dataset.copy);
        const orig = btn.textContent;
        btn.textContent = "✓";
        setTimeout(() => { btn.textContent = orig; }, 900);
      } catch {
        btn.textContent = "!";
      }
    });
  });
}

function renderDomain(d, verdicts) {
  const kinds = { spf: !!d.spf_verified_at, dkim: !!d.dkim_verified_at,
                  dmarc: !!d.dmarc_verified_at, mx: !!d.mx_verified_at };
  const overallOk = Object.values(kinds).every(Boolean);
  const chip = (label, ok) => `<span class="chip ${ok ? "ok" : "bad"}">${label} ${ok ? "✓" : "✗"}</span>`;

  const verdictByKind = {};
  (d.verdicts || verdicts || []).forEach(v => verdictByKind[v.kind] = v);

  const rowsHTML = (d.records || []).map((rec) => {
    const v = verdictByKind[rec.kind];
    let status = kinds[rec.kind] ? '<span class="chip ok">verified</span>' :
                  (v && v.error ? `<span class="chip bad">${esc(v.error)}</span>` :
                                  '<span class="chip">unverified</span>');
    const valueForCopy = rec.type === "MX" ? `${rec.priority} ${rec.value}` : rec.value;
    const valueDisplay = rec.type === "MX" ? `${rec.priority}  ${esc(rec.value)}` : esc(rec.value);
    return `
      <tr>
        <td><strong>${esc(rec.type)}</strong></td>
        <td>
          <code>${esc(rec.name)}</code>
          <button class="copy-btn" data-copy="${esc(rec.name)}" title="Copy name">⧉</button>
        </td>
        <td class="dns-value">
          <code>${valueDisplay}</code>
          <button class="copy-btn" data-copy="${esc(valueForCopy)}" title="Copy value">⧉</button>
        </td>
        <td>${status}</td>
      </tr>
    `;
  }).join("");

  return `
    <li data-id="${esc(d.id)}">
      <div class="domain-head">
        <span class="domain-name">${esc(d.name)}</span>
        ${chip("SPF", kinds.spf)}
        ${chip("DKIM", kinds.dkim)}
        ${chip("DMARC", kinds.dmarc)}
        ${chip("MX", kinds.mx)}
        ${overallOk ? '<span class="chip ok">ready</span>' : ""}
      </div>
      <table class="dns-table">
        <thead><tr><th>Type</th><th>Name</th><th>Value</th><th>Status</th></tr></thead>
        <tbody>${rowsHTML}</tbody>
      </table>
      <div class="dns-actions">
        <button class="ghost-btn" data-action="verify" data-id="${esc(d.id)}">Verify DNS</button>
        <button class="danger-btn" data-action="delete-domain" data-id="${esc(d.id)}" data-name="${esc(d.name)}">Delete</button>
      </div>
    </li>
  `;
}

// ── suppressions ───────────────────────────────────────────────────
const newSuppForm = $("#new-supp-form");
const newSuppErr = $("#new-supp-error");

newSuppForm.addEventListener("submit", async (ev) => {
  ev.preventDefault();
  newSuppErr.classList.add("hidden");
  const address = newSuppForm.address.value.trim().toLowerCase();
  if (!address) return;
  try {
    await api("/suppressions", {
      method: "POST",
      body: JSON.stringify({ address, reason: "manual" }),
    });
    newSuppForm.reset();
    await refreshSuppressions();
  } catch (err) {
    newSuppErr.textContent = String(err.message || err).replace(/^\d+:\s*/, "");
    newSuppErr.classList.remove("hidden");
  }
});

async function refreshSuppressions() {
  const r = await api("/suppressions");
  const ul = $("#supp-list");
  const empty = $("#supp-empty");
  const items = r.suppressions || [];
  if (!items.length) {
    ul.innerHTML = "";
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");
  ul.innerHTML = items.map((s) => `
    <li data-address="${esc(s.address)}">
      <div class="key-main">
        <div class="key-name">${esc(s.address)}</div>
        <div class="key-meta">
          <span class="chip ${s.reason === 'manual' ? '' : 'bad'}">${esc(s.reason)}</span>
          · added ${esc(fmtDate(s.created_at))}
          ${s.source ? "· " + esc(s.source) : ""}
        </div>
      </div>
      <button class="danger-btn" data-action="unsupp">Remove</button>
    </li>
  `).join("");
  ul.querySelectorAll('[data-action="unsupp"]').forEach((btn) => {
    btn.addEventListener("click", async (e) => {
      const li = e.target.closest("li");
      await api("/suppressions/" + encodeURIComponent(li.dataset.address), {
        method: "DELETE",
      });
      await refreshSuppressions();
    });
  });
}

async function verifyDomain(id) {
  const btn = document.querySelector(`[data-action="verify"][data-id="${CSS.escape(id)}"]`);
  if (btn) { btn.disabled = true; btn.textContent = "Checking…"; }
  try {
    await api(`/domains/${encodeURIComponent(id)}/verify`, { method: "POST" });
    await refreshDomains();
  } finally {
    if (btn) { btn.disabled = false; btn.textContent = "Verify DNS"; }
  }
}

// ── compose attachments ───────────────────────────────────────────
const MAX_ATTACH_BYTES = 25 * 1024 * 1024; // matches server cap

function fmtBytes(n) {
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

function renderStagedAttachments() {
  const ul = $("#compose-attachments");
  const status = $("#compose-attach-status");
  if (!stagedFiles.length) {
    ul.innerHTML = "";
    status.textContent = "";
    return;
  }
  const total = stagedFiles.reduce((n, f) => n + f.size, 0);
  status.textContent = `${stagedFiles.length} file(s), ${fmtBytes(total)} total`;
  ul.innerHTML = stagedFiles.map((f, i) => `
    <li data-idx="${i}">
      <span class="name">📎 ${esc(f.name)}</span>
      <span class="size">${esc(fmtBytes(f.size))}</span>
      <button type="button" class="remove" title="Remove">×</button>
    </li>
  `).join("");
  ul.querySelectorAll(".remove").forEach((btn) => {
    btn.addEventListener("click", (e) => {
      const idx = Number(e.target.closest("li").dataset.idx);
      stagedFiles.splice(idx, 1);
      renderStagedAttachments();
    });
  });
}

$("#compose-files").addEventListener("change", (ev) => {
  for (const file of ev.target.files) {
    if (file.size > MAX_ATTACH_BYTES) {
      alert(`${file.name} is larger than 25MB. Skipping.`);
      continue;
    }
    stagedFiles.push(file);
  }
  // Reset input so re-picking the same file fires 'change' again.
  ev.target.value = "";
  renderStagedAttachments();
});

// readAsBase64 returns just the payload — no "data:mime;base64," prefix.
function readAsBase64(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => {
      const dataURL = r.result;
      const i = dataURL.indexOf(",");
      resolve(i >= 0 ? dataURL.slice(i + 1) : dataURL);
    };
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

composeForm.addEventListener("submit", async (ev) => {
  ev.preventDefault();
  composeErr.classList.add("hidden");
  composeSend.disabled = true;
  composeStatus.textContent = stagedFiles.length ? "Encoding attachments…" : "Sending…";

  const splitList = (v) => v.split(",").map((s) => s.trim()).filter(Boolean);
  const payload = {
    from:    composeForm.from.value.trim(),
    to:      splitList(composeForm.to.value),
    subject: composeForm.subject.value.trim(),
    text:    composeForm.text.value,
  };
  const cc = splitList(composeForm.cc.value);
  if (cc.length) payload.cc = cc;

  // Encode each attachment. FileReader is async so we wait on all in parallel.
  if (stagedFiles.length) {
    payload.attachments = await Promise.all(stagedFiles.map(async (f) => ({
      filename:     f.name,
      content_type: f.type || "application/octet-stream",
      content:      await readAsBase64(f),
    })));
    composeStatus.textContent = "Sending…";
  }

  // Threading headers — passed through /emails via the headers map so the
  // resulting message chains correctly in every recipient's client.
  const inReplyTo = composeForm.dataset.inReplyTo || "";
  const references = composeForm.dataset.references || "";
  if (inReplyTo || references) {
    payload.headers = {};
    if (inReplyTo)  payload.headers["In-Reply-To"] = wrapAngle(inReplyTo);
    if (references) payload.headers["References"]  = references
      .split(/\s+/).filter(Boolean).map(wrapAngle).join(" ");
  }

  try {
    const resp = await api("/emails", { method: "POST", body: JSON.stringify(payload) });
    composeStatus.textContent = `Sent ✓ (${resp.accepted.length} accepted${resp.rejected?.length ? `, ${resp.rejected.length} rejected` : ""})`;
    setTimeout(closeCompose, 800);
    await refreshMailboxes();
    await refreshThreads();
  } catch (err) {
    composeErr.textContent = String(err.message || err).replace(/^\d+:\s*/, "");
    composeErr.classList.remove("hidden");
    composeStatus.textContent = "";
  } finally {
    composeSend.disabled = false;
  }
});

function wrapAngle(id) {
  id = id.trim().replace(/^<|>$/g, "");
  return "<" + id + ">";
}

// ── live stream via SSE ───────────────────────────────────────────
function connectStream() {
  disconnectStream();
  const es = new EventSource(API + "/stream");
  state.sse = es;

  es.addEventListener("hello", () => setLive(true));
  es.addEventListener("message.received", async (ev) => {
    const evt = JSON.parse(ev.data);
    if (state.selectedMailbox === "" || evt.mailbox_id === state.selectedMailbox) {
      await refreshThreads();
      // If the currently open thread got a new message, refresh the detail pane.
      if (state.selectedThread &&
          evt.mailbox_id &&
          state.selectedThread.messages.some((m) => m.mailbox_id === evt.mailbox_id)) {
        openThread(state.selectedThread.thread.id);
      }
    }
    // Add mailbox if it's new — happens with auto-provisioned inboxes.
    if (!state.mailboxes.some((m) => m.id === evt.mailbox_id)) {
      await refreshMailboxes();
    }
  });
  es.addEventListener("message.deleted", () => refreshThreads());

  es.onerror = () => {
    setLive(false);
    // EventSource reconnects automatically; nothing more to do.
  };
}

function disconnectStream() {
  if (state.sse) {
    state.sse.close();
    state.sse = null;
  }
  setLive(false);
}

function setLive(on) {
  const dot = $("#live-dot");
  dot.classList.toggle("live", !!on);
  dot.title = on ? "live" : "disconnected";
}

// ── keyboard shortcuts ────────────────────────────────────────────
// Gmail-style: j/k to navigate, r to reply, etc. Guards against firing
// while the user is typing in an input, textarea, or contenteditable.
function isTypingContext(el) {
  if (!el) return false;
  const tag = el.tagName;
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return true;
  if (el.isContentEditable) return true;
  return false;
}

function anyModalOpen() {
  return !$("#compose").classList.contains("hidden") ||
         !$("#settings").classList.contains("hidden") ||
         !$("#shortcuts-help").classList.contains("hidden") ||
         !$("#key-reveal").classList.contains("hidden");
}

function selectedThreadIndex() {
  return state.threads.findIndex((t) => t.id === state.selectedThread?.thread.id);
}

function moveSelection(delta) {
  if (!state.threads.length) return;
  const cur = selectedThreadIndex();
  const next = Math.max(0, Math.min(state.threads.length - 1, cur === -1 ? 0 : cur + delta));
  const target = state.threads[next];
  openThread(target.id);
  // Ensure the row is visible in the inbox list.
  const li = document.querySelector(`#message-list li[data-id="${CSS.escape(target.id)}"]`);
  li?.scrollIntoView({ block: "nearest" });
}

document.addEventListener("keydown", (ev) => {
  if (ev.ctrlKey || ev.metaKey || ev.altKey) return;
  if (isTypingContext(ev.target)) return;

  // Global keys that work even with modals open
  if (ev.key === "Escape") {
    if (!$("#shortcuts-help").classList.contains("hidden")) {
      $("#shortcuts-help").classList.add("hidden"); return;
    }
    if (!$("#settings").classList.contains("hidden")) {
      $("#settings").classList.add("hidden"); return;
    }
    if (!$("#compose").classList.contains("hidden")) {
      closeCompose(); return;
    }
    if (!$("#detail").classList.contains("hidden")) {
      $("#detail").classList.remove("open");
      $("#detail").classList.add("hidden");
      state.selectedThread = null;
      renderThreads();
      return;
    }
  }

  if (anyModalOpen()) return;

  switch (ev.key) {
    case "?":
      ev.preventDefault();
      $("#shortcuts-help").classList.remove("hidden");
      return;
    case "j":
      ev.preventDefault(); moveSelection(1); return;
    case "k":
      ev.preventDefault(); moveSelection(-1); return;
    case "Enter":
      if (!state.selectedThread && state.threads[0]) {
        ev.preventDefault(); openThread(state.threads[0].id);
      }
      return;
    case "c":
      ev.preventDefault();
      $("#compose-btn").click();
      return;
    case "/":
      ev.preventDefault();
      $("#search-input").focus();
      $("#search-input").select();
      return;
    case "r":
      if (!state.selectedThread) return;
      ev.preventDefault();
      if (ev.shiftKey) replyToLatest(true); else replyToLatest(false);
      return;
    case "R":
      if (!state.selectedThread) return;
      ev.preventDefault();
      replyToLatest(true);
      return;
    case "f":
      if (!state.selectedThread) return;
      ev.preventDefault(); forwardLatest(); return;
    case "u":
      // Back to inbox — collapse the detail pane.
      if (state.selectedThread) {
        ev.preventDefault();
        $("#detail").classList.remove("open");
        $("#detail").classList.add("hidden");
        state.selectedThread = null;
        renderThreads();
      }
      return;
    case "#":
    case "Delete":
      if (!state.selectedThread) return;
      ev.preventDefault();
      $("#detail-delete").click();
      return;
  }
});

$("#shortcuts-close").addEventListener("click", () => {
  $("#shortcuts-help").classList.add("hidden");
});
$("#shortcuts-help").addEventListener("click", (ev) => {
  if (ev.target.id === "shortcuts-help") ev.target.classList.add("hidden");
});

// ── channel switcher (Mail / SMS) ─────────────────────────────────
if (!("channel"    in state)) state.channel    = "mail";
if (!("smsFilter"  in state)) state.smsFilter  = ""; // "" | "outbound" | "inbound"
if (!("smsList"    in state)) state.smsList    = [];

document.querySelectorAll(".channel-switch .ch-btn").forEach((btn) => {
  btn.addEventListener("click", () => {
    state.channel = btn.dataset.channel;
    document.querySelectorAll(".channel-switch .ch-btn").forEach((b) =>
      b.classList.toggle("active", b === btn));
    $("#channel-mail").classList.toggle("hidden", state.channel !== "mail");
    $("#channel-sms").classList.toggle("hidden", state.channel !== "sms");
    // Swap the inbox pane contents based on channel.
    if (state.channel === "sms") {
      $("#inbox-title").textContent = "SMS";
      refreshSMS();
    } else {
      $("#inbox-title").textContent = "All mail";
      refreshThreads();
    }
  });
});

document.querySelectorAll("[data-sms-filter]").forEach((li) => {
  li.addEventListener("click", () => {
    state.smsFilter = li.dataset.smsFilter;
    document.querySelectorAll("[data-sms-filter]").forEach((el) =>
      el.classList.toggle("active", el === li));
    refreshSMS();
  });
});

async function refreshSMS() {
  const qs = state.smsFilter ? `?direction=${state.smsFilter}` : "";
  try {
    const r = await api("/sms" + qs);
    state.smsList = r.sms || [];
    renderSMSList();
  } catch (err) {
    console.error("refreshSMS", err);
  }
}

function renderSMSList() {
  const ul = $("#message-list");
  const empty = $("#inbox-empty");
  if (!state.smsList.length) {
    ul.innerHTML = "";
    empty.classList.remove("hidden");
    empty.textContent = "No SMS yet.";
    return;
  }
  empty.classList.add("hidden");
  ul.className = "sms-list";
  ul.innerHTML = state.smsList.map((m) => {
    const isOut = m.direction === "outbound";
    const icon  = isOut ? "→" : "←";
    const badge = m.status
      ? `<span class="status-badge ${esc(m.status)}">${esc(m.status)}</span>`
      : "";
    return `
      <li data-id="${esc(m.id)}">
        <span class="dir-icon">${icon}</span>
        <div class="body-preview">
          <span class="to">${esc(isOut ? m.to : m.from)}</span>
          ${badge}
          <br>
          <span class="muted">${esc(m.body || "")}</span>
        </div>
        <div class="meta">
          ${esc(fmtDate(m.created_at))}<br>
          <span class="muted">${m.segments} seg</span>
        </div>
      </li>
    `;
  }).join("");
}

// SMS compose
const smsComposeEl = $("#sms-compose");
const smsForm      = $("#sms-compose-form");
const smsError     = $("#sms-compose-error");
const smsStatus    = $("#sms-compose-status");
const smsSendBtn   = $("#sms-compose-send");

$("#new-sms-btn").addEventListener("click", () => {
  smsForm.reset();
  smsError.classList.add("hidden");
  smsStatus.textContent = "";
  smsComposeEl.classList.remove("hidden");
  smsForm.to.focus();
});
$("#sms-compose-close").addEventListener("click", () => smsComposeEl.classList.add("hidden"));
smsComposeEl.addEventListener("click", (ev) => {
  if (ev.target === smsComposeEl) smsComposeEl.classList.add("hidden");
});

smsForm.addEventListener("submit", async (ev) => {
  ev.preventDefault();
  smsError.classList.add("hidden");
  smsSendBtn.disabled = true;
  smsStatus.textContent = "Sending…";
  try {
    const payload = {
      from: smsForm.from.value.trim(),
      to:   smsForm.to.value.trim(),
      body: smsForm.body.value,
    };
    const resp = await api("/sms", { method: "POST", body: JSON.stringify(payload) });
    smsStatus.textContent = `Sent ✓ (${resp.segments || 1} seg, status: ${resp.status || "sent"})`;
    setTimeout(() => smsComposeEl.classList.add("hidden"), 800);
    await refreshSMS();
  } catch (err) {
    smsError.textContent = String(err.message || err).replace(/^\d+:\s*/, "");
    smsError.classList.remove("hidden");
    smsStatus.textContent = "";
  } finally {
    smsSendBtn.disabled = false;
  }
});

// ── go ─────────────────────────────────────────────────────────────
boot();
