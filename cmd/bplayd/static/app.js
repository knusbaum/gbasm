const defaultSource = `package main

import "fmt"

fn main() {
\tfmt.print("hello from Boson\\n")
}
`;

const sourceEl = document.querySelector("#source");
const runButton = document.querySelector("#run");
const resetButton = document.querySelector("#reset");
const statusEl = document.querySelector("#status");
const outputEl = document.querySelector("#output");
const assemblyEl = document.querySelector("#assembly");
const stepsEl = document.querySelector("#steps");
const toolchainEl = document.querySelector("#toolchain");
const tabs = Array.from(document.querySelectorAll(".tab"));
const panels = Array.from(document.querySelectorAll(".panel"));

sourceEl.value = localStorage.getItem("bplayd.source") || defaultSource;

sourceEl.addEventListener("input", () => {
  localStorage.setItem("bplayd.source", sourceEl.value);
});

runButton.addEventListener("click", runProgram);
resetButton.addEventListener("click", () => {
  sourceEl.value = defaultSource;
  localStorage.setItem("bplayd.source", sourceEl.value);
  sourceEl.focus();
});

sourceEl.addEventListener("keydown", (event) => {
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
    localStorage.setItem("bplayd.source", sourceEl.value);
  }
});

tabs.forEach((tab) => {
  tab.addEventListener("click", () => selectPanel(tab.dataset.panel));
});

loadToolchain();

async function loadToolchain() {
  try {
    const response = await fetch("/api/toolchain");
    if (!response.ok) return;
    const data = await response.json();
    toolchainEl.textContent = data.commit || "dev";
    toolchainEl.title = `${data.runtimeBundleImportcfg || ""}`;
  } catch {
    toolchainEl.textContent = "offline";
  }
}

async function runProgram() {
  runButton.disabled = true;
  setStatus("running", "Running...");
  outputEl.textContent = "";
  assemblyEl.textContent = "";
  stepsEl.textContent = "";
  selectPanel("output");

  try {
    const response = await fetch("/api/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ source: sourceEl.value, stdin: "" }),
    });
    const data = await response.json();
    renderRun(data);
  } catch (error) {
    setStatus("error", `request failed: ${error.message}`);
    outputEl.textContent = String(error.stack || error);
  } finally {
    runButton.disabled = false;
  }
}

function renderRun(data) {
  const program = data.program || {};
  const stdout = program.stdout || "";
  const stderr = program.stderr || "";
  const combined = stdout + (stdout && stderr ? "\n" : "") + stderr;
  outputEl.textContent = combined || firstFailedStepOutput(data) || "";
  assemblyEl.textContent = data.artifacts?.assembly || "";
  stepsEl.textContent = formatSteps(data.steps || []);

  const status = data.status || "internal_error";
  const elapsed = totalStepMs(data.steps || []);
  const exitCode = Number.isInteger(program.exitCode) ? `, exited ${program.exitCode}` : "";
  setStatus(status === "ok" ? "ok" : "error", `${status} in ${elapsed}ms${exitCode}`);
}

function firstFailedStepOutput(data) {
  const failed = (data.steps || []).find((step) => step.exitCode !== 0);
  if (!failed) return data.error?.message || "";
  return [failed.stdout, failed.stderr].filter(Boolean).join("\n");
}

function formatSteps(steps) {
  if (!steps.length) return "";
  return steps.map((step) => {
    const lines = [
      `$ ${step.argv.join(" ")}`,
      `stage=${step.name} exit=${step.exitCode} ms=${step.ms}`,
    ];
    if (step.stdout) lines.push("", "stdout:", step.stdout);
    if (step.stderr) lines.push("", "stderr:", step.stderr);
    return lines.join("\n");
  }).join("\n\n");
}

function totalStepMs(steps) {
  return steps.reduce((sum, step) => sum + (step.ms || 0), 0);
}

function setStatus(kind, text) {
  statusEl.className = `status ${kind}`;
  statusEl.textContent = text;
}

function selectPanel(name) {
  tabs.forEach((tab) => {
    const active = tab.dataset.panel === name;
    tab.classList.toggle("active", active);
    tab.setAttribute("aria-selected", active ? "true" : "false");
  });
  panels.forEach((panel) => {
    panel.classList.toggle("active", panel.id === name);
  });
}
