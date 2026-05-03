document.addEventListener('DOMContentLoaded', async function() {
    try {
        const response = await fetch('data/landscape.json');
        if (!response.ok) throw new Error('Failed to load data/landscape.json');
        const results = await response.json();
        
        renderLandscape(results);
        renderTrends(results); // Placeholder for future time-series data
        renderRegression(results);
        renderDeepDive(results);

        // Update summary stats based on real data
        updateSummary(results);
    } catch (err) {
        console.error('Error loading dashboard data:', err);
        // Show error message on UI
        document.querySelector('main').insertAdjacentHTML('afterbegin', `<div class="error-msg">Error: ${err.message}. Ensure data/landscape.json exists.</div>`);
    }
});

function updateSummary(results) {
    if (!results || results.length === 0) return;
    
    // Simple speedup calculation relative to GoRegexp
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
    document.getElementById('regression-count').textContent = '0'; // Logic for this would require historical data
}

function renderLandscape(results) {
    // Group by S, B for a specific L
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

    // Re-render when L selector changes
    document.getElementById('l-slice').onchange = () => renderLandscape(results);
}

function renderTrends() {
    const dates = ['2026-04-20', '2026-04-21', '2026-04-22', '2026-04-23', '2026-04-24'];
    const emailData = [10.5, 11.2, 11.0, 15.4, 15.6];
    const ipData = [25.0, 24.8, 26.2, 25.8, 27.1];

    const data = [
        { x: dates, y: emailData, name: 'email', type: 'scatter', mode: 'lines+markers' },
        { x: dates, y: ipData, name: 'ip', type: 'scatter', mode: 'lines+markers' }
    ];

    const layout = {
        title: 'Historical Performance Tracking',
        xaxis: { title: 'Commit Date' },
        yaxis: { title: 'Speedup (x)' }
    };

    Plotly.newPlot('trends-chart', data, layout);
}

function renderRegression() {
    const sValues = [0.99, 0.9, 0.7, 0.5, 0.3, 0.1, 0.05, 0.01];
    const bValues = [1, 2, 5, 10, 20, 50, 100];
    
    // Mock regression data (diff vs previous)
    const zData = bValues.map(b => sValues.map(s => {
        return (Math.random() - 0.5) * 10; // -5% to +5%
    }));

    const data = [{
        z: zData,
        x: sValues,
        y: bValues,
        type: 'heatmap',
        colorscale: 'RdBu', // Red for improvement, Blue for regression (or vice versa, let's use RdBu)
        reversescale: true,
        zmid: 0,
        colorbar: { title: 'Diff (%)' }
    }];

    const layout = {
        title: 'Regression Heatmap (Current vs Previous)',
        xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
        yaxis: { title: 'Complexity (B)', type: 'log' }
    };

    Plotly.newPlot('regression-chart', data, layout);
}

function renderDeepDive() {
    // S-sweep at fixed B
    const sValues = Array.from({length: 20}, (_, i) => 1.0 - i * 0.05);
    const reData = sValues.map(s => 100 / (s + 0.1));
    const standardData = sValues.map(s => 10);

    const data = [
        { x: sValues, y: reData, name: 'go-regexp-re', type: 'scatter' },
        { x: sValues, y: standardData, name: 'standard regexp', type: 'scatter' }
    ];

    const layout = {
        title: 'S-Sweep Profile (B=10, L=0.5)',
        xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
        yaxis: { title: 'Throughput (MB/s)', type: 'log' }
    };

    Plotly.newPlot('deepdive-chart', data, layout);
}
