# TDFA Pipeline Stabilization TODO

## 1. Naked Indexing Isolation (Pass 1)
- [ ] `internal/ir/dfa.go`: ビットレイアウトを固定し、外部への公開定数を定義する。
  - レイアウト: `[31: Tagged] [30: Anchor] [29: Warp] [28-22: AnchorMask] [21-0: StateIndex]`
  - `StateIDMask = 0x003FFFFF`
- [ ] `regexp.go`: `submatchExecLoop` において、`mc.history[i]` に書き込む直前に必ず `state & ir.StateIDMask` を実行し、フラグを物理的に除去する。
- [ ] **Validation (White-box)**: `repro_test.go` 等で、走査完了後の `mc.history` の全要素が `ir.StateIDMask` の範囲内に収まっている（不純物がない）ことをアサーションで確認する。

## 2. Streamlined Reconstruction (Pass 2 & 3)
- [ ] `regexp.go`: Pass 2 (`sparseTDFA_PathSelection`) および Pass 3 (`sparseTDFA_Recap`) から、履歴取り出し時のマスク処理をすべて削除する。
- [ ] **Constraints**: Pass 1 での隔離を信頼し、二重のチェックロジックを入れない。

## 3. Precision Refinement
- [ ] `internal/ir/dfa.go`: `a(b)c` 等のサブマッチ境界を正しく捉えるため、エポキシ closure のタグ情報を `TransitionUpdate.PreUpdates` に同期させる。
- [ ] `pass3_test.go` の全ケース合格を確認する。

## 4. Final Validation
- [ ] `go test ./...`
- [ ] `benchmark-full.sh` によるスループット検証。
