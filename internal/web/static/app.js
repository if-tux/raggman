// Raggman UI: plain JavaScript against the HTTP API. Text from the server is
// always set with textContent, never parsed as HTML. The UI speaks Portuguese;
// the API speaks English, so server messages are translated here.
"use strict";

const $ = (id) => document.getElementById(id);
const POLL_MS = 1500;
const MAX_DOCUMENT_BYTES = 50 * 1024 * 1024;
const IN_PROGRESS = new Set(["PENDING", "INGESTING"]);
const STATE_LABELS = { PENDING: "Pendente", INGESTING: "Em ingestão", READY: "Pronto", FAILED: "Falhou" };

// Fixed English texts from the API, shown in Portuguese.
const NOT_FOUND_ANSWER = "I could not find anything about this in the Corpus.";
const STATUS_MESSAGES = {
  413: "o PDF passa de 50 MB",
  415: "só aceitamos arquivos PDF",
  502: "não foi possível gerar a Resposta",
};

async function errorMessage(resp) {
  if (STATUS_MESSAGES[resp.status]) return STATUS_MESSAGES[resp.status];
  try {
    return (await resp.json()).error || resp.statusText;
  } catch {
    return resp.statusText;
  }
}

function failureReason(reason) {
  if (reason === "no extractable text") return "sem texto extraível (PDF escaneado?)";
  return reason.replace(/^Ingestion failed after (\d+) attempts: /, "a Ingestion falhou após $1 tentativas: ");
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
    $("corpus-count").textContent = `Não foi possível carregar os Documentos: ${err.message}`;
    schedulePoll(POLL_MS * 4);
    return;
  }
  if (seq !== loadSeq) return; // a newer load already drew the list

  render(docs);
  const ready = docs.filter((d) => d.status === "READY").length;
  $("corpus-count").textContent = `${docs.length} Documento(s), ${ready} pronto(s)`;
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
  const reason = doc.failure_reason ? failureReason(doc.failure_reason) : "";
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
  show("Remover", `Remover ${doc.filename}`);

  button.addEventListener("click", async () => {
    if (!armed) {
      show("Certeza?", `Ative de novo para remover ${doc.filename}`);
      armed = setTimeout(() => {
        armed = undefined;
        show("Remover", `Remover ${doc.filename}`);
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
        ? `${doc.filename} foi removido.`
        : `Não foi possível remover ${doc.filename}: ${await errorMessage(resp)}`;
    } catch (err) {
      message = `Não foi possível remover ${doc.filename}: ${err.message}`;
      button.disabled = false;
      armed = undefined;
      show("Remover", `Remover ${doc.filename}`);
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
    status.textContent = `${pdf.name} não foi adicionado: o PDF passa de 50 MB.`;
    return;
  }
  status.textContent = `Enviando ${pdf.name}…`;
  const form = new FormData();
  form.append("file", pdf);

  try {
    const resp = await fetch("/documents", { method: "POST", body: form });
    if (!resp.ok) {
      status.textContent = `${pdf.name} não foi adicionado: ${await errorMessage(resp)}`;
      return;
    }
    const doc = await resp.json();
    status.textContent = resp.status === 201
      ? `${doc.filename} foi adicionado; a Ingestion começou.`
      : `${doc.filename} já está no Raggman (${STATE_LABELS[doc.status] || doc.status}).`;
    input.value = "";
    loadDocuments();
  } catch (err) {
    status.textContent = `${pdf.name} não foi adicionado: ${err.message}`;
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

// renderAnswer shows **bold** from the model as bold, built from DOM nodes so
// the text is still never parsed as HTML.
function renderAnswer(el, text) {
  el.replaceChildren(...text.split(/(\*\*[^*\n]+\*\*)/).map((part) => {
    if (!/^\*\*[^*\n]+\*\*$/.test(part)) return document.createTextNode(part);
    const bold = document.createElement("strong");
    bold.textContent = part.slice(2, -2);
    return bold;
  }));
}

function showCitations(citations) {
  const items = citations.map((c) => {
    const item = document.createElement("li");
    item.value = c.marker;
    item.textContent = `${c.filename}, página ${c.page}`;
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
        if (answer.textContent === NOT_FOUND_ANSWER) {
          answer.textContent = "Não encontrei nada sobre isso no Corpus.";
        } else {
          renderAnswer(answer, answer.textContent);
        }
        showCitations(payload.citations);
        finished = true;
      } else if (name === "error") {
        note.textContent = "Não foi possível gerar a Resposta.";
        finished = true;
      }
    }
    if (!finished) note.textContent = "A Resposta foi interrompida antes do fim.";
  } catch (err) {
    note.textContent = `A Resposta foi interrompida: ${err.message}`;
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
  $("clock").textContent = new Date().toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
}

tick();
setInterval(tick, 10_000);
loadDocuments();
