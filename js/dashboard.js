document.addEventListener('DOMContentLoaded', async function() {
    try {
        const response = await fetch('data/landscape.json');
        if (!response.ok) throw new Error('data/landscape.json not found. Run benchmark on main branch to generate data.');
        const results = await response.json();
        
        // Setup L-slice selector based on actual data
        populateLSelector(results);

        // Render all charts with the loaded data
        renderLandscape(results);
        renderTrends();
        renderRegression(results);
        renderDeepDive(results);

        // Update summary stats
        updateSummary(results);
    } catch (err) {
        console.error('Viewer Error:', err);
        document.querySelector('main').insertAdjacentHTML('afterbegin', `
            <div style="background: #fff3f3; color: #721c24; padding: 20px; border-radius: 8px; margin-bottom: 30px; border: 1px solid #f5c6cb;">
                <strong>Viewer Status:</strong> ${err.message}
            </div>
        `);
    }
});

function populateLSelector(results) {
    const lValues = [...new Set(results.map(r => parseFloat(r.l.toFixed(2))))].sort((a, b) => a - b);
    const selector = document.getElementById('l-slice');
    selector.innerHTML = lValues.map(l => `<option value="${l}">L = ${l}</option>`).join('');
    // Select a middle-ish value by default
    if (lValues.length > 0) {
        selector.selectedIndex = Math.floor(lValues.length / 2);
    }
}

function updateSummary(results) {
    const ourResults = results.filter(r => r.engine === 'GoRegexpRe');
    const stdResults = results.filter(r => r.engine === 'GoRegexp');
    
    let logSum = 0;
    let maxSpeedup = 0;
    let count = 0;

    ourResults.forEach(re => {
        const std = stdResults.find(s => Math.abs(s.s - re.s) < 0.01 && Math.abs(s.b - re.b) < 0.01 && Math.abs(s.l - re.l) < 0.01);
        if (std && std.throughput > 0) {
            const speedup = re.throughput / std.throughput;
            
            logSum += Math.log(speedup);
            if (speedup > maxSpeedup) maxSpeedup = speedup;
            count++;
        }
    });

    if (count > 0) {
        // Geometric Mean is more appropriate for ratios
        const geoMean = Math.exp(logSum / count);
        document.getElementById('avg-speedup').textContent = geoMean.toFixed(1) + 'x';
        document.getElementById('max-speedup').textContent = maxSpeedup.toFixed(1) + 'x';
    }
    document.getElementById('regression-count').textContent = 'Calculating...';
}

function renderLandscape(results) {
    const selector = document.getElementById('l-slice');
    const lSlice = parseFloat(selector.value);
    
    const ourResults = results.filter(r => r.engine === 'GoRegexpRe' && Math.abs(r.l - lSlice) < 0.05);
    const stdResults = results.filter(r => r.engine === 'GoRegexp' && Math.abs(r.l - lSlice) < 0.05);

    const sValues = [...new Set(results.map(r => r.s))].sort((a, b) => b - a);
    const bValues = [...new Set(results.map(r => r.b))].sort((a, b) => a - b);
    
    const zData = bValues.map(b => sValues.map(s => {
        const re = ourResults.find(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
        const std = stdResults.find(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
        if (re && std && std.throughput > 0) {
            const speedup = re.throughput / std.throughput;
            // Return log10 for logarithmic color mapping
            return Math.log10(Math.max(speedup, 0.1));
        }
        return null;
    }));

    const data = [{
        z: zData,
        x: sValues,
        y: bValues,
        type: 'heatmap',
        colorscale: 'Portland',
        colorbar: { 
            title: 'Speedup',
            tickmode: 'array',
            tickvals: [0, 1, 2, 3, 4, 5],
            ticktext: ['1x', '10x', '100x', '1kx', '10kx', '100kx']
        },
        hoverongaps: false,
        hovertemplate: 'S: %{x}<br>B: %{y}<br>Speedup: %{customdata}x<extra></customdata></extra>',
        customdata: bValues.map(b => sValues.map(s => {
            const re = ourResults.find(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
            const std = stdResults.find(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
            return (re && std && std.throughput > 0) ? (re.throughput / std.throughput).toFixed(1) : 'N/A';
        }))
    }];

    const layout = {
        title: `S x B Performance Landscape (Log Scale, L=${lSlice})`,
        xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
        yaxis: { title: 'Complexity (B)' }
    };

    Plotly.newPlot('landscape-chart', data, layout);
    selector.onchange = () => {
        renderLandscape(results);
        renderRegression(results);
        renderDeepDive(results);
    };
}

async function renderTrends() {
    try {
        const response = await fetch('data/history.json');
        if (!response.ok) throw new Error('data/history.json not found');
        const history = await response.json();

        const dates = history.map(h => h.date);
        const speedups = history.map(h => h.avg_speedup);

        const data = [
            { 
                x: dates, 
                y: speedups, 
                name: 'Avg Speedup', 
                type: 'scatter', 
                mode: 'lines+markers',
                line: { shape: 'spline', color: '#007bff' },
                marker: { size: 8 }
            }
        ];

        const layout = {
            title: 'Historical Performance Tracking (Re / Go)',
            xaxis: { title: 'Commit Date', tickangle: -45 },
            yaxis: { title: 'Avg Speedup (x)', rangemode: 'tozero' },
            margin: { b: 100 }
        };

        Plotly.newPlot('trends-chart', data, layout);
    } catch (err) {
        console.warn('Trends Chart Error:', err);
        document.getElementById('trends-chart').innerHTML = `<p style="padding: 100px; text-align: center; color: #999;">Error loading trends: ${err.message}</p>`;
    }
}

async function renderRegression(currentResults) {
    try {
        const hResponse = await fetch('data/history.json');
        if (!hResponse.ok) throw new Error('data/history.json not found');
        const history = await hResponse.json();

        if (history.length < 2) {
            document.getElementById('regression-chart').innerHTML = '<p style="padding: 100px; text-align: center; color: #999;">Need at least two data points for regression analysis.</p>';
            return;
        }

        const prevEntry = history[history.length - 2];
        const pResponse = await fetch(`benchmarks/history/${prevEntry.file}`);
        if (!pResponse.ok) throw new Error(`Failed to load ${prevEntry.file}`);
        const prevResults = await pResponse.json();

        const selector = document.getElementById('l-slice');
        const lSlice = parseFloat(selector.value);
        
        // Use our engine results for comparison
        const curOur = currentResults.filter(r => r.engine === 'GoRegexpRe' && Math.abs(r.l - lSlice) < 0.05);
        const prevOur = prevResults.filter(r => r.engine === 'GoRegexpRe' && Math.abs(r.l - lSlice) < 0.05);

        const sValues = [...new Set(currentResults.map(r => r.s))].sort((a, b) => b - a);
        const bValues = [...new Set(currentResults.map(r => r.b))].sort((a, b) => a - b);
        
        let regressionCount = 0;

        const zData = bValues.map(b => sValues.map(s => {
            const cur = curOur.find(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
            const prev = prevOur.find(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
            if (cur && prev && prev.throughput > 0) {
                const diff = (cur.throughput - prev.throughput) / prev.throughput * 100;
                if (diff < -5.0) regressionCount++; // Count > 5% drop as regression
                return diff;
            }
            return null;
        }));

        const data = [{
            z: zData,
            x: sValues,
            y: bValues,
            type: 'heatmap',
            colorscale: 'RdBu',
            reversescale: true,
            zmid: 0,
            colorbar: { title: 'Diff (%)' },
            hoverongaps: false
        }];

        const layout = {
            title: `Regression Heatmap (Current vs ${prevEntry.sha}, L=${lSlice})`,
            xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
            yaxis: { title: 'Complexity (B)' }
        };

        Plotly.newPlot('regression-chart', data, layout);
        document.getElementById('regression-count').textContent = regressionCount;
    } catch (err) {
        console.warn('Regression Chart Error:', err);
        document.getElementById('regression-chart').innerHTML = `<p style="padding: 100px; text-align: center; color: #999;">Error loading regression: ${err.message}</p>`;
    }
}

function renderDeepDive(results) {
    const selector = document.getElementById('l-slice');
    const lSlice = parseFloat(selector.value);
    const bTarget = Math.max(...results.map(r => r.b)); // Use highest complexity for deep dive

    const ourData = results.filter(r => r.engine === 'GoRegexpRe' && Math.abs(r.b - bTarget) < 0.01 && Math.abs(r.l - lSlice) < 0.05);
    const stdData = results.filter(r => r.engine === 'GoRegexp' && Math.abs(r.b - bTarget) < 0.01 && Math.abs(r.l - lSlice) < 0.05);

    ourData.sort((a, b) => b.s - a.s);
    stdData.sort((a, b) => b.s - a.s);

    const data = [
        {
            x: ourData.map(d => d.s),
            y: ourData.map(d => d.throughput),
            name: 'go-regexp-re',
            type: 'scatter',
            mode: 'lines+markers'
        },
        {
            x: stdData.map(d => d.s),
            y: stdData.map(d => d.throughput),
            name: 'Go standard',
            type: 'scatter',
            mode: 'lines+markers'
        }
    ];

    const layout = {
        title: `Throughput Profile (B=${bTarget.toFixed(2)}, L=${lSlice})`,
        xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
        yaxis: { title: 'Throughput (MB/s)', type: 'log' }
    };

    Plotly.newPlot('deepdive-chart', data, layout);
}
