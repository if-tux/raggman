// Raggman UI: plain JavaScript against the HTTP API. Text from the server is
// always set with textContent, never parsed as HTML.
"use strict";

const $ = (id) => document.getElementById(id);
const POLL_MS = 1500;
const MAX_DOCUMENT_BYTES = 50 * 1024 * 1024;
const IN_PROGRESS = new Set(["PENDING", "INGESTING"]);
const STATE_LABELS = { PENDING: "Pending", INGESTING: "Ingesting", READY: "Ready", FAILED: "Failed" };

async function errorMessage(resp) {
  try {
    return (await resp.json()).error || resp.statusText;
  } catch {
    return resp.statusText;
  }
}

// --- Documents ---

let pollTimer;
let loadSeq = 0;
const rows = new Map(); // Document id → table row, updated in place

async function loadDocuments() {
  const seq = ++loadSeq;
  let docs;
  try {
    const resp = await fetch("/documents");
    if (!resp.ok) throw new Error(await errorMessage(resp));
    docs = await resp.json();
  } catch (err) {
    if (seq !== loadSeq) return;
    $("corpus-count").textContent = `Could not load Documents: ${err.message}`;
    schedulePoll(POLL_MS * 4);
    return;
  }
  if (seq !== loadSeq) return; // a newer load already drew the list

  render(docs);
  const ready = docs.filter((d) => d.status === "READY").length;
  $("corpus-count").textContent = `${docs.length} Document(s), ${ready} Ready`;
  if (docs.some((d) => IN_PROGRESS.has(d.status))) schedulePoll(POLL_MS);
}

function schedulePoll(ms) {
  clearTimeout(pollTimer);
  pollTimer = setTimeout(loadDocuments, ms);
}

// render updates rows in place, so focus and an armed Remove button survive
// each poll; rows only move when a Document is added or removed.
function render(docs) {
  const tbody = $("documents");
  const seen = new Set();
  docs.forEach((doc, i) => {
    seen.add(doc.id);
    let row = rows.get(doc.id);
    if (!row) {
      row = documentRow(doc);
      rows.set(doc.id, row);
    }
    updateState(row, doc);
    if (tbody.children[i] !== row) tbody.insertBefore(row, tbody.children[i] || null);
  });
  for (const [id, row] of rows) {
    if (!seen.has(id)) {
      row.remove();
      rows.delete(id);
    }
  }
}

function documentRow(doc) {
  const row = document.createElement("tr");
  const name = document.createElement("td");
  name.textContent = doc.filename;
  const state = document.createElement("td");
  state.className = "state";
  const actions = document.createElement("td");
  actions.append(removeButton(doc));
  row.append(name, state, actions);
  return row;
}

function updateState(row, doc) {
  const cell = row.querySelector(".state");
  const label = STATE_LABELS[doc.status] || doc.status;
  const reason = doc.failure_reason || "";
  if (cell.dataset.label === label && cell.dataset.reason === reason) return;
  cell.dataset.label = label;
  cell.dataset.reason = reason;
  cell.textContent = label;
  if (reason) {
    const span = document.createElement("span");
    span.className = "failure-reason";
    span.textContent = reason;
    cell.append(span);
  }
}

// Removing takes two activations: the first arms the button for a few seconds.
function removeButton(doc) {
  const button = document.createElement("button");
  button.type = "button";
  let armed;
  const show = (text, aria) => {
    button.textContent = text;
    button.setAttribute("aria-label", aria);
  };
  show("Remove", `Remove ${doc.filename}`);

  button.addEventListener("click", async () => {
    if (!armed) {
      show("Sure?", `Activate again to remove ${doc.filename}`);
      armed = setTimeout(() => {
        armed = undefined;
        show("Remove", `Remove ${doc.filename}`);
      }, 8000);
      return;
    }
    clearTimeout(armed);
    button.disabled = true;
    const row = button.closest("tr");
    const next = row.nextElementSibling || row.previousElementSibling;
    let message;
    try {
      const resp = await fetch(`/documents/${doc.id}`, { method: "DELETE" });
      message = resp.ok || resp.status === 404
        ? `Removed ${doc.filename}.`
        : `Could not remove ${doc.filename}: ${await errorMessage(resp)}`;
    } catch (err) {
      message = `Could not remove ${doc.filename}: ${err.message}`;
      button.disabled = false;
      armed = undefined;
      show("Remove", `Remove ${doc.filename}`);
    }
    $("submit-status").textContent = message;
    await loadDocuments();
    // Keep keyboard users in the list: focus the neighbouring row's button.
    if (!button.isConnected) (next?.querySelector("button") || $("pdf")).focus();
  });
  return button;
}

$("submit-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = $("pdf");
  const pdf = input.files[0];
  if (!pdf) return;

  const status = $("submit-status");
  if (pdf.size > MAX_DOCUMENT_BYTES) {
    status.textContent = `${pdf.name} was not added: PDFs can be at most 50 MB.`;
    return;
  }
  status.textContent = `Sending ${pdf.name}…`;
  const form = new FormData();
  form.append("file", pdf);

  try {
    const resp = await fetch("/documents", { method: "POST", body: form });
    if (!resp.ok) {
      status.textContent = `${pdf.name} was not added: ${await errorMessage(resp)}`;
      return;
    }
    const doc = await resp.json();
    status.textContent = resp.status === 201
      ? `Added ${doc.filename}; Ingestion has started.`
      : `${doc.filename} is already in Raggman (${STATE_LABELS[doc.status] || doc.status}).`;
    input.value = "";
    loadDocuments();
  } catch (err) {
    status.textContent = `${pdf.name} was not added: ${err.message}`;
  }
});

// --- Questions ---

// readEvents parses a Server-Sent Events body into {event, data} objects.
async function* readEvents(reader) {
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return;
    buffer += value.replace(/\r\n?/g, "\n");
    let end;
    while ((end = buffer.indexOf("\n\n")) >= 0) {
      const frame = buffer.slice(0, end);
      buffer = buffer.slice(end + 2);
      const event = { event: "message", data: [] };
      for (const line of frame.split("\n")) {
        const [field, ...rest] = line.split(":");
        const value = rest.join(":").replace(/^ /, "");
        if (field === "event") event.event = value;
        else if (field === "data") event.data.push(value);
      }
      if (event.data.length) yield { event: event.event, data: event.data.join("\n") };
    }
  }
}

function showCitations(citations) {
  const items = citations.map((c) => {
    const item = document.createElement("li");
    item.value = c.marker;
    item.textContent = `${c.filename}, page ${c.page}`;
    return item;
  });
  $("citations").replaceChildren(...items);
  $("citations-box").hidden = items.length === 0;
}

$("ask-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = $("ask-button");
  const question = $("question").value.trim();
  if (!question || button.disabled) return; // one Question at a time

  const panel = $("answer-panel");
  const answer = $("answer");
  const note = $("answer-note");
  const thinking = $("thinking");
  answer.textContent = "";
  note.textContent = "";
  showCitations([]);
  button.disabled = true;
  panel.setAttribute("aria-busy", "true");

  let reader;
  let finished = false;
  try {
    const resp = await fetch("/questions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question }),
    });
    if (!resp.ok) {
      note.textContent = await errorMessage(resp);
      return;
    }
    reader = resp.body.pipeThrough(new TextDecoderStream()).getReader();
    for await (const { event: name, data } of readEvents(reader)) {
      const payload = JSON.parse(data);
      if (name === "thinking") {
        thinking.hidden = false;
      } else if (name === "token") {
        thinking.hidden = true;
        answer.textContent += payload.text;
      } else if (name === "citations") {
        showCitations(payload.citations);
        finished = true;
      } else if (name === "error") {
        note.textContent = payload.error;
        finished = true;
      }
    }
    if (!finished) note.textContent = "The Answer was cut off before it finished.";
  } catch (err) {
    note.textContent = `The Answer was interrupted: ${err.message}`;
  } finally {
    reader?.cancel().catch(() => {});
    thinking.hidden = true;
    button.disabled = false;
    panel.setAttribute("aria-busy", "false");
  }
});

// Ctrl+Enter or Cmd+Enter asks from the textarea.
$("question").addEventListener("keydown", (event) => {
  if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
    event.preventDefault();
    $("ask-form").requestSubmit();
  }
});

// --- Taskbar clock ---

function tick() {
  $("clock").textContent = new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

tick();
setInterval(tick, 10_000);
loadDocuments();
