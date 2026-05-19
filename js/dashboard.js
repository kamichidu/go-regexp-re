const B_TIERS = [
    { label: 'Low B (Simple)',  min: 0.0, max: 0.6 },
    { label: 'Mid B (Balanced)', min: 0.6, max: 0.8 },
    { label: 'High B (Stress)',  min: 0.8, max: 1.1 }
];

const REGIME_COLORS = {
    'MAP': '#FFD700',    // Gold
    'Warp': '#00BFFF',   // Blue
    'DFA': '#32CD32',    // Green
    'Hybrid': '#9370DB', // Purple
    'Standard': '#666666'
};

const ENGINE_COLORS = {
    'GoRegexpRe': 'rgba(26, 115, 232, 0.15)',
    'Coregex':    'rgba(234, 67, 53, 0.1)',
    'RE2-CGO':    'rgba(52, 168, 83, 0.1)',
    'PCRE2-CGO':  'rgba(251, 188, 4, 0.1)'
};

const EPS = 1e-4;
const transformX = (s) => -Math.log10(s + EPS);

let landscapeData = [];
let historyData = [];
let currentMode = 'relative';
let selectedEngines = new Set(['GoRegexpRe', 'Coregex']);

document.addEventListener('DOMContentLoaded', async () => {
    await init();
});

async function init() {
    try {
        const [lsResp, histResp] = await Promise.all([
            fetch('data/landscape.json'),
            fetch('data/history.json')
        ]);
        landscapeData = await lsResp.json();
        historyData = await histResp.json();

        const engines = [...new Set(landscapeData.map(d => d.engine))].filter(e => e !== 'GoRegexp');
        setupControls(engines);
        updateSummary();
        renderLandscape();
        renderTrends();

        const modal = document.getElementById('trace-modal');
        document.querySelector('.close-btn').onclick = () => modal.style.display = 'none';
        window.onclick = (event) => { if (event.target == modal) modal.style.display = 'none'; };
    } catch (err) {
        console.error('Failed to load dashboard data:', err);
        document.getElementById('loading-overlay').innerText = 'Error loading data.';
    }
}

function setupControls(engines) {
    document.getElementById('metric-mode').onchange = (e) => {
        currentMode = e.target.value;
        renderLandscape();
    };

    const filterContainer = document.getElementById('engine-filters');
    filterContainer.innerHTML = '';
    engines.sort().forEach(engine => {
        const label = document.createElement('label');
        label.style.marginRight = '15px';
        const cb = document.createElement('input');
        cb.type = 'checkbox';
        cb.checked = selectedEngines.has(engine);
        cb.onchange = (e) => {
            if (e.target.checked) selectedEngines.add(engine);
            else selectedEngines.delete(engine);
            renderLandscape();
        };
        label.appendChild(cb);
        label.appendChild(document.createTextNode(' ' + engine));
        filterContainer.appendChild(label);
    });
}

function updateSummary() {
    const latest = historyData[historyData.length - 1];
    if (!latest) return;
    document.getElementById('min-speedup').innerText = latest.min_speedup.toFixed(2) + 'x';
    document.getElementById('avg-speedup').innerText = latest.avg_speedup.toFixed(2) + 'x';
    document.getElementById('max-speedup').innerText = (latest.max_speedup/1000).toFixed(1) + 'k';
}

function renderLandscape() {
    const container = document.getElementById('landscape-content');
    container.innerHTML = ''; 

    const traces = [];
    const annotations = [];
    const shapes = [];
    const layout = {};

    B_TIERS.forEach((tier, idx) => {
        const axisSuffix = idx === 0 ? '' : (idx + 1);
        const isBottom = idx === B_TIERS.length - 1;

        const sliceData = landscapeData.filter(d => d.b >= tier.min && d.b < tier.max);
        const stdData = sliceData.filter(d => d.engine === 'GoRegexp');
        const stdByS = {};
        stdData.forEach(s => stdByS[s.s.toFixed(5)] = s.throughput);

        // Zone Backgrounds
        if (currentMode === 'relative') {
            shapes.push({
                type: 'rect', xref: 'x' + axisSuffix, yref: 'y' + axisSuffix,
                x0: -0.5, x1: transformX(0) + 0.5, y0: 10, y1: 1e8,
                fillcolor: 'rgba(255, 215, 0, 0.05)', line: { width: 0 }, layer: 'below'
            });
            shapes.push({
                type: 'rect', xref: 'x' + axisSuffix, yref: 'y' + axisSuffix,
                x0: -0.5, x1: transformX(0) + 0.5, y0: 1e-4, y1: 1.0,
                fillcolor: 'rgba(255, 0, 0, 0.03)', line: { width: 0 }, layer: 'below'
            });
            traces.push({
                x: [0, transformX(0)], y: [1, 1], type: 'scatter', mode: 'lines',
                line: { color: '#666', width: 1 }, xaxis: 'x' + axisSuffix, yaxis: 'y' + axisSuffix,
                showlegend: false, hoverinfo: 'none'
            });
        }

        // Generate Envelopes
        selectedEngines.forEach(engine => {
            const engData = sliceData.filter(d => d.engine === engine);
            
            const sGroups = {};
            engData.forEach(d => {
                const sKey = d.s.toFixed(5);
                if (!sGroups[sKey]) sGroups[sKey] = [];
                sGroups[sKey].push(d);
            });

            const sortedS = Object.keys(sGroups).sort((a,b) => parseFloat(a) - parseFloat(b));
            const topX = [], topY = [], botX = [], botY = [];

            sortedS.forEach(sKey => {
                const pts = sGroups[sKey];
                const stdTp = stdByS[sKey];
                if (!stdTp && currentMode === 'relative') return;

                const getVal = (tp) => currentMode === 'relative' ? tp/stdTp : tp;
                const vals = pts.map(p => getVal(p.throughput));
                
                topX.push(transformX(parseFloat(sKey)));
                topY.push(Math.max(...vals));
                botX.push(transformX(parseFloat(sKey)));
                botY.push(Math.min(...vals));
            });

            if (topX.length > 0) {
                traces.push({
                    x: topX.concat(botX.reverse()),
                    y: topY.concat(botY.reverse()),
                    fill: 'toself',
                    fillcolor: ENGINE_COLORS[engine] || 'rgba(0,0,0,0.05)',
                    line: { width: 0 },
                    name: `${engine} Envelope`,
                    xaxis: 'x' + axisSuffix, yaxis: 'y' + axisSuffix,
                    showlegend: idx === 0, hoverinfo: 'none'
                });
                traces.push({
                    x: engData.map(d => transformX(d.s)),
                    y: engData.map(d => {
                        const stdTp = stdByS[d.s.toFixed(5)];
                        return currentMode === 'relative' ? (stdTp ? d.throughput/stdTp : 1) : d.throughput;
                    }),
                    ids: engData.map(d => d.trace_id),
                    mode: 'markers',
                    marker: { size: 6, color: engData.map(d => REGIME_COLORS[d.regime] || '#999'), opacity: 0.8 },
                    name: engine,
                    text: engData.map(d => `${d.category}<br>S: ${d.s.toFixed(4)}<br>Regime: ${d.regime}`),
                    xaxis: 'x' + axisSuffix, yaxis: 'y' + axisSuffix,
                    showlegend: idx === 0 && engine === 'GoRegexpRe'
                });
            }
        });

        annotations.push({
            text: tier.label, xref: 'paper', yref: 'y' + axisSuffix + ' domain',
            x: 1.02, y: 0.5, showarrow: false, textangle: 90, font: { size: 14, fontWeight: 'bold' }
        });

        layout['xaxis' + axisSuffix] = { 
            title: isBottom ? '-log10(S + ε)' : '', 
            autorange: false, range: [-0.1, transformX(0) + 0.1], gridcolor: '#eee', showticklabels: true, matches: 'x'
        };
        const yRange = currentMode === 'relative' ? [-3, 7] : [0, 10];
        layout['yaxis' + axisSuffix] = { 
            title: 'Speedup (x)', type: 'log', autorange: false, range: yRange, gridcolor: '#eee'
        };
    });

    const layoutParams = {
        grid: { rows: B_TIERS.length, columns: 1, pattern: 'independent', roworder: 'top to bottom', ygap: 0.1 },
        height: 1200, autosize: true, margin: { t: 50, b: 80, l: 80, r: 80 },
        hovermode: 'closest', showlegend: true, legend: { orientation: 'h', y: -0.05, x: 0.5, xanchor: 'center' },
        annotations: annotations, shapes: shapes
    };

    Object.assign(layout, layoutParams);
    Plotly.newPlot(container, traces, layout, { responsive: true }).then(gd => {
        container.on('plotly_click', (data) => {
            const point = data.points[0];
            if (point && point.data.ids) {
                const traceId = point.data.ids[point.pointIndex];
                if (traceId) showTrace(traceId);
            }
        });
    });
}

async function showTrace(id) {
    const modal = document.getElementById('trace-modal');
    modal.style.display = 'block';
    try {
        const resp = await fetch(`data/trace/${id}.json`);
        const data = await resp.json();
        document.getElementById('trace-title').innerText = data.category;
        document.getElementById('trace-pattern').innerText = data.pattern;
        document.getElementById('trace-explain').innerText = data.explain;
        document.getElementById('trace-stats').innerHTML = `
            <div>S: ${data.s.toFixed(4)}</div>
            <div>B: ${data.b.toFixed(4)}</div>
            <div>L: ${data.l.toFixed(4)}</div>
        `;
    } catch (err) {
        document.getElementById('trace-title').innerText = 'Error loading trace';
    }
}

function renderTrends() {
    const dates = historyData.map(d => d.date);
    const avg = historyData.map(d => d.avg_speedup);
    const max = historyData.map(d => d.max_speedup);
    const traces = [
        { x: dates, y: avg, name: 'Avg Speedup', type: 'scatter', mode: 'lines+markers', line: { color: '#1a73e8', width: 3 } },
        { x: dates, y: max, name: 'Max Speedup', type: 'scatter', mode: 'lines+markers', line: { color: '#34a853', width: 2, dash: 'dot' }, yaxis: 'y2' }
    ];
    const layout = {
        title: 'Architectural Performance Trends',
        xaxis: { title: 'Release Date' },
        yaxis: { title: 'Avg Speedup (x)', gridcolor: '#eee' },
        yaxis2: { title: 'Max Speedup (x)', overlaying: 'y', side: 'right', type: 'log', showgrid: false },
        margin: { t: 60, b: 50, l: 60, r: 60 },
        hovermode: 'x unified', legend: { orientation: 'h', y: -0.2 }
    };
    Plotly.newPlot('trends-chart', traces, layout, { responsive: true });
}
