const L_BINS = [
    { id: 'random',     label: 'Random',     min: 0.0, max: 0.2 },
    { id: 'natural',    label: 'Natural',    min: 0.2, max: 0.6 },
    { id: 'structured', label: 'Structured', min: 0.6, max: 0.8 },
    { id: 'literal',    label: 'Literal',    min: 0.8, max: 1.01 }
];

const REGIME_COLORS = {
    'MAP': '#FFD700',    // Gold
    'Warp': '#00BFFF',   // Blue
    'DFA': '#32CD32',    // Green
    'Hybrid': '#9370DB', // Purple
    'Standard': '#666666'
};

let landscapeData = [];
let historyData = [];
let currentLocality = 'random';
let currentMode = 'relative';
let selectedEngines = new Set();

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

        // Default: all engines except standard
        const engines = [...new Set(landscapeData.map(d => d.engine))];
        engines.forEach(e => {
            if (e !== 'GoRegexp') selectedEngines.add(e);
        });

        setupControls(engines);
        updateSummary();
        renderLandscape();
        renderTrends();

        // Setup Modal
        const modal = document.getElementById('trace-modal');
        document.querySelector('.close-btn').onclick = () => modal.style.display = 'none';
        window.onclick = (event) => { if (event.target == modal) modal.style.display = 'none'; };

    } catch (err) {
        console.error('Failed to load dashboard data:', err);
        document.getElementById('loading-overlay').innerText = 'Error loading data. Check console.';
    }
}

function setupControls(engines) {
    // Metric mode
    document.getElementById('metric-mode').onchange = (e) => {
        currentMode = e.target.value;
        renderLandscape();
    };

    // Engine filters
    const filterContainer = document.getElementById('engine-filters');
    engines.sort().forEach(engine => {
        if (engine === 'GoRegexp') return; // Reference engine

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

    // Locality Tabs
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.onclick = (e) => {
            document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            currentLocality = btn.dataset.locality;
            renderLandscape();
        };
    });
}

function updateSummary() {
    const latest = historyData[historyData.length - 1];
    if (!latest) return;

    document.getElementById('min-speedup').innerText = latest.min_speedup.toFixed(2) + 'x';
    document.getElementById('avg-speedup').innerText = latest.avg_speedup.toFixed(2) + 'x';
    document.getElementById('max-speedup').innerText = formatLargeNumber(latest.max_speedup) + 'x';
}

function formatLargeNumber(n) {
    if (n < 1000) return n.toFixed(2);
    if (n < 1000000) return (n/1000).toFixed(1) + 'k';
    return (n/1000000).toFixed(1) + 'M';
}

function renderLandscape() {
    const locality = L_BINS.find(b => b.id === currentLocality);
    
    // Filter data by Locality and Selected Engines
    const filtered = landscapeData.filter(d => 
        d.l >= locality.min && d.l < locality.max &&
        (d.engine === 'GoRegexp' || selectedEngines.has(d.engine))
    );

    // Group by quantized B
    const bValues = [...new Set(filtered.map(d => Math.round(d.b * 20) / 20))].sort((a, b) => a - b);
    
    const container = document.getElementById('landscape-content');
    container.innerHTML = ''; // Clear

    if (bValues.length === 0) {
        container.innerHTML = '<p style="text-align:center; padding: 50px;">No data available for this locality segment.</p>';
        return;
    }

    const nRows = Math.ceil(bValues.length / 2);
    const nCols = Math.min(bValues.length, 2);

    const traces = [];
    const annotations = [];
    
    bValues.forEach((bVal, idx) => {
        const row = Math.floor(idx / nCols) + 1;
        const col = (idx % nCols) + 1;
        const axisSuffix = idx === 0 ? '' : (idx + 1);

        // Standard library baseline for this B-slice
        const stdData = filtered.filter(d => d.engine === 'GoRegexp' && Math.abs(d.b - bVal) < 0.03);
        const ourData = filtered.filter(d => d.engine !== 'GoRegexp' && Math.abs(d.b - bVal) < 0.03);

        const stdByS = {};
        stdData.forEach(s => stdByS[s.s.toFixed(5)] = s.throughput);

        // Plot Baseline
        if (currentMode === 'relative') {
            traces.push({
                x: [0, 1], y: [1, 1],
                type: 'scatter', mode: 'lines',
                line: { color: '#ccc', width: 2, dash: 'dash' },
                xaxis: 'x' + axisSuffix, yaxis: 'y' + axisSuffix,
                showlegend: false, hoverinfo: 'none'
            });
        } else {
            // Absolute mode: plot stdlib as reference points
            traces.push({
                x: stdData.map(d => d.s),
                y: stdData.map(d => d.throughput),
                name: 'GoRegexp (std)',
                type: 'scatter', mode: 'markers',
                marker: { symbol: 'cross-thin', color: '#666', size: 8, opacity: 0.4 },
                xaxis: 'x' + axisSuffix, yaxis: 'y' + axisSuffix,
                showlegend: idx === 0
            });
        }

        // Plot Our Data
        const engineTraces = {};
        ourData.forEach(d => {
            if (!engineTraces[d.engine]) engineTraces[d.engine] = { x: [], y: [], text: [], ids: [], color: [] };
            
            let yVal = d.throughput;
            if (currentMode === 'relative') {
                const stdTp = stdByS[d.s.toFixed(5)];
                yVal = stdTp ? (d.throughput / stdTp) : 1.0;
            }
            if (yVal <= 0) yVal = 0.0001; // Epsilon for log scale

            engineTraces[d.engine].x.push(d.s);
            engineTraces[d.engine].y.push(yVal);
            engineTraces[d.engine].text.push(`Case: ${d.category}<br>S: ${d.s.toFixed(3)}, B: ${d.b.toFixed(3)}<br>TP: ${d.throughput.toFixed(2)} MB/s`);
            engineTraces[d.engine].ids.push(d.trace_id);
            engineTraces[d.engine].color.push(REGIME_COLORS[d.regime] || '#999');
        });

        for (const [engine, data] of Object.entries(engineTraces)) {
            traces.push({
                x: data.x, y: data.y,
                text: data.text,
                ids: data.ids,
                name: engine,
                type: 'scatter', mode: 'markers',
                marker: { size: 10, color: data.color, line: { width: 1, color: '#fff' } },
                xaxis: 'x' + axisSuffix, yaxis: 'y' + axisSuffix,
                showlegend: idx === 0
            });
        }

        annotations.push({
            text: `Complexity B ≈ ${bVal.toFixed(2)}`,
            xref: 'paper', yref: 'paper',
            x: (col - 1) / nCols + 0.25,
            y: 1 - (row - 1) / nRows,
            showarrow: false,
            font: { size: 14, fontWeight: 'bold' }
        });
    });

    const layout = {
        grid: { rows: nRows, columns: nCols, pattern: 'independent' },
        height: 400 * nRows,
        margin: { t: 50, b: 50, l: 60, r: 20 },
        hovermode: 'closest',
        showlegend: true,
        legend: { orientation: 'h', y: -0.1 },
        annotations: annotations
    };

    // Configure all axes
    for (let i = 0; i < bValues.length; i++) {
        const axisSuffix = i === 0 ? '' : (i + 1);
        layout['xaxis' + axisSuffix] = { 
            title: 'Selectivity (S)', 
            autorange: 'reversed', 
            gridcolor: '#eee',
            range: [1.05, -0.05]
        };
        layout['yaxis' + axisSuffix] = { 
            title: currentMode === 'relative' ? 'Speedup (x)' : 'Throughput (MB/s)',
            type: 'log', 
            gridcolor: '#eee'
        };
    }

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
    document.getElementById('trace-title').innerText = 'Loading details...';
    document.getElementById('trace-pattern').innerText = '';
    document.getElementById('trace-explain').innerText = '';
    document.getElementById('trace-stats').innerHTML = '';
    modal.style.display = 'block';

    try {
        const resp = await fetch(`data/trace/${id}.json`);
        const data = await resp.json();

        document.getElementById('trace-title').innerText = data.category;
        document.getElementById('trace-pattern').innerText = data.pattern;
        document.getElementById('trace-explain').innerText = data.explain;
        
        document.getElementById('trace-stats').innerHTML = `
            <div><strong>S:</strong> ${data.s.toFixed(4)}</div>
            <div><strong>B:</strong> ${data.b.toFixed(4)}</div>
            <div><strong>L:</strong> ${data.l.toFixed(4)}</div>
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
        {
            x: dates, y: avg,
            name: 'Avg Speedup (Geometric Mean)',
            type: 'scatter', mode: 'lines+markers',
            line: { color: '#007bff', width: 3 },
            marker: { size: 8 }
        },
        {
            x: dates, y: max,
            name: 'Max Speedup (Peak Performance)',
            type: 'scatter', mode: 'lines+markers',
            line: { color: '#28a745', width: 2, dash: 'dot' },
            marker: { size: 6 },
            yaxis: 'y2'
        }
    ];

    const layout = {
        title: 'Performance Evolution',
        xaxis: { title: 'Date' },
        yaxis: { title: 'Avg Speedup (x)', gridcolor: '#eee' },
        yaxis2: { 
            title: 'Max Speedup (x)', 
            overlaying: 'y', 
            side: 'right', 
            type: 'log',
            showgrid: false
        },
        margin: { t: 50, b: 50, l: 60, r: 60 },
        hovermode: 'x unified',
        legend: { orientation: 'h', y: -0.2 }
    };

    Plotly.newPlot('trends-chart', traces, layout, { responsive: true });
}
