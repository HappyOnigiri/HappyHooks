# git-hookspath-guard

[English](git-hookspath-guard.md) | 日本語 · [hook 一覧](../../README.ja.md#hook-一覧)

**既定:** Claude Code と Codex で有効。

グローバルな Git hook からリポジトリの hook へ委ねる構成向けです。`core.hooksPath` の変更と、`.git/config` などの Git 設定ファイルの直接編集を止めます。設定の読み取りは通します。

`~/.config/hhx/config.yaml` で無効にできます。

```yaml
hooks:
  git-hookspath-guard:
    enabled: false
```
