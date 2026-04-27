# TODO: 多点アンカー型制約伝搬 (MAP) 実装

## Phase 1: 現状分析とベンチマーク基盤 (Baseline)
- [ ] MAPの恩恵を受けやすいテストパターンを選定 (Pivot, Suffix, UTF-8)
- [ ] 専用ベンチマーク `bench_map_test.go` の作成
- [ ] 現行エンジンと標準 `regexp` による性能ベースラインの測定
- [ ] DFA起動回数を計測するためのデバッグ・カウンタの導入

## Phase 2: AST解析とアンカー抽出層
- [ ] `syntax/optimize.go` の拡張: Pivot (中間), Suffix (末尾) リテラルの抽出
- [ ] アンカー評価関数 (Scoring) の実装: 最も効率的な起点を選択
- [ ] 制約（前後 CharClass）の畳み込みロジックの実装

## Phase 3: SWARガード・カーネルの実装
- [ ] 逆方向バリデーション用 SWAR カーネルの実装
- [ ] 前方スキップ用 SWAR ワープの統合
- [ ] バリデータの単体テストと境界条件（UTF-8等）の検証

## Phase 4: 実行エンジンへの統合 (Pass 0)
- [ ] `LiteralMatcher` の拡張: 双方向ガード付き検索の実装
- [ ] `exec_dispatch.go` への Pass 0 組み込み
- [ ] Pass 0 から Pass 1 (DFA) へのコンテキスト引き継ぎの最適化

## Phase 5: 最終検証と性能評価
- [ ] 互換性テスト (Fowler, RE2) によるデグレード確認
- [ ] ベースラインとの性能比較（スループット、DFA起動数）
- [ ] ドキュメント (docs/algorithm) の最終更新
