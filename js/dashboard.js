const L_BINS = [
    { id: 'random',     label: 'Random',     min: 0.0, max: 0.2 },
    { id: 'natural',    label: 'Natural',    min: 0.2, max: 0.6 },
    { id: 'structured', label: 'Structured', min: 0.6, max: 0.8 },
    { id: 'literal',    label: 'Literal',    min: 0.8, max: 1.01 }
];

document.addEventListener('DOMContentLoaded', async function() {
    try {
        const response = await fetch('data/landscape.json');
        if (!response.ok) throw new Error('data/landscape.json not found. Run benchmark on main branch to generate data.');
        const results = await response.json();
        
        // Render all layers of the landscape
        renderLayeredLandscape(results);
        
        // Load history and update summary stats
        await renderTrends();
        
        renderRegression(results);
        renderDeepDive(results);
    } catch (err) {
        console.error('Viewer Error:', err);
        document.querySelector('main').insertAdjacentHTML('afterbegin', `
            <div style="background: #fff3f3; color: #721c24; padding: 20px; border-radius: 8px; margin-bottom: 30px; border: 1px solid #f5c6cb;">
                <strong>Viewer Status:</strong> ${err.message}
            </div>
        `);
    }
});

function renderLayeredLandscape(results) {
    L_BINS.forEach(bin => {
        renderLandscapeBin(results, bin);
    });
}

function renderLandscapeBin(results, bin) {
    const binResults = results.filter(r => r.l >= bin.min && r.l < bin.max);
    
    const ourResults = binResults.filter(r => r.engine === 'GoRegexpRe');
    const stdResults = binResults.filter(r => r.engine === 'GoRegexp');

    const sValues = [...new Set(results.map(r => r.s))].sort((a, b) => b - a);
    const bValues = [...new Set(results.map(r => r.b))].sort((a, b) => a - b);
    
    const zData = bValues.map(b => sValues.map(s => {
        const reMatches = ourResults.filter(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
        const stdMatches = stdResults.filter(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
        
        if (reMatches.length > 0 && stdMatches.length > 0) {
            const avgRe = reMatches.reduce((acc, r) => acc + r.throughput, 0) / reMatches.length;
            const avgStd = stdMatches.reduce((acc, r) => acc + r.throughput, 0) / stdMatches.length;
            return avgStd > 0 ? Math.log10(Math.max(avgRe / avgStd, 0.1)) : null;
        }
        return null;
    }));

    const data = [{
        z: zData,
        x: sValues,
        y: bValues,
        type: 'heatmap',
        colorscale: 'Portland',
        zmin: 0,
        zmax: 5,
        showscale: bin.id === 'literal',
        colorbar: { 
            title: 'Speedup',
            thickness: 15,
            tickvals: [0, 1, 2, 3, 4, 5],
            ticktext: ['1x', '10x', '100x', '1kx', '10kx', '100kx']
        },
        hoverongaps: false,
        hovertemplate: 'S: %{x}<br>B: %{y}<br>Speedup: %{customdata}x<extra></extra>',
        customdata: bValues.map(b => sValues.map(s => {
            const reMatches = ourResults.filter(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
            const stdMatches = stdResults.filter(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
            if (reMatches.length > 0 && stdMatches.length > 0) {
                const avgRe = reMatches.reduce((acc, r) => acc + r.throughput, 0) / reMatches.length;
                const avgStd = stdMatches.reduce((acc, r) => acc + r.throughput, 0) / stdMatches.length;
                return (avgRe / avgStd).toFixed(1);
            }
            return 'N/A';
        }))
    }];

    const layout = {
        margin: { t: 10, b: 25, l: 30, r: bin.id === 'literal' ? 50 : 5 },
        xaxis: { 
            title: 'S', 
            range: [1, 0], 
            tickvals: [0, 0.5, 1],
            fixedrange: true 
        },
        yaxis: { 
            title: bin.id === 'random' ? 'B' : '', 
            range: [0, 1], 
            tickvals: [0, 0.5, 1],
            fixedrange: true 
        },
        hovermode: 'closest',
        font: { size: 9 }
    };

    Plotly.newPlot(`landscape-${bin.id}`, data, layout, {displayModeBar: false});
}

async function renderTrends() {
    try {
        const response = await fetch('data/history.json');
        if (!response.ok) throw new Error('data/history.json not found');
        const history = await response.json();

        // Update summary from the LATEST history entry (ground truth)
        if (history.length > 0) {
            const latest = history[history.length - 1];
            document.getElementById('min-speedup').textContent = latest.min_speedup.toFixed(1) + 'x';
            document.getElementById('avg-speedup').textContent = latest.avg_speedup.toFixed(1) + 'x';
            document.getElementById('max-speedup').textContent = latest.max_speedup.toFixed(1) + 'x';
        }

        const dates = history.map(h => h.date);
        const avgs = history.map(h => h.avg_speedup);
        const mins = history.map(h => h.min_speedup);
        const maxs = history.map(h => h.max_speedup);

        const data = [
            {
                x: dates, y: maxs,
                type: 'scatter', mode: 'lines', line: { width: 0 },
                showlegend: false, hoverinfo: 'skip'
            },
            {
                x: dates, y: mins,
                type: 'scatter', mode: 'lines', line: { width: 0 },
                fill: 'tonexty', fillcolor: 'rgba(0, 123, 255, 0.1)',
                name: 'Min-Max Range', hoverinfo: 'skip'
            },
            { 
                x: dates, y: avgs, 
                name: 'Geo Mean Speedup', 
                type: 'scatter', mode: 'lines+markers',
                line: { shape: 'spline', color: '#007bff', width: 3 },
                marker: { size: 8 }
            }
        ];

        const layout = {
            title: 'Historical Performance Evolution (Min / GeoMean / Max)',
            xaxis: { title: 'Commit Date', tickangle: -45 },
            yaxis: { title: 'Speedup (x)', type: 'log', autorange: true },
            margin: { b: 100 },
            legend: { orientation: 'h', y: -0.2 }
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

        const curOur = currentResults.filter(r => r.engine === 'GoRegexpRe');
        const prevOur = prevResults.filter(r => r.engine === 'GoRegexpRe');

        const sValues = [...new Set(currentResults.map(r => r.s))].sort((a, b) => b - a);
        const bValues = [...new Set(currentResults.map(r => r.b))].sort((a, b) => a - b);
        
        let regressionCount = 0;

        const zData = bValues.map(b => sValues.map(s => {
            const curMatches = curOur.filter(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
            const prevMatches = prevOur.filter(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
            
            if (curMatches.length > 0 && prevMatches.length > 0) {
                const avgCur = curMatches.reduce((acc, r) => acc + r.throughput, 0) / curMatches.length;
                const avgPrev = prevMatches.reduce((acc, r) => acc + r.throughput, 0) / prevMatches.length;
                const diff = (avgCur - avgPrev) / avgPrev * 100;
                if (diff < -10.0) regressionCount++; 
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
            hoverongaps: false,
            hovertemplate: 'S: %{x}<br>B: %{y}<br>Diff: %{customdata}%<extra></extra>',
            customdata: bValues.map(b => sValues.map(s => {
                const curMatches = curOur.filter(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
                const prevMatches = prevOur.filter(r => Math.abs(r.s - s) < 0.01 && Math.abs(r.b - b) < 0.01);
                if (curMatches.length > 0 && prevMatches.length > 0) {
                    const avgCur = curMatches.reduce((acc, r) => acc + r.throughput, 0) / curMatches.length;
                    const avgPrev = prevMatches.reduce((acc, r) => acc + r.throughput, 0) / prevMatches.length;
                    return ((avgCur - avgPrev) / avgPrev * 100).toFixed(2);
                }
                return 'N/A';
            }))
        }];

        const layout = {
            title: `Regression Heatmap (All L Averaged, Current vs ${prevEntry.sha})`,
            xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
            yaxis: { title: 'Complexity (B)' },
            hovermode: 'closest'
        };

        Plotly.newPlot('regression-chart', data, layout);
        document.getElementById('regression-count').textContent = regressionCount;
    } catch (err) {
        console.warn('Regression Chart Error:', err);
        document.getElementById('regression-chart').innerHTML = `<p style="padding: 100px; text-align: center; color: #999;">Error loading regression: ${err.message}</p>`;
    }
}

function renderDeepDive(results) {
    const bTarget = Math.max(...results.map(r => r.b));
    const ourDataRaw = results.filter(r => r.engine === 'GoRegexpRe' && Math.abs(r.b - bTarget) < 0.01);
    const stdDataRaw = results.filter(r => r.engine === 'GoRegexp' && Math.abs(r.b - bTarget) < 0.01);
    const sValues = [...new Set(ourDataRaw.map(r => r.s))].sort((a, b) => b - a);
    
    const ourData = sValues.map(s => {
        const matches = ourDataRaw.filter(r => Math.abs(r.s - s) < 0.01);
        return { s, throughput: matches.reduce((acc, r) => acc + r.throughput, 0) / matches.length };
    });
    
    const stdData = sValues.map(s => {
        const matches = stdDataRaw.filter(r => Math.abs(r.s - s) < 0.01);
        return { s, throughput: matches.reduce((acc, r) => acc + r.throughput, 0) / matches.length };
    });

    const data = [
        { x: ourData.map(d => d.s), y: ourData.map(d => d.throughput), name: 'go-regexp-re', type: 'scatter', mode: 'lines+markers' },
        { x: stdData.map(d => d.s), y: stdData.map(d => d.throughput), name: 'Go standard', type: 'scatter', mode: 'lines+markers' }
    ];

    const layout = {
        title: `Throughput Profile (B=${bTarget.toFixed(2)}, All L Averaged)`,
        xaxis: { title: 'Selectivity (S)', autorange: 'reversed' },
        yaxis: { title: 'Throughput (MB/s)', type: 'log' }
    };

    Plotly.newPlot('deepdive-chart', data, layout);
}
