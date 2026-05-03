# GEMINI.md - go-regexp-re GitHub Pages Viewer Constitution

This document defines the foundational principles and technical mandates for the `gh-pages` branch of the `go-regexp-re` project. As the Gemini CLI agent, you must prioritize these instructions when working on this branch.

## 1. Branch Philosophy
The `gh-pages` branch serves as the **Pure Visualization Viewer** for the regex engine's performance characteristics. It is a data consumer, not a generator.

- **Objective**: Provide a high-signal, interactive dashboard to visualize the "Performance Landscape".
- **Vision**: Enable instant identification of "Dominance Zones" and "Regression Terrains" through multi-dimensional analysis.

## 2. Architectural Mandates

### 2.1 Generator-Viewer Separation (Strict Decoupling)
This branch is strictly prohibited from containing data generation or processing logic.
- **No Go Source**: Go files (benchmark logic, parsers, generators) MUST NOT be committed to this branch.
- **Pure Static Assets**: This branch should only contain HTML, CSS, client-side JS (e.g., Plotly.js), and JSON data artifacts.
- **External Data Supply**: All data files in `data/*.json` are considered external artifacts produced by GitHub Actions on development branches.

### 2.2 Data-Driven Rendering
- **Asynchronous Loading**: The viewer MUST load data asynchronously (e.g., via `fetch`) and handle the absence of data files (e.g., `data/landscape.json`) gracefully without crashing.
- **Zero-Logic JSON**: JSON data should be "rendering-ready". Any complex transformation or normalization MUST be performed by the Generator (on development branches) before deployment to this branch.

### 2.3 Visual Consistency & Performance
- **Visual Impact**: Visualizations MUST prioritize the identification of the "engine's terrain" (S x B x L) over simple list-based rankings.
- **Monospace Aesthetics**: Maintain a professional, engineering-focused look consistent with the project's CLI nature.
- **Plotly Integration**: Utilize Plotly.js for interactive, high-performance charting.

## 3. Project Structure (gh-pages)
- `/index.html`: The main entry point for the dashboard.
- `/js/`: Client-side visualization logic (e.g., `dashboard.js`).
- `/css/`: Dashboard styling.
- `/data/`: **Ephemeral Data Store**. Contains JSON artifacts synced from CI. Files in this directory are subject to being overwritten by every CI run.
- `/benchmarks/`: Historical raw text results (legacy compatibility).

## 4. Engineering Standards
- **Zero-Babel/Zero-Build**: Favor vanilla JavaScript and standard CSS to keep the viewer lightweight and zero-maintenance. Avoid complex frontend build pipelines (Webpack, Vite, etc.) unless explicitly requested.
- **Resilience**: The viewer MUST provide clear status messages if data is missing or if a fetch fails, guiding the user to run the benchmark on the main branch.

---
**Note**: This branch is a destination for CI artifacts. Never perform development of core engine logic here.
