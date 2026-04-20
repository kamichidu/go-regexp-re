# TDFA Pipeline Stabilization TODO

## 1. Naked Indexing Isolation (Pass 1) [DONE]
- [x] `internal/ir/dfa.go`: ビットレイアウトを固定し、外部への公開定数を定義する。
  - レイアウト: `[31: Tagged] [30: Anchor] [29: Warp] [28-22: AnchorMask] [21-0: StateIndex]`
  - `StateIDMask = 0x003FFFFF`
- [x] `regexp.go`: `submatchExecLoop` において、`mc.history[i]` に書き込む直前に必ず `state & ir.StateIDMask` を実行し、フラグを物理的に除去する。
- [x] **Validation (White-box)**: `repro_test.go` において `TestPass1HistoryPurity` で不純物がないことを実証。

## 2. Streamlined Reconstruction (Pass 2 & 3) [DONE]
- [x] `regexp.go`: Pass 2 (`sparseTDFA_PathSelection`) および Pass 3 (`sparseTDFA_Recap`) から、履歴取り出し時のマスク処理をすべて削除。
- [x] **Constraints**: Pass 1 の隔離結果を直接利用し、パニックが発生しないことを確認。

## 3. Precision Refinement [IN PROGRESS]
- [x] `internal/ir/dfa.go`: 状態ではなく「遷移（RecapEntry）」にタグ情報を焼くように修正（Mandate 2.5 準拠）。
- [ ] `a(b)c` およびマルチバイトパターンのオフセットずれ（1バイト前後の誤差）を解消する。
- [ ] `pass3_test.go` の全ケース合格を確認する。

## 4. Logic Integrity (Next Step)
- [ ] **Greedy Loop Correction**: `IsBestMatch` の早期終了による、繰り返し（`*`, `+`）の空マッチ問題を修正する。
- [ ] **Pass 2 Priority Synchronization**: `SearchRestartPenalty` を考慮した優先度チェーンの完全な復元。

## 5. Final Validation
- [ ] `go test ./...` の完全グリーン化。
- [ ] `benchmark-full.sh` によるスループット検証（Go標準比 5x以上）。
