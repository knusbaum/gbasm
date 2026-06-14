const navEl = document.querySelector("#nav");
const proseEl = document.querySelector("#prose");
const sourceEl = document.querySelector("#source");
const runButton = document.querySelector("#run");
const resetButton = document.querySelector("#reset");
const prevButton = document.querySelector("#prev");
const nextButton = document.querySelector("#next");
const statusEl = document.querySelector("#status");
const outputEl = document.querySelector("#output");
const checkEl = document.querySelector("#check");
const stepsEl = document.querySelector("#steps");
const tabs = Array.from(document.querySelectorAll(".tab"));
const panels = Array.from(document.querySelectorAll(".panel"));

let order = [];          // flat [{section, slug, title}] in tour order
let current = null;      // current lesson payload
let pristineSource = ""; // the lesson's original main.bos

init();

async function init() {
  tabs.forEach((tab) => tab.addEventListener("click", () => selectPanel(tab.dataset.panel)));
  runButton.addEventListener("click", runProgram);
  resetButton.addEventListener("click", resetSource);
  prevButton.addEventListener("click", () => step(-1));
  nextButton.addEventListener("click", () => step(1));
  sourceEl.addEventListener("input", () => {
    if (current) localStorage.setItem(storageKey(current), sourceEl.value);
  });
  sourceEl.addEventListener("keydown", onEditorKey);
  window.addEventListener("hashchange", routeFromHash);

  await loadIndex();
  routeFromHash();
}

async function loadIndex() {
  const resp = await fetch("/api/tour");
  const data = await resp.json();
  navEl.innerHTML = "";
  order = [];
  for (const section of data.sections || []) {
    const name = document.createElement("div");
    name.className = "section-name";
    name.textContent = section.name;
    navEl.appendChild(name);
    for (const lesson of section.lessons || []) {
      order.push(lesson);
      const a = document.createElement("a");
      a.textContent = lesson.title;
      a.dataset.key = `${lesson.section}/${lesson.slug}`;
      a.href = `#${lesson.section}/${lesson.slug}`;
      navEl.appendChild(a);
    }
  }
}

function routeFromHash() {
  const key = decodeURIComponent(location.hash.replace(/^#/, ""));
  const target = order.find((l) => `${l.section}/${l.slug}` === key) || order[0];
  if (target) loadLesson(target.section, target.slug);
}

async function loadLesson(section, slug) {
  const resp = await fetch(`/api/tour/${section}/${slug}`);
  if (!resp.ok) {
    setStatus("error", "lesson not found");
    return;
  }
  current = await resp.json();
  pristineSource = current.source;

  proseEl.innerHTML = renderProse(current.prose, current.title);
  const saved = localStorage.getItem(storageKey(current));
  sourceEl.value = saved !== null ? saved : current.source;

  outputEl.textContent = "";
  checkEl.textContent = "";
  stepsEl.textContent = "";
  selectPanel("output");
  setStatus("ready", current.diagnostic ? "this lesson is expected to fail to compile" : "ready");

  const key = `${section}/${slug}`;
  navEl.querySelectorAll("a").forEach((a) => a.classList.toggle("active", a.dataset.key === key));
  const idx = order.findIndex((l) => `${l.section}/${l.slug}` === key);
  prevButton.disabled = idx <= 0;
  nextButton.disabled = idx < 0 || idx >= order.length - 1;
}

function step(delta) {
  if (!current) return;
  const key = `${current.section}/${current.slug}`;
  const idx = order.findIndex((l) => `${l.section}/${l.slug}` === key);
  const next = order[idx + delta];
  if (next) location.hash = `#${next.section}/${next.slug}`;
}

function resetSource() {
  if (!current) return;
  sourceEl.value = pristineSource;
  localStorage.removeItem(storageKey(current));
  sourceEl.focus();
}

async function runProgram() {
  if (!current) return;
  runButton.disabled = true;
  setStatus("running", "Running...");
  outputEl.textContent = "";
  checkEl.textContent = "";
  stepsEl.textContent = "";
  selectPanel("output");
  try {
    const resp = await fetch(`/api/tour/${current.section}/${current.slug}/run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ source: sourceEl.value, stdin: "" }),
    });
    const data = await resp.json();
    renderResult(data);
  } catch (error) {
    setStatus("error", `request failed: ${error.message}`);
    outputEl.textContent = String(error.stack || error);
  } finally {
    runButton.disabled = false;
  }
}

function renderResult(data) {
  const run = data.run || {};
  const program = run.program || {};
  const stdout = program.stdout || "";
  const stderr = program.stderr || "";
  outputEl.textContent = (stdout + (stdout && stderr ? "\n" : "") + stderr) || firstFailedStepOutput(run) || "(no output)";
  stepsEl.textContent = formatSteps(run.steps || []);

  const check = data.check || {};
  checkEl.innerHTML = check.passed
    ? `<span class="pass">✓ check passed</span>`
    : `<span class="fail">✗ check failed</span>\n\n${escapeHTML(check.message || "")}`;

  if (check.passed) {
    setStatus("ok", "check passed");
  } else {
    setStatus("error", "check failed");
    selectPanel("check");
  }
}

function firstFailedStepOutput(run) {
  const failed = (run.steps || []).find((step) => step.exitCode !== 0);
  if (!failed) return run.error?.message || "";
  return [failed.stdout, failed.stderr].filter(Boolean).join("\n");
}

function formatSteps(steps) {
  if (!steps.length) return "(no stages)";
  return steps.map((step) => {
    const lines = [`$ ${(step.argv || []).join(" ")}`, `stage=${step.name} exit=${step.exitCode} ms=${step.ms}`];
    if (step.stdout) lines.push("", "stdout:", step.stdout);
    if (step.stderr) lines.push("", "stderr:", step.stderr);
    return lines.join("\n");
  }).join("\n\n");
}

function onEditorKey(event) {
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
    event.preventDefault();
    runProgram();
    return;
  }
  if (event.key === "Tab") {
    event.preventDefault();
    const start = sourceEl.selectionStart;
    const end = sourceEl.selectionEnd;
    sourceEl.value = sourceEl.value.slice(0, start) + "\t" + sourceEl.value.slice(end);
    sourceEl.selectionStart = sourceEl.selectionEnd = start + 1;
    if (current) localStorage.setItem(storageKey(current), sourceEl.value);
  }
}

function selectPanel(name) {
  tabs.forEach((tab) => tab.classList.toggle("active", tab.dataset.panel === name));
  panels.forEach((panel) => panel.classList.toggle("active", panel.id === name));
}

function setStatus(kind, text) {
  statusEl.className = `status ${kind}`;
  statusEl.textContent = text;
}

function storageKey(lesson) {
  return `btour.${lesson.section}/${lesson.slug}`;
}

// renderProse converts a small subset of Markdown (heading, paragraphs,
// inline `code` and **bold**) to HTML. The lesson title is shown as an H1.
function renderProse(markdown, title) {
  const blocks = (markdown || "").split(/\n\s*\n/).map((b) => b.trim()).filter(Boolean);
  let html = `<h1>${escapeHTML(title || "")}</h1>`;
  for (const block of blocks) {
    html += `<p>${inline(block)}</p>`;
  }
  return html;
}

function inline(text) {
  let out = escapeHTML(text).replace(/\n/g, " ");
  out = out.replace(/`([^`]+)`/g, (_, code) => `<code>${code}</code>`);
  out = out.replace(/\*\*([^*]+)\*\*/g, (_, b) => `<strong>${b}</strong>`);
  return out;
}

function escapeHTML(s) {
  return String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}
