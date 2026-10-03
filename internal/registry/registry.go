// Package registry は hhx が持つ hook の一覧である。
// install が書くエントリと `hhx hook <name>` の振り分けは、どちらもこの一覧から作る。
package registry

import (
	"github.com/HappyOnigiri/hhx/internal/hookrt"
	"github.com/HappyOnigiri/hhx/internal/hooks/agentslocalcontext"
	"github.com/HappyOnigiri/hhx/internal/hooks/dangerousrmguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/discardguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/exitplansubagentguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/forbiddentermguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/generatededitguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/githookspathguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/idlewaitguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/irreversibleguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/prbodystaleness"
	"github.com/HappyOnigiri/hhx/internal/hooks/prcontext"
	"github.com/HappyOnigiri/hhx/internal/hooks/prmergeguard"
	"github.com/HappyOnigiri/hhx/internal/hooks/pushcicontext"
)

// definitions は hook を設定ファイルへ書く順に並べる。
// 同じ CLI・イベント・matcher のエントリは 1 つのグループにまとまり、グループ内の順序もこの順になる。
// 移行元の Python 実装を登録していた順に合わせ、移植した hook はその位置へ差し込む。
// agents-local-context は、移植元の Codex の PreToolUse で Bash のグループより前（matcher なし）に登録していた。
// generated-edit-guard は移植元の無い新しい hook なので、既存のエントリの位置を動かさないよう末尾に置く。
var definitions = []hookrt.Definition{
	agentslocalcontext.Definition(),
	prmergeguard.Definition(),
	discardguard.Definition(),
	githookspathguard.Definition(),
	irreversibleguard.Definition(),
	dangerousrmguard.Definition(),
	forbiddentermguard.Definition(),
	idlewaitguard.Definition(),
	exitplansubagentguard.Definition(),
	pushcicontext.Definition(),
	prbodystaleness.Definition(),
	prcontext.Definition(),
	generatededitguard.Definition(),
}

// All は登録済みの hook をすべて返す。
func All() []hookrt.Definition {
	return definitions
}

// Lookup は name の hook を返す。未知の名前なら nil を返す。
func Lookup(name string) *hookrt.Definition {
	for index := range definitions {
		if definitions[index].Name == name {
			return &definitions[index]
		}
	}
	return nil
}

// Known は name が登録済みの hook かを返す。
func Known(name string) bool {
	return Lookup(name) != nil
}
