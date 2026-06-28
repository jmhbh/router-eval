const $ = (id) => document.getElementById(id);

async function fetchJSON(url) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json();
}

const metricHelp = {
  cost: "Sum of router-provided request costs observed inline or by API. If no router cost was present, this is shown as unknown.",
  ttfb: "Time to first byte: request sent → first response byte from the router (a stream framing event or the start of a buffered body).",
  ttft: "Time to first token: request sent → first streamed content delta. Streaming only; blank for non-streaming responses.",
  e2e: "Median proxy-measured time from sending the upstream request to receiving the last upstream byte.",
  e2eColumn: "Per-request end-to-end latency, measured at the proxy→router boundary: last_byte − request_sent (time from sending this upstream request to receiving its last upstream byte). It excludes local/agent time spent between requests. For client-canceled streams it ends at the last byte received before cancellation.",
  totalRequest: "Sum of proxy-measured request E2E durations for this run or group.",
  success: "Share of upstream requests that reached the router and returned a successful HTTP status. Client-canceled streams after router bytes are diagnostics, not router failures."
};

function fmtMoney(value, known = true) {
  if (!known || value === undefined || value === null) return "—";
  return `$${Number(value).toFixed(6)}`;
}

function fmtMS(value) {
  if (!value) return "—";
  const n = Number(value);
  if (n >= 1000) return `${(n / 1000).toFixed(2)} s`;
  return `${n.toFixed(1)} ms`;
}

function fmtRate(value) {
  if (value === undefined || value === null) return "—";
  return `${(Number(value) * 100).toFixed(1)}%`;
}

function fmtNum(value) {
  if (value === undefined || value === null) return "—";
  return Number(value).toLocaleString();
}

function fmtDate(value) {
  if (!value) return "—";
  const date = new Date(parseDateValue(value));
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat("en-GB", {
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false
  }).format(date);
}

function parseDateValue(value) {
  const text = String(value || "");
  const runIDMatch = text.match(/^(\d{4})(\d{2})(\d{2})T(\d{2})(\d{2})(\d{2})Z$/);
  if (!runIDMatch) return text;
  return `${runIDMatch[1]}-${runIDMatch[2]}-${runIDMatch[3]}T${runIDMatch[4]}:${runIDMatch[5]}:${runIDMatch[6]}Z`;
}

function shortID(value) {
  if (!value) return "—";
  if (value.length <= 30) return value;
  return `${value.slice(0, 15)}…${value.slice(-8)}`;
}

async function loadDashboard() {
  const aggregate = await fetchJSON("/api/aggregate");
  renderProbes(aggregate.probes || []);
  renderHarnesses(aggregate.harnesses || []);
}

function renderProbes(routers) {
  const el = $("probes");
  if (!routers.length) {
    el.innerHTML = `<div class="empty">No probe runs found.</div>`;
    return;
  }
  el.innerHTML = routers.map((router) => `
    <section class="panel">
      <div class="panel-head">
        <div>
          <div class="panel-title">${escapeHTML(router.router || "unknown router")}</div>
          <div class="panel-subtitle">Probe results grouped by probe type.</div>
        </div>
        ${probeRouterChips(router.summary)}
      </div>
      ${(router.probes || []).map(renderProbeSubgroup).join("")}
    </section>
  `).join("");
  attachRunDetailHandlers();
}

function renderProbeSubgroup(probe) {
  return `<details class="subgroup">
    <summary>
      <span>${escapeHTML(probe.name || "unknown probe")}</span>
      ${inlineSummary(probe.summary)}
    </summary>
    ${runCards(probe.runs || [])}
  </details>`;
}

function renderHarnesses(harnesses) {
  const el = $("harnesses");
  if (!harnesses.length) {
    el.innerHTML = `<div class="empty">No harness runs found.</div>`;
    return;
  }
  el.innerHTML = harnesses.map((harness) => `
    <section class="panel">
      <div class="panel-head">
        <div>
          <div class="panel-title">${escapeHTML(harness.harness || "unknown harness")}</div>
          <div class="panel-subtitle">Per-router aggregates for this harness. Compare the router rows below.</div>
        </div>
      </div>
      ${(harness.routers || []).map((router) => `
        <details class="group">
          <summary>
            <span>${escapeHTML(router.router || "unknown router")}</span>
            ${inlineSummary(router.summary)}
          </summary>
          ${(router.tasks || []).map((task) => `
            <details class="subgroup">
              <summary>
                <span>${escapeHTML(task.task || "unknown task")}</span>
                ${inlineSummary(task.summary)}
              </summary>
              ${runCards(task.runs || [])}
            </details>
          `).join("")}
        </details>
      `).join("")}
    </section>
  `).join("");
  attachRunDetailHandlers();
}

function summaryChips(summary = {}) {
  return `<div class="chips compact">
    ${chip("Requests", fmtNum(summary.request_count))}
    ${chip("Cost", fmtMoney(summary.total_cost_usd, summary.total_cost_known), metricHelp.cost)}
    ${chip("E2E p50", fmtMS(summary.e2e_p50_ms), metricHelp.e2e)}
    ${chip("Total Request Time", fmtMS(summary.total_request_ms), metricHelp.totalRequest)}
    ${chip("Request Success", fmtRate(summary.success_rate), metricHelp.success)}
  </div>`;
}

// Router-level probe summary deliberately omits E2E p50 and Total Request Time: at this
// level they blend distinct probe types (e.g. a non-streaming floor probe with a
// streaming one), which is a misleading signal. Those latency metrics stay on the
// per-probe-type subgroups and individual run cards, where they aggregate one shape.
function probeRouterChips(summary = {}) {
  return `<div class="chips compact">
    ${chip("Requests", fmtNum(summary.request_count))}
    ${chip("Cost", fmtMoney(summary.total_cost_usd, summary.total_cost_known), metricHelp.cost)}
    ${chip("Request Success", fmtRate(summary.success_rate), metricHelp.success)}
  </div>`;
}

function inlineSummary(summary = {}) {
  return `<span class="inline-summary">
    ${fmtNum(summary.run_count)} runs · ${fmtNum(summary.request_count)} req · ${fmtMoney(summary.total_cost_usd, summary.total_cost_known)} · total ${fmtMS(summary.total_request_ms)} · success ${fmtRate(summary.success_rate)}
  </span>`;
}

function runCards(runs) {
  if (!runs.length) return `<div class="empty small">No runs.</div>`;
  return `<div class="run-grid">
    ${runs.map((run) => `
      <details class="run-card" data-run-id="${escapeHTML(run.run_id)}">
        <summary class="run-summary">
          <div class="run-card-head">
            <div>
              <div class="run-id" title="${escapeHTML(run.run_id)}">${escapeHTML(fmtDate(run.started_at || run.run_id))}</div>
              <div class="run-meta">${escapeHTML(run.model || "—")} · ${escapeHTML(shortID(run.run_id))}</div>
            </div>
            <span class="status ${escapeHTML(run.status || "")}">${escapeHTML(run.status || "unknown")}</span>
          </div>
          ${summaryChips(run.summary || {})}
        </summary>
        <div class="request-detail"></div>
      </details>
    `).join("")}
  </div>`;
}

function attachRunDetailHandlers() {
  for (const row of document.querySelectorAll(".run-card[data-run-id]")) {
    row.addEventListener("toggle", async () => {
      if (!row.open) return;
      const runID = row.dataset.runId;
      const detail = row.querySelector(".request-detail");
      if (!detail) return;
      if (detail.dataset.loaded === "true") {
        return;
      }
      detail.innerHTML = `<div class="empty small">Loading requests…</div>`;
      try {
        const data = await fetchJSON(`/api/runs/${encodeURIComponent(runID)}`);
        detail.innerHTML = renderRunDetail(data);
        detail.dataset.loaded = "true";
      } catch (err) {
        detail.innerHTML = `<div class="error">Failed to load requests: ${escapeHTML(err.message)}</div>`;
      }
    });
  }
}

function renderRunDetail(data) {
  const metrics = data.metrics || {};
  const context = metrics.context || {};
  const comparable = metrics.comparable || {};
  const allRequests = data.requests || [];
  const requests = groupRequestsByProbe(allRequests);
  return `
    ${renderPrompt(data.prompt)}
    <div class="detail-summary">
      ${chip("Requests", fmtNum(allRequests.length))}
      ${chip("Input Tokens", fmtNum(context.input_tokens))}
      ${chip("Output Tokens", fmtNum(context.output_tokens))}
      ${chip("Tool Calls", fmtNum(context.tool_call_count))}
      ${chip("Valid Tool Calls", fmtNum(context.valid_tool_call_count))}
      ${chip("TTFB p50", fmtMS(comparable.ttfb_p50_ms), metricHelp.ttfb)}
      ${chip("TTFT p50", fmtMS(comparable.ttft_p50_ms), metricHelp.ttft)}
      ${chip("Total Request Time", fmtMS(comparable.total_request_ms))}
    </div>
    ${Object.entries(requests).map(([probe, rows]) => `
      <details class="request-group">
        <summary>
          <span>${escapeHTML(formatRequestGroupLabel(probe))}</span>
          <span>${fmtNum(rows.length)} upstream requests</span>
        </summary>
        <div class="request-group-note">Each row is one proxied upstream request. Stream events and tool calls are nested under that request.</div>
        ${requestTable(rows)}
      </details>
    `).join("")}
  `;
}

function renderPrompt(prompt) {
  const text = (prompt || "").trim();
  if (!text) {
    return `<details class="prompt-detail"><summary>Prompt</summary><pre>Prompt was not captured for this run.</pre></details>`;
  }
  return `<details class="prompt-detail"><summary>Prompt</summary><pre>${escapeHTML(text)}</pre></details>`;
}

function groupRequestsByProbe(requests) {
  const groups = {};
  for (const request of requests) {
    const key = request.diagnostics?.router_specific?.probe || request.endpoint || "requests";
    if (!groups[key]) groups[key] = [];
    groups[key].push(request);
  }
  return groups;
}

function formatRequestGroupLabel(value) {
  const text = String(value || "requests");
  if (text.startsWith("/v1/")) return `${text} upstream requests`;
  return text;
}

function requestTable(requests) {
  if (!requests.length) return `<div class="empty small">No request records.</div>`;
  const rows = requests.map((req, index) => {
    const usage = req.usage || {};
    const timing = req.timing || {};
    const success = requestSuccess(req);
    const requestID = req.request_id || req.generation_id || "";
    const hideClientCancel = success && req.error === "context canceled";
    const diagnostic = hideClientCancel ? "" : (req.error_class || "");
    const error = hideClientCancel ? "" : (req.error || "");
    return `<tr>
      <td class="request-id">${requestToolDetails(index + 1, requestID, req.diagnostics?.tool_calls || [])}</td>
      <td>${escapeHTML(req.endpoint || "—")}</td>
      <td>${escapeHTML(String(req.status_code || "—"))}</td>
      <td class="${success ? "ok" : "bad"}">${success ? "yes" : "no"}</td>
      <td>${fmtMS(timing.ttfb_ms)}</td>
      <td>${fmtMS(timing.ttft_ms)}</td>
      <td>${fmtMS(timing.e2e_ms)}</td>
      <td>${fmtNum(req.context?.tool_call_count)}</td>
      <td>${fmtMoney(usage.cost_usd, costKnown(usage))}</td>
      <td>${escapeHTML(usage.cost_state || "—")}</td>
      <td>${escapeHTML(diagnostic)}</td>
      <td>${escapeHTML(error)}</td>
    </tr>`;
  }).join("");
  return `<div class="table-scroll"><table>
      <thead>
        <tr>
          <th>Upstream Request</th>
          <th>Endpoint</th>
          <th>Status</th>
          <th>Success</th>
          <th>${columnHelp("TTFB", metricHelp.ttfb)}</th>
          <th>${columnHelp("TTFT", metricHelp.ttft)}</th>
          <th>${columnHelp("E2E", metricHelp.e2eColumn)}</th>
          <th>Tool Calls</th>
          <th>Cost</th>
          <th>Cost State</th>
          <th>Diagnostic</th>
          <th>Error</th>
        </tr>
      </thead>
      <tbody>${rows}</tbody>
    </table></div>`;
}

function requestToolDetails(ordinal, requestID, toolCalls) {
  const id = escapeHTML(requestID || "—");
  const label = `Request ${ordinal}`;
  if (!toolCalls.length) {
    return `<div class="request-label">${escapeHTML(label)}</div><div class="request-full-id">${id}</div>`;
  }
  return `<details class="request-tools" open>
    <summary>
      <span class="request-label">${escapeHTML(label)}</span>
      <span class="request-full-id">${id}</span>
    </summary>
    <div class="tool-call-list">
      ${toolCalls.map((call, index) => `
        <div class="tool-call">
          <div class="tool-call-head">
            <span>${escapeHTML(call.name || call.type || `tool call ${index + 1}`)}</span>
            <span class="${call.arguments_valid ? "ok" : "bad"}">${call.arguments_valid ? "valid args" : "invalid args"}</span>
          </div>
          ${call.id ? `<div class="tool-call-id">${escapeHTML(call.id)}</div>` : ""}
          <pre>${escapeHTML(call.arguments || "—")}</pre>
        </div>
      `).join("")}
    </div>
  </details>`;
}

function costKnown(usage = {}) {
  if (usage.cost_known || Number(usage.cost_usd) !== 0) return true;
  const raw = usage.raw || {};
  return Object.prototype.hasOwnProperty.call(raw, "cost") ||
    Object.prototype.hasOwnProperty.call(raw, "cost_usd") ||
    Object.prototype.hasOwnProperty.call(raw, "total_cost");
}

function requestSuccess(req = {}) {
  if (req.success) return true;
  const timing = req.timing || {};
  const context = req.context || {};
  return (req.error_class === "stream" || req.error_class === "client_cancel") &&
    req.error === "context canceled" &&
    Number(timing.first_byte_unix_nano || 0) > 0 &&
    Number(context.response_bytes || 0) > 0;
}

function chip(label, value, help = "") {
  const helpHTML = help ? metricHelpBubble(help) : "";
  return `<div class="chip"><div class="label">${escapeHTML(label)}${helpHTML}</div><div class="value">${escapeHTML(value)}</div></div>`;
}

function metricHelpBubble(help) {
  return `<span class="metric-help" tabindex="0">?<span class="tooltip">${escapeHTML(help)}</span></span>`;
}

function columnHelp(label, help) {
  return `<span class="th-help">${escapeHTML(label)}${metricHelpBubble(help)}</span>`;
}

function escapeHTML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

for (const tab of document.querySelectorAll(".tab")) {
  tab.addEventListener("click", () => {
    for (const t of document.querySelectorAll(".tab")) t.classList.remove("active");
    for (const panel of document.querySelectorAll(".tab-panel")) panel.classList.remove("active");
    tab.classList.add("active");
    $(tab.dataset.tab).classList.add("active");
  });
}

$("refresh").addEventListener("click", () => {
  loadDashboard().catch(renderLoadError);
});

function renderLoadError(err) {
  $("probes").innerHTML = `<div class="error">${escapeHTML(err.message)}</div>`;
}

loadDashboard().catch(renderLoadError);
