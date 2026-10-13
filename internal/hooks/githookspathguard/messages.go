package githookspathguard

import "github.com/HappyOnigiri/hhx/internal/i18n"

const idReason = "git-hookspath-guard.reason"

var messages = i18n.Register(i18n.Catalog{
	// 書き換えを止めるだけでなく、正しい手段（git config）と置き場所（.git/hooks/）を示す。
	// 回避を思いとどまらせるのはこの文面だけなので、同じ変更を形を変えて行う迂回を禁じ、ユーザーへ伝えることも書く。
	idReason: {
		EN: "Do not edit Git config files directly; change Git settings with `git config <key> <value>`. " +
			"Changing core.hooksPath, however, is forbidden: it breaks the global-to-local hook delegation chain. " +
			"Put repository-specific hooks directly in .git/hooks/. " +
			"Do not use another tool, or a script file you write and run, to change core.hooksPath " +
			"or edit the config file directly to get around this block. " +
			"If you still need the change, tell the user what you want to change and why, and ask for instructions.",
		JA: "Git 設定ファイルを直接編集せず、Git 設定は `git config <key> <value>` で" +
			"書き換えてください。ただし core.hooksPath の変更は禁止されています。" +
			"global→local hook 委譲チェーンが壊れます。リポジトリ固有の hook は .git/hooks/ に" +
			"直接配置してください。" +
			"core.hooksPath の変更や設定ファイルの直接編集を、別ツール・スクリプトファイルに書いての実行など形を変えて行い、" +
			"このブロックを避けることはしないでください。" +
			"それでも変更が必要なら、変更したい内容と目的をユーザーに伝えて指示を仰いでください。",
	},
})
