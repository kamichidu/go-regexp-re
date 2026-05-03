# GEMINI.md - go-regexp-re Project Constitution

This document defines the foundational principles and technical mandates for the `go-regexp-re` project. As the Gemini CLI agent, you must prioritize these instructions over general defaults for all development, refactoring, and optimization tasks.

## 1. Project Philosophy
`go-regexp-re` is a **Pure Go, high-performance DFA regular expression engine** designed to surpass the physical throughput limits of the standard `regexp` package.

- **Objective**: Achieve 5x to 100x higher throughput than Go's standard `regexp` while strictly guaranteeing $O(n)$ time complexity.
- **Vision**: To evolve the concept of `Regexp::Assemble` into a modern engine optimized for CPU cache locality and pipeline efficiency.

## 2. Core Architectural Mandates
Every implementation must adhere to these pillars to ensure maximum performance:

### 2.1 Deterministic Finite Automaton (DFA)
- **Deterministic Transitions**: Patterns must be pre-compiled into a single transition table or a bit-parallel state vector where one input byte leads to exactly one deterministic set of states.
- **Constant Time Per Byte**: Processing cost per byte must be fixed at $O(1)$, regardless of pattern complexity.

### 2.2 Byte-Oriented Scanning
- **Eliminate Rune Decoding**: Abandon mandatory UTF-8 to `rune` decoding. Scan `[]byte` directly to maximize CPU pipeline efficiency.
- **Byte-Level Transitions**: All state transitions must operate on raw bytes to minimize branching and memory latency.

### 2.3 Cache Locality Optimization
- **Flattened Memory Layout**: Transition tables MUST be stored as a contiguous `int32` array. Access must use `table[(state << 8) | byte]` to eliminate multiplication and maximize L1/L2 cache hit rates.
- **Minimize Memory Latency**: Keep core structures small enough to fit within L2/L3 caches even for large pattern sets.

### 2.4 Execution Switching Strategy
To maximize throughput, the engine MUST select the most efficient execution loop based on pattern characteristics and the requested operation. The core execution loops are implemented in **`exec_dfa.go`** and dispatched via **`exec_dispatch.go`**:

- **0-Pass (Literal Bypass)**: Selected for pure constant strings and anchored literals. Bypasses all DFA construction. Uses pointer-passing and direct bytes.Equal/Index to achieve 0-allocation parity with standard library.
- **Fast Path (Boundary-Only Discovery)**: Selected for `Match` and `FindIndex` calls where capture groups are not required. It utilizes a minimalist execution loop (Pass 0 + Pass 1) with zero history recording and early exit on first match.
- **Full Path (Multi-Pass Sparse TDFA)**: Selected for `FindSubmatchIndex` calls. It employs the comprehensive 5-pass pipeline to guarantee peak performance and Go parity:
    - **Pass 0 (MAP)**: Primary search phase. Identifies mandatory paths and extracts anchor candidates (**Prefix**, **Pivot**, or **Suffix**).
    - **Pass 1: Boundary Discovery (Searching DFA)**: Uses a Safe Searching DFA to identify match end and winning priority in $O(n)$.
    - **Pass 1.5: Leftmost Start Discovery**: Since searching DFAs conflate start positions, the engine performs a manual scan to find the exact leftmost `start`.
    - **Pass 2: Anchored Recording (Precise Forward Scan)**: Re-runs an Anchored DFA over the identified match range to generate a noise-free execution history. History initialization MUST be $O(1)$ relative to total input.
    - **Pass 3 & 4: Extraction (TDFA Trace)**: Reconstructs the winning path via backward trace and licks capture tags using bit-parallel updates.
- **SWAR Character Class Warp (CCWarp)**: A specialized execution strategy for "Pure Self-Loops". Detects universal sets (.*) and performs an $O(1)$ jump to the end of the buffer to match memory bandwidth limits.
- **Anchor-Aware Guarded SIMD Warp**: Selected for patterns with anchors. Utilizes a separate `anchorTransitions` table and **guarded warp points** to allow SIMD skipping even in the presence of anchors (e.g., `^`, `$`, `\b`).
- **Early Exit on Match Finality**: To maintain peak efficiency, execution loops MUST adopt an **Early Exit on Mismatch** strategy: once a match is found and the DFA can no longer transition, the loop returns the best match immediately instead of continuing a redundant search.
- **Explicit Hot-Loop Monomorphization**: To ensure zero-overhead, the engine MUST avoid Go generics (`GCShape` sharing). Instead, it employs manually monomorphized functions (e.g., `fastMatchExecLoop`, `extendedMatchExecLoop`) to ensure the Go compiler can completely eliminate unreachable branches and avoid runtime dictionary lookups.

- **State-Resident Acceptance Flag**: To eliminate redundant memory lookups in hot loops, DFA state IDs MUST embed the `AcceptingStateFlag` (Bit 29). This allows the execution loop to identify accepting states via a single bitwise AND operation on the current state variable.
- **Inlining-Friendly Anchor Verification**: Anchor verification logic (e.g., `^`, `$`, `\b`) MUST be implemented as tiny, specialized functions (e.g., `VerifyBegin`, `VerifyEnd`) to guarantee inlining by the Go compiler. Complex junction analysis MUST be avoided in favor of short-circuiting these specialized checks to minimize call-stack overhead and branch misprediction.
- **Bitmask-First Short-Circuiting Mandate**: Every verification function MUST employ a single-instruction bitmask check (e.g., `if (req & MASK) == 0 { return true }`) as its first operation. This ensures that any transition not requiring specific anchor checks can exit in $O(1)$ with minimal pipeline disturbance. Failure to follow this pattern has been proven to cause 30-60% throughput regressions.
- **Pointer-Passing for Input Context**: To avoid the 48-byte struct copy overhead (`runtime.duffcopy`) in hot loops, the `ir.Input` structure MUST be passed by pointer (`*ir.Input`) to all anchor verification and execution helper functions.
- **Pre-calculated Search/Match States**: The initial `searchState` and `matchState` MUST be pre-calculated during compilation with all relevant flags already applied, eliminating per-match setup costs.

### 2.5 Submatch Extraction Architecture (Multi-Pass Sparse TDFA)
The engine follows a **Multi-Pass Sparse TDFA** strategy (implemented in **`exec_tdfa.go`**) to guarantee peak performance, $O(n)$ time complexity, and production-grade Go parity.

- **NFA-Free & Calculation-Free Mandate**: Runtime NFA simulation, backtracking, or dynamic priority comparison is **STRICTLY PROHIBITED**. All submatch extraction decisions MUST be pre-calculated and "burned into" the transition tables during compilation.
- **Pass 1: Boundary Discovery (Searching DFA)**: A high-speed forward scan that determines the exact match end and the winning priority.
    - **Guaranteed Compatibility**: To resolve complex leftmost-first and non-greedy ties (e.g., `a*?` vs `a*`), the Searching DFA incorporates the start node into a dedicated `SearchState`, providing a safe $O(n)$ entry point without state explosion.
    - **Leftmost Start Discovery**: Since searching DFAs naturally conflate start positions, the engine performs a manual scan (Pass 1.5) to find the exact leftmost `start` that corresponds to the identified `end` and `priority`.

- **Pass 2: Anchored Recording**: A precise forward scan re-runs an **Anchored DFA** strictly over the identified match range `[start, end]` to generate a noise-free execution history.
    - **On-Demand History Initialization Mandate**: To eliminate the $O(n)$ memory bottleneck, memory for `pathHistory` MUST be cleared and prepared **exactly once**, only when a match is confirmed and Pass 2 is about to begin. This ensures that `matchContext.prepare` is $O(1)$ relative to total input.
    - **Hybrid RLE History Mandate**: The engine MUST employ a **32-bit bit-packed RLE** strategy during SWAR skips. Consecutive bytes in the same state MUST be collapsed into a single "Warp Entry" with a length field (capped at 2047 bytes).
- **Pass 3: Path Identity Selection**: Identifies the unique "winning NFA path" by **performing a backward trace** from the match end point.
    - **Defensive History Indexing Mandate**: To prevent `index out of range` panics, Pass 3 and Pass 4 MUST access `mc.history` using defensive indexing: the match-end state MUST be anchored at `len(mc.history)-1`, and backward steps MUST be relative to this tail index. This ensures robustness regardless of the search start position.
    - **Path Reconstruction**: This leverages the `MatchPriority` determined in Pass 1 to reconstruct the priority identity sequence without lookahead or ambiguity. **To maintain submatch precision for multi-byte runes, Pass 3 MUST trace every byte transition in the history.**
- **Pass 4: Group-Specific Recap (Licking)**: Iterates forward along the confirmed winning path and applies delta tags from the `RecapTable`. This pass MUST be a pure, sequential update loop ("licking") where later tags on the path define the final boundaries.
- **Bit-Parallel Tag Updating Mandate**: To maximize throughput, the engine MUST utilize bit-parallel operations (e.g., `math/bits.TrailingZeros64`) to process only the set bits in the tag delta. This ensures that group updates are $O(1)$ relative to the number of *active* tags per step.
- **Transition-Resident Tags (Delta-Only)**: Capturing group boundaries (tags) MUST be associated exclusively with **transitions** (`RecapEntry`).
- **Priority Delta Propagation**: Priority MUST be tracked using **Priority Deltas** stored in `TransitionUpdate.BasePriority`.
- **Naked History Isolation (Panic Prevention)**: To maintain $O(1)$ table access in Pass 3 and 4 without redundant boundary checks, the execution history (`mc.history`) MUST store only the raw state index. All control flags (Tagged, Anchor, Warp) MUST be physically stripped via `StateIDMask` before recording the trace.

#### 2.5.2 Priority-Aware Anchor Masking (Mandate)
To prevent "Anchor Pollution", DFA subset construction MUST filter anchor verification flags (`preGuard`) by NFA path priority. A transition's anchor requirements MUST be derived exclusively from the **highest-priority NFA paths** that match the current byte. This prevents low-priority Search Restart paths (which often carry `^` or `\b` constraints) from incorrectly imposing their restrictions onto high-priority continuation paths, ensuring that valid matches are never blocked by unrelated anchor mismatches.

#### 2.5.1 NFA-Free Path Selection Mandate (Pass 3)
- **NFA-Free Path Selection Mandate (Pass 3)**: Path selection in Pass 3 MUST NOT employ runtime NFA simulation or dynamic priority comparisons. The identity of the winning path must be reconstructed solely by following pre-calculated priority transitions (`InputPriority` -> `NextPriority`) stored within the `RecapTable`.
- **Bit-Parallel Tag Updating Mandate (Pass 4)**: To maximize throughput during the recap phase, the engine MUST utilize bit-parallel operations (e.g., `math/bits.TrailingZeros64`) to process only the set bits in the tag delta. This ensures that group updates are $O(1)$ relative to the number of *active* tags per step, rather than $O(N)$ for all possible tags.
- **Zero-Allocation Execution**: All recap paths MUST be strictly iterative and utilize stack-resident or pre-allocated buffers, ensuring zero heap allocations during execution. The `matchContext` MUST provide a pre-allocated `regs` buffer for submatch indices to eliminate per-match allocation of the result slice.

- **Naked History Isolation (Panic Prevention)**: To maintain $O(1)$ table access in Pass 3 and 4 without redundant boundary checks, the execution history (`mc.history`) MUST store only the raw state index. All control flags (Tagged, Anchor, Warp) MUST be physically stripped via `StateIDMask` before recording the trace.

### 2.6 Physical Prevention of State Explosion (Naked State Identity)
To achieve scalability, DFA construction (Subset Construction) employs **Naked State Identity**:
- **Identity via NFA Set**: DFA state identity is primarily defined by the NFA state set.
- **Additive Memory Structure**: Limits memory usage to `O(DFA States + Σ Group Tables)`, ensuring that total memory consumption is linear relative to the number of capturing groups.

### 2.7 Static Compatibility Check & Structural Rejection
To maintain the integrity of the NFA-free architecture, the engine MUST perform a static analysis during compilation on the optimized AST:
- **Epsilon Cycle Rejection**: Patterns that match empty strings in a loop (e.g., `(|a)*`), where deterministic path selection is impossible, MUST be rejected.
- **Ambiguous Capture Rejection**: Patterns with structural ambiguities that the Multi-Pass TDFA cannot reliably resolve MUST be rejected at compile time:
    - **Explicit Empty Alternatives in Captures**: e.g., `(|a)`, `(a|)`, `(a||b)`.
    - **Optional Empty Captures**: e.g., `(a*)?`, `(a?|b?)?`.
- **Deterministic Guarantee**: Only patterns whose submatch extraction can be perfectly "burned" into a deterministic table are supported.
- **Error Type**: Violations MUST return a **`regexp.UnsupportedError`** (aliased from `syntax.UnsupportedError`). This allows callers to distinguish between syntax errors and engine limitations.

### 2.8 Architectural Shortcut (Compilation Efficiency)
To minimize compilation overhead, the engine MUST use an **Architectural Shortcut** for simple patterns.
- **Literal-Only Bypass**: If a pattern is identified as a literal-only or anchor-literal sequence, the engine MUST skip `ir.DFA` construction entirely and delegate all operations to the `LiteralMatcher`.
- **ASCII Restriction**: DFA construction is currently optimized for ASCII-only runes (0-127) when possible. Patterns requiring multi-byte UTF-8 support (e.g., non-ASCII runes or `.`) MUST utilize the full table-based DFA with UTF-8 handling.

### 2.10 Multi-Point Anchor & Constraint Optimization (Pass 0 - MAP)
The engine MUST extract the most selective anchors from mandatory AST paths to minimize DFA activations.
- **Multi-Entry Point Discovery**: The engine MUST traverse **`OpAlternate`** to identify all possible entry points and categorize anchors into **Prefix** (start-anchored), **Pivot** (middle-anchored), or **Suffix** (end-anchored) candidates for EACH mandatory path (**Covering Set**).
- **Anchor Selection Heuristic**: Anchors MUST be selected based on a heuristic score that prioritizes length, specificity, and fixed-distance status.
- **Line-Anchored Jump Mandate**: For non-multiline patterns, the engine MUST use **Line-Anchored Jump** to warp the search starting point directly to the beginning of the line where an anchor is found, bypassing redundant DFA transitions.
- **Merged Newline Discovery**: To maximize throughput, the engine MUST utilize **Merged Newline Detection** within SWAR kernels to identify line boundaries and pattern anchors in a single pass.
- **Mandatory & Fixed-Distance Safety**: Exclusive skipping (jumping `restartBase`) MUST be restricted to anchors that are both **Mandatory** for the whole pattern and have a **Fixed Distance** from the match start to guarantee leftmost-longest correctness.
- **Capture Transparency**: Anchor extraction MUST be **Capture-Stripped**—ignoring `OpCapture` boundaries to merge adjacent literals into longer anchors.
- **SIMD/SWAR Discovery**: Use SIMD (`bytes.Index`, `bytes.IndexAny`) for literals and SWAR (`IndexClass`) for character classes.
- **Separation of Concerns (Search vs. Match)**: MAP is responsible for **Searching** (finding match start candidates). DFA is responsible for **Validation** (Anchored matching from the candidate position).
- **Forward/Backward Constraint Guard**: Once an anchor candidate is found, validate surrounding character constraints (fixed-length or dynamic warps) using path-specific SWAR kernels before starting the DFA.

### 2.11 Pure Go (No CGO)
- **Zero Overhead**: Native Go only. CGO is strictly prohibited.

### 2.12 Priority Normalization & Absolute Tracking
- **Priority Normalization**: During DFA construction, NFA path priorities within each state MUST be normalized.
- **Absolute Priority Tracking**: The engine MUST track cumulative priority to identify the true leftmost-first match during Phase 1.

### 2.13 Early Exit Optimization (IsBestMatch)
- **Strict Greedy Finality**: A DFA state is considered to have an unbeatable match (`IsBestMatch == true`) ONLY if it is an accepting state with the highest possible priority (`matchP == 0`) and starts at the earliest possible position in the current search window (`minP == 0`). This ensures that greedy repetitions do not terminate prematurely.

### 2.14 State Explosion Protection (Configurable & Scalable)
- **Default Memory Threshold**: The DFA transition table is typically limited to **64MiB**.
- **Graceful Failure**: If a pattern exceeds the configured `MaxMemory`, the engine MUST return `regexp: pattern too large or ambiguous`.
- **Dynamic Offloading**: When `MaxMemory` exceeds 1GiB, the engine SHOULD switch the NFA path set storage from memory to a **File-based backend**.

### 2.15 Syntax-Level Optimization & AST Rewriting
- **Mandatory Optimization**: The engine MUST call `syntax.Simplify` and `syntax.Optimize` before compilation. Static compatibility checks MUST be performed on the resulting optimized AST to avoid false positives.

### 2.19 Submatch Precision & High-Fidelity Go Parity (Field-Proven)
- **Overall Match Boundaries**: The engine MUST guarantee that indices 0 and 1 strictly match the standard library's results.
- **Leftmost-First Tagging**: During Pass 4 licking, Start tags (even bits: 2, 4, ...) MUST be fixed once set (leftmost), while End tags (odd bits: 3, 5, ...) MUST be updated by the latest encounter on the winning path.
- **Lead-Byte Warp (Jump Optimization)**: For the "any-rune" (dot) and wide character classes, the DFA MUST employ a **Warp-on-Lead-Byte** strategy. **The Warp flag is applied ONLY when all NFA paths in a state accept any valid UTF-8 continuation (e.g., InstRuneAny).**
- **Warp Flag Preservation**: The `WarpStateFlag` (Bit 21) MUST be preserved during DFA minimization.

#### 2.19.1 Multi-byte Dot ('.') Determinism Mandate
To ensure DFA determinism and $O(1)$ transitions, the behavior of `.` (dot) is strictly defined as matching a single byte (for ASCII and invalid UTF-8 bytes) or a static lead-byte unit (for valid multi-byte UTF-8).
- **Invalid UTF-8 Handling**: Single bytes in the ranges `80-BF`, `C0-C1`, and `F5-FF` are treated as valid 1-byte matches for `.` to maintain parity with Go's standard library.
- **Warp Safety**: The execution loop MUST use a robust `GetTrailingByteCount` to ensure index increments do not overflow when encountering these invalid bytes during a Warp skip.
- **Submatch Precision**: Internal capturing group boundaries that fall within an invalid UTF-8 sequence are handled on a best-effort basis and may deviate from standard `regexp` to maintain $O(n)$ performance.

#### 2.19.2 Calculation-Free Boundary Analysis Mandate
Junction verification for anchors (`\b`, `^`, `$`) MUST NOT employ `utf8.DecodeRune`. Word boundaries (`\b`) are defined as **ASCII Word Boundaries**; multi-byte bytes (0x80+) are treated as non-word characters. **Execution loops MUST propagate the Absolute Position Context via `ir.Input` to ensure accurate boundary evaluation even when scanning partial segments.**

### 2.20 Byte-Parallel SWAR Execution (Mandate)
To achieve the $O(n)$ physical throughput goal, the engine MUST implement a hierarchical SWAR (SIMD Within A Register) execution strategy for character classes and repetitions.

- **Hierarchical Kernel Selection**: The engine MUST automatically select the most efficient kernel based on the character set complexity (Equal -> Range -> Set -> Bitmask).
- **Sub-cube Decomposition**: For disjoint character sets (e.g., `[aeiou]`), the engine MUST employ Sub-cube Decomposition (pairing characters with Hamming distance 1) to enable parallel 8-byte matching with minimal XOR+OR chains.
- **Register Pressure Management**: Hot loops MUST favor slim implementation (e.g., `for` loops) over manual unrolling if unrolling causes register spilling to the stack. Performance MUST be verified by inspecting compiler-generated assembly or micro-benchmarks.
- **Cache-line Optimization**: Core transition data structures (e.g., `CCWarpInfo`) MUST be kept small (ideally **32 bytes**) to maximize L1/L2 cache hit rates. Non-critical or large data MUST be offloaded to heap-allocated slices.
- **Two-Phase Warping & Inverted Logic**: The engine MUST distinguish between **SearchWarp** (finding match start) and **CCWarp** (continuing match). SearchWarp MUST skip noise (characters NOT in the start set) using **Inverted Kernel Logic**. To achieve 10 GB/s+ throughput, SearchWarp MUST prioritize SIMD-accelerated library functions (`bytes.IndexByte`, `bytes.IndexAny`) before falling back to custom SWAR kernels.
- **Correctness via Self-Loop Restriction**: SWAR Warp MUST be strictly restricted to "Pure Self-Loops"—states that lead back to the same ID without updating capture tags—to prevent submatch boundary corruption.
- **Physical Throughput Baseline**: The engine MUST aim for a baseline throughput of 3-5 GB/s for simple repetitions (`a+`, `.*`, `[0-9]+`) and 0.5-1 GB/s for disjoint sets on modern x86/ARM hardware.

### 2.21 MAP Correctness & Safety Mandate
To maintain 100% compatibility, MAP MUST adhere to safety constraints:
- **Nullable Pattern Protection**: If a pattern can match an empty string (`minLength == 0`), Pass 0 (MAP rejection) MUST be **disabled** to prevent missing matches.
- **Complex Anchor Fallback**: Context-dependent anchors (e.g., `\b`, multiline `^`/`$`) MUST be handled by the DFA; MAP MUST be disabled if safe validation is impossible.
- **FindAll Advancement Rule**: The `FindAll` loop MUST skip redundant empty matches at the same position and advance exactly one rune (not one byte) to avoid infinite loops and ensure standard library parity.

#### 2.22 Absolute Coordinate Context Propagation (Mandate)
To ensure 100% accurate anchor verification and submatch extraction regardless of the internal scan's starting point, the engine MUST propagate an **Absolute Coordinate Context** via the `ir.Input` structure.
- **Virtual Slicing (Allocation Exclusion)**: `ir.Input` MUST hold the full, original byte slice (`OriginalB`) to act as a zero-allocation alternative to repeated slice truncations.
- **Relative-Coordinate Hot Loops**: Internal execution loops MUST maintain a **Relative Coordinate System**. Loop variables (`i`), priority (`prio`), and internal capture indices MUST be 0-based relative to the start of the current virtual slice (`in.AbsPos`). This ensures that absolute coordinate addition is excluded from the $O(1)$ hot path.
- **Zero-Ambiguity Contextual Anchors**: `VerifyBegin`, `VerifyEnd`, and `VerifyWord (\b)` MUST use `(in.AbsPos + i)` to index into `in.OriginalB`. This allows accurate boundary assessment even when the virtual slice starts in the middle of a word or line.
- **Pointer-Passing for Input Context**: To avoid the 48-byte struct copy overhead (`runtime.duffcopy`) in hot loops, the `ir.Input` structure MUST be passed by pointer (`*ir.Input`) to all anchor verification and execution helper functions.
- **Exit-Only Absolute Conversion**: Conversion from relative to absolute coordinates (e.g., `regs[i] += in.AbsPos`) MUST be performed **exactly once** at the public API boundary before returning results to the caller.
- **Encapsulation**: This absolute coordinate system is an internal architectural detail. Public APIs MUST continue to provide standard, buffer-relative indices (0-based from the provided slice) to maintain 100% compatibility with Go's `regexp` package.


## 3. Feature Selection Policy

### 3.1 Supported Features
- **Standard Syntax**: Support `syntax.Prog`.
- **Anchors & Boundaries**: Supported via Virtual Byte Insertion.
- **Capturing Groups**: Supported via the DFA-First Hybrid Strategy.

### 3.2 Excluded Features
- **Backreferences & Dynamic Lookaround**: Strictly excluded.
- **POSIX Semantics**: Unsupported to maintain $O(n)$.

## 4. Engineering & Validation Standards
The project employs a rigorous validation hierarchy to ensure that performance optimizations never compromise the $O(n)$ guarantee or functional correctness. For detailed methodology, refer to **`docs/compatibility-policy.adoc`**.

- **Two-Stage Submatch Evaluation**: Test validation MUST distinguish between engine search correctness and submatch extraction precision:
    - **Overall Match Mismatch (Tier 1)**: (Indices 0, 1) If the engine fails to identify the correct match boundaries [start, end], it MUST be treated as a **FAIL**.
    - **Submatch Boundary Mismatch (Tier 2)**: (Indices 2+) If the match boundaries are correct but internal group boundaries deviate from standard `regexp`, it MAY be treated as a **SKIP** (Known Limitation) to document Multi-Pass Sparse TDFA boundary ambiguity.
- **Error Compatibility Standard (Tier 3)**: When evaluating compilation success:
    - **Consistent Rejection**: If both the engine and Go standard `regexp` fail to parse a pattern (returning a `syntax.Error`), it MUST be treated as a **PASS** (Compatible).
    - **Engine Limitation**: If the engine rejects a pattern that is valid in standard `regexp` (returning an `UnsupportedError`), it MUST be treated as a **SKIP** (Acknowledged Limitation).
    - **Unexpected Error**: Any other compilation failure MUST be treated as a **FAIL**.
- **Memory Accumulation Prevention**: Dispose of compiled `Regexp` objects promptly during mass testing.
- **100% DFA Validation**: DFA match boundaries MUST strictly match the standard library's boundaries except where documented (e.g., Dot behavior).

### 4.1 Throughput-Oriented Benchmarking
To minimize environmental noise and provide a flat evaluation of engine performance, the project employs a **Throughput-Oriented Benchmarking** strategy.
- **Physical Metrics**: Benchmarks MUST use `b.SetBytes(int64(len(input)))` to report throughput in `MB/s` or `GB/s`.
- **Steady-State Measurement**: Inputs for benchmarks SHOULD be scaled to at least **1MB** (using `scaleInput` or large corpora) to bury function call overhead and OS noise.
- **Feature-Categorized Scan**: Benchmarks derived from external corpora (like `re2-search.txt`) MUST be categorized by feature (Literal, CharClass, Alternation, Anchored, Complex) to ensure a balanced evaluation across the engine's entire capability matrix.
- **Noise-Interleaved Scaling**: To prevent unrealistic branch prediction saturation and cache-hit bias, scaled payloads MUST interleave target test cases with a representative noise block (typically ~1KB).
- **Full-Scan Mandate**: To measure the engine's "cruising speed," benchmarks SHOULD use anchored patterns (e.g., `^...$`) or place matches at the end of the input to force a full scan of the payload.
- **Layered Evaluation**: Utilize `BenchmarkSynthetic` to isolate and evaluate specific optimization layers.
- **Performance Landscape Auditing**: To understand the structural response of the engine, use the 3D Landscape Model (S, B, L). Performance must be evaluated as a function of **Selectivity**, **Branching Complexity**, and **Locality** to ensure optimizations are effective across the entire pattern space.
- `SearchWarp`: Match start position searching (Pre-filter).
    - `CCWarp`: Character class scanning (SWAR).
    - `PureDFA`: Table-based transition logic (NFA-free).
    - `SIMDWarp`: Prefix skipping (`bytes.Index`).

### 4.2 Benchmark Persistence & History
To enable long-term performance auditing, the project maintains a historical record of benchmark results.
- **Latest Baseline**: The most recent results from the `main` branch MUST be stored at `benchmarks/baseline/benchmark-main.txt` on the `gh-pages` branch for CI comparison.
- **Historical Archive**: Every merge to `main` MUST archive its full benchmark results in `benchmarks/history/benchmark-YYYYMMDD-HHMMSS-SHA.txt` on the `gh-pages` branch.
- **Relative Auditing**: Performance regressions MUST be evaluated relative to the `Latest Baseline` within the same CI runner environment to cancel out hardware noise.

### 4.3 Continuous Integration & Quality Audit
The project maintains a multi-layered automated verification suite to ensure that performance optimizations do not compromise correctness or Go compatibility.
- **Unit Test Mandate**: All packages MUST pass `go test ./...` on every PR. This ensures the structural and behavioral integrity of individual components.
- **Compatibility Audit**: Every change MUST be evaluated against the standard library using the `Compatibility Audit`.
    - **Zero Unexpected Regression**: Any "Unexpected Incompatibility" (mismatched match results or unexpected errors) is treated as a critical regression and MUST be fixed.
    - **Visibility**: Compatibility rates (Passed %) MUST be reported in the CI summary to provide transparency on the engine's maturity.
- **Hierarchy of Verification**: Correctness (Unit Tests) > Parity (Compatibility Audit) > Efficiency (Benchmarks). A faster engine that fails correctness or loses parity is considered a failure.

### 4.4 Performance Landscape Visualization
To prove the engine's superiority across diverse workloads, the project maintains a multi-dimensional visualization dashboard on GitHub Pages.
- **Multi-Dimensional Sweep (S-B-L)**: The engine MUST be evaluated across three axes:
    - **Selectivity (S)**: 0.01 (Sparse) to 0.99 (Dense). Identifies MAP/SIMD pre-filter efficiency.
    - **Complexity (B)**: 1 (Simple) to 100 (Complex). Identifies DFA state explosion resistance.
    - **Locality (L)**: 0.1 (Random) to 0.9 (Continuous). Identifies CCWarp (SWAR) acceleration zones.
- **Generator-Viewer Decoupling**:
    - **Generator Mandate (Main Branch)**: This branch is responsible for data generation and processing. CI workflows MUST execute the landscape benchmarks and convert raw text results into rendering-ready JSON using tools in `_scripts/`.
    - **Viewer Mandate (gh-pages Branch)**: The `gh-pages` branch is a pure data consumer. It MUST NOT contain Go source code or processing logic. It exists solely to host static visualization assets and data artifacts.

## 5. Coding Conventions
- **Explicit Aliasing**:
  - `regexp` -> `goregexp`
  - `regexp/syntax` -> `gosyntax`

## 6. Project Structure & Modularity Mandates
To prevent file bloat and maintain long-term maintainability, the codebase MUST adhere to the following file organization. Any new high-level logic should be placed in a specialized file rather than `regexp.go` or `dfa.go`.

### 6.1 Core Runtime (Root Package)
- **`regexp.go`**: Public API entry points (`Compile`, `MustCompile`, `FindSubmatchIndex`) and the core `Regexp` structure definition.
- **`exec_dispatch.go`**: Execution strategy binding and high-level match/submatch dispatching logic.
- **`exec_dfa.go`**: Manually monomorphized hot-loops for DFA execution (`fastMatchExecLoop`, `extendedMatchExecLoop`).
- **`exec_tdfa.go`**: Multi-Pass Sparse TDFA logic, including backward path selection (Pass 3) and forward tag licking (Pass 4).
- **`context.go`**: `matchContext` management, history recording (RLE), and memory pooling.
- **`compat_*.go`**: Standard library compatibility layers, grouped by functionality (match, find, replace, misc).

### 6.2 IR & Compiler (`internal/ir/`)
- **`dfa.go`**: core `DFA` structure, StateID layout constants, and basic getter methods.
- **`dfa_builder.go`**: The DFA compiler, including subset construction, epsilon closure with anchor verification, and SWAR kernel detection.
- **`validate.go`**: Pattern compatibility analysis and structural rejection logic (Epsilon loops, ambiguous captures).
- **`anchor.go`**: Multi-Point Anchor extraction, constraint folding, and Pass 0 discovery kernels.
- **`recap.go`**: Definitions for tag-carrying transition tables (`GroupRecapTable`, `RecapEntry`).
- **`storage.go`**: Abstractions and implementations for NFA path set storage (`NFAPathStorage`).
- **`literal.go`**: The `LiteralMatcher` for 0-pass bypass.
- **`utf8.go`**: UTF-8 to Byte-level transition Trie construction.

### 6.3 Syntax & Optimization (`syntax/`)
- **`syntax.go`**: Fundamental types and standard library-compliant `Parse`/`Compile` wrappers.
- **`optimize.go`**: Project-specific AST optimizations (Simplify, Optimize, Factoring).

### 6.4 Test Organization
To maintain a high-signal testing environment, tests MUST be categorized by the architectural layer they verify:
- **`api_test.go`**: High-level integration tests covering the public API surface.
- **`exec_dfa_test.go`**: Correctness of the DFA execution engine, SWAR kernels, and SIMD warp logic.
- **`exec_tdfa_test.go`**: Submatch extraction precision, Pass 3 path selection, and Pass 4 tag updates.
- **`utf8_test.go`**: Specialized UTF-8 handling, multi-byte boundaries, and invalid sequence resilience.
- **`compat_test.go`**: Systematic parity verification against Go's standard `regexp` package.
- **`internal/ir/dfa_builder_test.go`**: Validation of subset construction, epsilon closure logic, and memory-limit protection.

---
**Note**: Any modification to the compilation shortcut or rescan dispatch must be validated against the **"Efficiency First, Precision Mandatory"** principle.
