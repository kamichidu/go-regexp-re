# GEMINI.md - go-regexp-re Project Constitution

This document defines the foundational principles and technical mandates for the `go-regexp-re` project. As the Gemini CLI agent, you must prioritize these instructions over general defaults for all development, refactoring, and optimization tasks.

## 1. Project Philosophy
`go-regexp-re` is a **Pure Go, high-performance DFA regular expression engine** designed to surpass the physical throughput limits of the standard `regexp` package.

- **Objective**: Achieve 5x to 100x higher throughput than Go's standard `regexp` while strictly guaranteeing $O(n)$ time complexity.
- **Vision**: To evolve the concept of `Regexp::Assemble` into a modern engine optimized for CPU cache locality and pipeline efficiency.

## 2. Core Architectural Mandates
Every implementation must adhere to these four pillars to ensure maximum performance:

### 2.1 Deterministic Finite Automaton (DFA)
- **Deterministic Transitions**: Patterns must be pre-compiled into a single transition table where `table[state][byte]` leads to exactly one state.
- **Constant Time Per Byte**: Processing cost per byte must be fixed at $O(1)$, regardless of pattern complexity.

### 2.2 Byte-Oriented Scanning
- **Eliminate Rune Decoding**: Abandon mandatory UTF-8 to `rune` decoding. Scan `[]byte` directly to maximize CPU pipeline efficiency.
- **Byte-Level Transitions**: All state transitions must operate on raw bytes to minimize branching and memory latency.

### 2.3 Cache Locality Optimization
- **Flattened Memory Layout**: Transition tables MUST be stored as a single, contiguous `int32` array. Access must use `table[state * stride + byte]` to eliminate pointer chasing and maximize L1/L2 cache hit rates.
- **Minimize Memory Latency**: Keep core data structures small enough to fit within L2/L3 caches even for large pattern sets.

### 2.4 Execution Switching Strategy
To maximize throughput, the engine MUST select the most efficient execution loop based on pattern characteristics:
- **Fast Path (Pure DFA)**: Automatically selected for patterns without anchors. It utilizes a minimalist execution loop with zero boundary/context checks to approach raw memory bandwidth speeds.
- **Extended Path (Virtual Byte Insertion)**: Selected for patterns with anchors (e.g., `^`, `$`, `\b`). It employs "Virtual Bytes" (indices 256+) injected at character boundaries to process empty-width assertions within the DFA's $O(n)$ framework.
- **Submatch Hybrid Path (2-Pass)**: Selected when submatches are requested. It combines a high-speed DFA scan to identify match boundaries with a targeted NFA rescan for precise submatch extraction. This approach guarantees $O(n)$ performance while avoiding the state explosion risks associated with 1-pass tagged DFAs.

### 2.5 Submatch Extraction Architecture (Hybrid 2-Pass Strategy)
To ensure absolute $O(n)$ execution and maintain L1/L2 cache locality, the engine adopts a **Hybrid 2-Pass** architecture for submatch extraction. This strategy is the definitive architectural choice, prioritizing system stability and memory predictability over the theoretical ideal of 1-pass submatch resolution, which frequently triggers catastrophic state explosion.

- **Phase 1: DFA Boundary Scan**:
  - A specialized DFA identifies the overall match boundaries (Start/End).
  - This phase records only the minimum necessary tags (Capture 0) to resolve the leftmost-first match range.
  - By excluding internal submatch tags from DFA states, we physically prevent state explosion and maintain fixed, predictable memory requirements during compilation.
- **Phase 2: Targeted NFA Rescan**:
  - Once a match range is identified, the engine performs a targeted rescan using an NFA (Pike VM) ONLY within the identified `[start, end]` bounds.
  - By limiting the NFA scan to the precise match region, we keep the overhead negligible while ensuring 100% compatibility with standard Go `regexp` submatch results.
- **Architecture Rationale**: This hybrid approach provides the best balance of "raw DFA speed" for the bulk scan and "guaranteed correctness" for submatch extraction without the risk of non-deterministic memory consumption or compilation failure due to state explosion.

### 2.6 Prefix-Skip Optimization (SIMD Acceleration)
To maximize throughput for patterns with literal prefixes, the engine MUST utilize a **Prefix-Skip** optimization:
- **Mandatory Prefix Extraction**: During compilation, the longest constant prefix is extracted from the pattern.
- **SIMD-Accelerated Skipping**: All execution loops (Fast, Extended, and Submatch) MUST use `bytes.Index` (which leverages platform-specific SIMD instructions) to rapidly skip non-matching segments of the input.
- **Pre-calculated Prefix State**: The DFA state reached after matching the mandatory prefix (`prefixState`) is pre-calculated during compilation. This allows the engine to resume DFA execution directly from the state following the prefix, eliminating redundant transitions.

### 2.7 Pure Go (No CGO)
- **Zero Overhead**: CGO is strictly prohibited to avoid context-switching overhead and maintain Go's native portability and build simplicity.

### 2.8 Literal Match Bypass (0-Pass Strategy)
- **Direct Literal Resolution**: If the entire pattern is a constant literal and no capturing groups are present, the engine MUST completely bypass both DFA and NFA stages and use `bytes.Index` or other standard library functions directly for match and submatch extraction.
- **Zero-Engine Overhead**: This "0-Pass Strategy" physically eliminates regex engine overhead for simple literal searches, guaranteeing peak performance equivalent to optimized standard library string search functions.

## 3. Feature Selection Policy (Performance over Features)

### 3.1 Supported Features
- **Standard Syntax Compatibility**: Accept `syntax.Prog` instruction sequences from the standard Go parser.
- **Anchors & Boundaries**: Support `^`, `$`, `\b`, `\B` and multiline anchors via the **Virtual Byte Insertion** mechanism.
- **Capturing Groups**: Support extraction by recording offsets into fixed-size register arrays via the **Hybrid 2-Pass Strategy**.
- **Fixed-Length Lookahead/Lookbehind**: Support assertions that can be statically integrated into the DFA transition graph during compilation.

### 3.2 Excluded Features
- **Backreferences**: Strictly excluded to maintain $O(n)$ complexity and prevent exponential "catastrophic backtracking."
- **Dynamic Lookaround**: Complex or recursive assertions that require significant backtracking are restricted.

## 4. Engineering & Validation Standards
- **Performance-First Benchmarking**: Any change must be validated against the standard `regexp` package. Significant throughput regressions are unacceptable.
- **Scalability for Large Pattern Sets**: Ensure the engine maintains $O(n)$ performance (proportional only to input length) even when merging tens of thousands of patterns.
- **SIMD-Driven Throughput**: Aim for 5x to 100x higher throughput than standard `regexp` by leveraging SIMD-accelerated prefix-skipping combined with $O(1)$ per-byte DFA transitions.

## 5. Coding Conventions
- **Explicit Aliasing for Standard Regexp Packages**: To avoid confusion between this engine and the standard library, always use explicit aliases when importing Go's standard `regexp` packages:
  - `regexp` must be imported as `goregexp`.
  - `regexp/syntax` must be imported as `gosyntax`.

---
**Note**: If a user request contradicts these principles, you MUST highlight the conflict and explain the potential performance impact before proceeding.
