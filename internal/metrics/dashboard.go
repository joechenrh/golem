package metrics

import "net/http"

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Golem Metrics</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
         background: #0f1117; color: #e0e0e0; padding: 24px; }
  h1 { font-size: 22px; font-weight: 600; margin-bottom: 4px; }
  .header { display: flex; justify-content: space-between; align-items: baseline;
            margin-bottom: 24px; border-bottom: 1px solid #2a2d35; padding-bottom: 16px; }
  .uptime { color: #888; font-size: 14px; }
  .agents { display: flex; flex-wrap: wrap; gap: 20px; }
  .agent-card { background: #181a20; border: 1px solid #2a2d35; border-radius: 10px;
                padding: 20px; min-width: 380px; flex: 1; max-width: 600px; }
  .agent-name { font-size: 17px; font-weight: 600; color: #7eb8ff; margin-bottom: 14px; }
  .stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; margin-bottom: 16px; }
  .stat { background: #1e2028; border-radius: 8px; padding: 12px; }
  .stat-value { font-size: 22px; font-weight: 700; color: #fff; }
  .stat-label { font-size: 11px; color: #888; text-transform: uppercase; letter-spacing: 0.5px; margin-top: 2px; }
  .stat-value.error { color: #ff6b6b; }
  .tool-table { width: 100%; border-collapse: collapse; font-size: 13px; }
  .tool-table th { text-align: left; color: #888; font-weight: 500; padding: 6px 8px;
                   border-bottom: 1px solid #2a2d35; font-size: 11px; text-transform: uppercase; }
  .tool-table td { padding: 5px 8px; border-bottom: 1px solid #1e2028; }
  .tool-table td.num { text-align: right; font-variant-numeric: tabular-nums; }
  .tool-table td.err { color: #ff6b6b; }
  .section-title { font-size: 12px; color: #888; text-transform: uppercase; letter-spacing: 0.5px;
                   margin-bottom: 8px; margin-top: 4px; }
  .no-agents { color: #666; font-style: italic; margin-top: 40px; text-align: center; }
  .pulse { display: inline-block; width: 8px; height: 8px; background: #4ade80; border-radius: 50%;
           margin-right: 8px; animation: pulse 2s ease-in-out infinite; }
  @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.4; } }
</style>
</head>
<body>
<div class="header">
  <h1><span class="pulse"></span>Golem Metrics</h1>
  <span class="uptime" id="uptime"></span>
</div>
<div class="agents" id="agents"></div>

<script>
function fmt(n) {
  if (n >= 1e6) return (n/1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n/1e3).toFixed(1) + 'K';
  return String(n);
}
function fmtUptime(s) {
  const h = Math.floor(s/3600), m = Math.floor((s%3600)/60), sec = Math.floor(s%60);
  if (h > 0) return h + 'h ' + m + 'm';
  if (m > 0) return m + 'm ' + sec + 's';
  return sec + 's';
}

function render(data) {
  document.getElementById('uptime').textContent = 'Uptime: ' + fmtUptime(data.uptime_seconds);
  const container = document.getElementById('agents');
  if (!data.agents || data.agents.length === 0) {
    container.innerHTML = '<div class="no-agents">No agents registered</div>';
    return;
  }
  container.innerHTML = data.agents.map(a => {
    const latency = a.latency_avg_ms !== null ? a.latency_avg_ms + ' ms' : '-';
    let toolRows = '';
    if (a.tools && a.tools.length > 0) {
      toolRows = '<div class="section-title">Tools</div><table class="tool-table">' +
        '<tr><th>Name</th><th style="text-align:right">Calls</th><th style="text-align:right">Errors</th></tr>' +
        a.tools.map(t =>
          '<tr><td>' + t.name + '</td><td class="num">' + fmt(t.calls) + '</td>' +
          '<td class="num' + (t.errors > 0 ? ' err' : '') + '">' + t.errors + '</td></tr>'
        ).join('') + '</table>';
    }
    return '<div class="agent-card">' +
      '<div class="agent-name">' + a.name + '</div>' +
      '<div class="stats">' +
        '<div class="stat"><div class="stat-value">' + fmt(a.llm_calls) + '</div><div class="stat-label">LLM Calls</div></div>' +
        '<div class="stat"><div class="stat-value' + (a.llm_errors > 0 ? ' error' : '') + '">' + a.llm_errors + '</div><div class="stat-label">LLM Errors</div></div>' +
        '<div class="stat"><div class="stat-value">' + latency + '</div><div class="stat-label">Avg Latency</div></div>' +
        '<div class="stat"><div class="stat-value">' + fmt(a.prompt_tokens) + '</div><div class="stat-label">Prompt Tokens</div></div>' +
        '<div class="stat"><div class="stat-value">' + fmt(a.completion_tokens) + '</div><div class="stat-label">Completion Tokens</div></div>' +
        '<div class="stat"><div class="stat-value">' + a.active_sessions + '</div><div class="stat-label">Sessions</div></div>' +
      '</div>' +
      toolRows +
    '</div>';
  }).join('');
}

async function poll() {
  try {
    const resp = await fetch('/debug/metrics.json');
    if (resp.ok) render(await resp.json());
  } catch(e) { /* retry next tick */ }
}
poll();
setInterval(poll, 5000);
</script>
</body>
</html>`

// NewDashboardHandler returns an http.Handler that serves the metrics dashboard.
func NewDashboardHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(dashboardHTML))
	})
}
