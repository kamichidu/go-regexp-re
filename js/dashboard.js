document.addEventListener('DOMContentLoaded', async function() {
    try {
        const response = await fetch('data/landscape.json');
        if (!response.ok) throw new Error('data/landscape.json not found. Run benchmark on main branch to generate data.');
        const results = await response.json();
        
        // Render all charts with the loaded data
        renderLandscape(results);
        renderTrends(results);
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

function updateSummary(results) {
    const ourResults = results.filter(r => r.engine === 'GoRegexpRe');
    const stdResults = results.filter(r => r.engine === 'GoRegexp');
    
    let totalSpeedup = 0;
    let maxSpeedup = 0;
    let count = 0;

    ourResults.forEach(re => {
        const std = stdResults.find(s => s.s === re.s && s.b === re.b && s.l === re.l);
        if (std && std.throughput > 0) {
            const speedup = re.throughput / std.throughput;
            totalSpeedup += speedup;
            if (speedup > maxSpeedup) maxSpeedup = speedup;
            count++;
        }
    });

    if (count > 0) {
        document.getElementById('avg-speedup').textContent = (totalSpeedup / count).toFixed(1) + 'x';
        document.getElementById('max-speedup').textContent = maxSpeedup.toFixed(1) + 'x';
    }
    document.getElementById('regression-count').textContent = 'N/A';
}

function renderLandscape(results) {
    const lSlice = parseFloat(document.getElementById('l-slice').value) || 0.1;
    
    const ourResults = results.filter(r => r.engine === 'GoRegexpRe' && Math.abs(r.l - lSlice) < 0.01);
    const stdResults = results.filter(r => r.engine === 'GoRegexp' && Math.abs(r.l - lSlice) < 0.01);

    const sValues = [...new Set(results.map(r => r.s))].sort((a, b) => b - a);
    const bValues = [...new Set(results.map(r => r.b))].sort((a, b) => a - b);
    
    const zData = bValues.map(b => sValues.map(s => {
        const re = ourResults.find(r => r.s === s && r.b === b);
        const std = stdResults.find(r => r.s === s && r.b === b);
        return (re && std && std.throughput > 0) ? re.throughput / std.throughput : null;
    }));

    const data = [{
        z: zData,
        x: sValues,
        y: bValues,
        type: 'heatmap',
        colorscale: 'Portland',
        colorbar: { title: 'Speedup (x)' }
    }];

    const layout = {
        title: `S x B Performance Landscape (L=${lSlice})`,
        xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
        yaxis: { title: 'Complexity (B)', type: 'log' }
    };

    Plotly.newPlot('landscape-chart', data, layout);
    document.getElementById('l-slice').onchange = () => renderLandscape(results);
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

        const lSlice = parseFloat(document.getElementById('l-slice').value) || 0.1;
        
        // Use our engine results for comparison
        const curOur = currentResults.filter(r => r.engine === 'GoRegexpRe' && Math.abs(r.l - lSlice) < 0.01);
        const prevOur = prevResults.filter(r => r.engine === 'GoRegexpRe' && Math.abs(r.l - lSlice) < 0.01);

        const sValues = [...new Set(currentResults.map(r => r.s))].sort((a, b) => b - a);
        const bValues = [...new Set(currentResults.map(r => r.b))].sort((a, b) => a - b);
        
        let regressionCount = 0;

        const zData = bValues.map(b => sValues.map(s => {
            const cur = curOur.find(r => r.s === s && r.b === b);
            const prev = prevOur.find(r => r.s === s && r.b === b);
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
            colorbar: { title: 'Diff (%)' }
        }];

        const layout = {
            title: `Regression Heatmap (Current vs ${prevEntry.sha}, L=${lSlice})`,
            xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
            yaxis: { title: 'Complexity (B)', type: 'log' }
        };

        Plotly.newPlot('regression-chart', data, layout);
        document.getElementById('regression-count').textContent = regressionCount;
    } catch (err) {
        console.warn('Regression Chart Error:', err);
        document.getElementById('regression-chart').innerHTML = `<p style="padding: 100px; text-align: center; color: #999;">Error loading regression: ${err.message}</p>`;
    }
}

function renderDeepDive(results) {
    const lSlice = parseFloat(document.getElementById('l-slice').value) || 0.1;
    const bTarget = Math.max(...results.map(r => r.b)); // Use highest complexity for deep dive

    const ourData = results.filter(r => r.engine === 'GoRegexpRe' && r.b === bTarget && Math.abs(r.l - lSlice) < 0.01);
    const stdData = results.filter(r => r.engine === 'GoRegexp' && r.b === bTarget && Math.abs(r.l - lSlice) < 0.01);

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
        title: `Throughput Profile (B=${bTarget}, L=${lSlice})`,
        xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
        yaxis: { title: 'Throughput (MB/s)', type: 'log' }
    };

    Plotly.newPlot('deepdive-chart', data, layout);
}
