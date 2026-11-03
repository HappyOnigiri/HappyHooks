package irreversibleguard

import (
	"path"
	"regexp"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

var (
	iamResourceRE    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	iamPermissionsRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z0-9]+)+` +
		`(?:,[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z0-9]+)+)*$`)
)

// staticIAMPermissionCheck は、引数をすべて確認できる単独の権限確認だけを識別する。
// 移植元との違い: 権限名の container.jobs.delete は削除のサブコマンドではない。
// 展開・制御構文・未知のオプションがあれば従来の判定を残し、別の削除を巻き添えで通さない。
func staticIAMPermissionCheck(command string) bool {
	command = strings.ReplaceAll(command, "\\\n", " ")
	if strings.ContainsAny(command, "$`\\") {
		return false
	}
	tokens, err := py.ShlexSplit(command, " \t\r", ";&|()<>\n")
	if err != nil || len(tokens) < 5 || path.Base(tokens[0]) != "gcloud" {
		return false
	}
	resource := 3
	switch {
	case tokens[1] == "projects" && tokens[2] == "test-iam-permissions":
		// 履歴の形。現行 CLI では無効なサブコマンドだが、削除として拒否する必要はない。
	case len(tokens) >= 6 && tokens[1] == "compute" && tokens[2] == "instances" && tokens[3] == "test-iam-permissions":
		resource = 4
	default:
		return false
	}
	if !iamResourceRE.MatchString(tokens[resource]) {
		return false
	}
	permissions := false
	for index := resource + 1; index < len(tokens); index++ {
		token := tokens[index]
		if token == "--permissions" {
			index++
			if index >= len(tokens) {
				return false
			}
			token = "--permissions=" + tokens[index]
		}
		if value, ok := strings.CutPrefix(token, "--permissions="); ok {
			if permissions || !iamPermissionsRE.MatchString(value) {
				return false
			}
			permissions = true
			continue
		}
		if token != "--format=json" && token != "--format=value(permissions)" {
			return false
		}
	}
	return permissions
}
