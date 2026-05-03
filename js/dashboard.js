document.addEventListener('DOMContentLoaded', function() {
    renderLandscape();
    renderTrends();
    renderRegression();
    renderDeepDive();

    // Mock summary stats
    document.getElementById('avg-speedup').textContent = '12.5x';
    document.getElementById('max-speedup').textContent = '84.2x';
    document.getElementById('regression-count').textContent = '2';
});

function renderLandscape() {
    const sValues = [0.99, 0.9, 0.7, 0.5, 0.3, 0.1, 0.05, 0.01];
    const bValues = [1, 2, 5, 10, 20, 50, 100];
    
    // Mock speedup data
    const zData = bValues.map(b => sValues.map(s => {
        // Simple heuristic: 
        // high S (sparse) -> high speedup (MAP/SIMD)
        // high B (complex) -> slightly lower speedup (DFA)
        return (s * 100) / (Math.log10(b) + 1);
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
        title: 'S x B Performance Landscape (L=0.2)',
        xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
        yaxis: { title: 'Complexity (B)', type: 'log' }
    };

    Plotly.newPlot('landscape-chart', data, layout);
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
