package prbodyroundtrip

import "github.com/HappyOnigiri/hhx/internal/i18n"

const (
	idMismatch  = Name + ".mismatch"
	idShortened = Name + ".shortened"
)

var messages = i18n.Register(i18n.Catalog{
	// 送信前後の差を見せ、意図した本文を送り直して確認するよう促す。
	idMismatch: {
		EN: "The PR body on GitHub differs from the file passed to --body-file. " +
			"Check the changes below, then resend the intended body and verify it on GitHub.\n\n{{.Diff}}",
		JA: "GitHub 上の PR 本文が `--body-file` で渡したファイルと異なります。\n" +
			"以下の差分を確認し、意図した本文を送り直して GitHub 上の内容を確かめてください。\n\n{{.Diff}}",
	},
	// 差分の行や長い行の一部を省いたことを伝える。
	idShortened: {
		EN: "\nPart of the diff was omitted.\n",
		JA: "\n差分の一部を省略しました。\n",
	},
})
