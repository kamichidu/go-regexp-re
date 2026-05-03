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

function renderTrends(results) {
    // Note: Trends will eventually need a separate history.json
    // For now, we just show a "Single Snapshot" message or clear the div
    document.getElementById('trends-chart').innerHTML = '<p style="padding: 100px; text-align: center; color: #999;">Historical tracking requires multiple data snapshots. Current view: Single Snapshot.</p>';
}

function renderRegression(results) {
    // Logic for regression heatmap (Current vs Baseline)
    // This requires two data sets. Placeholder for now.
    document.getElementById('regression-chart').innerHTML = '<p style="padding: 100px; text-align: center; color: #999;">Regression analysis requires a baseline.json to compare against.</p>';
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
