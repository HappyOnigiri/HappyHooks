package discardguard

import (
	"maps"
	"os"
	"slices"
	"strings"

	py "github.com/HappyOnigiri/hhx/internal/pycompat"
)

var shells = []string{"sh", "bash", "zsh", "dash", "ksh"}

// unsafeEnvPrefixes は作業ツリー・git ディレクトリを環境変数で差し替える形である。これは追わない。
var unsafeEnvPrefixes = []string{"GIT_WORK_TREE=", "GIT_DIR=", "GIT_INDEX_FILE="}

// unstaticChars を含むパス（変数展開・glob・クォート・シェルのメタ文字）は静的に解決できない。
const unstaticChars = "$`*?~'\"&;|()"

func isStaticPath(path string) bool {
	return !strings.ContainsAny(path, unstaticChars)
}

func isShell(token string) bool {
	if slices.Contains(shells, token) {
		return true
	}
	index := strings.LastIndex(token, "/")
	return index >= 0 && slices.Contains(shells, token[index+1:])
}

func hasUnsafeEnvPrefix(token string) bool {
	for _, prefix := range unsafeEnvPrefixes {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return false
}

// takeValue は tokens[index] から値を 1 つ取り出す。クォートが閉じていなければ、閉じるまで空白でつないで束ねる。
// 束ねないと -C 'my repo' の途中で切れてサブコマンドを取り違え、別のリポジトリを対象と誤認する。
// 戻り値は値と、最後に消費した位置である。閉じないまま尽きたら、そこまでを返す。
func takeValue(tokens []string, index int) (string, int) {
	value := tokens[index]
	for index+1 < len(tokens) && (strings.Count(value, "'")%2 == 1 || strings.Count(value, `"`)%2 == 1) {
		index++
		value += " " + tokens[index]
	}
	return value, index
}

// expandUser は Python の os.path.expanduser を、先頭が ~ か ~/ のパスに対して行う。
func expandUser(path string) string {
	home := py.Home()
	if home == "~" {
		// ホームディレクトリが得られなければ、Python と同じく展開しない。
		return path
	}
	if home == "/" {
		home = ""
	}
	if expanded := home + path[1:]; expanded != "" {
		return expanded
	}
	return "/"
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// resolution は snapshot の対象の候補である。
type resolution struct {
	// dirs は snapshot を取るディレクトリを出現順に持つ。
	dirs []string
	// scratch はセッションの scratchpad の中にある既存のディレクトリである。作業ツリーも scratchpad の中にあれば snapshot を取らない。
	scratch []string
}

func appendNew(list []string, item string) []string {
	if slices.Contains(list, item) {
		return list
	}
	return append(list, item)
}

// resolveTargetDirs は scratchpad を考えずに、snapshot の対象のディレクトリを出現順に返す。静的に特定できなければ nil を返す。
func resolveTargetDirs(command, cwd string) ([]string, error) {
	resolved, err := resolveTargets(command, cwd, nil, scratchpad{})
	if resolved == nil {
		return nil, err
	}
	return resolved.dirs, nil
}

// pathState は cd / git -C の行き先を解決した結果である。
type pathState int

const (
	unresolvedPath pathState = iota
	existingPath
	// missingScratchPath はセッションの scratchpad の中で、hook の実行時点ではまだ無いディレクトリである（同じコマンドで clone する形）。
	missingScratchPath
)

// track は模擬しているシェルの状態である。
type track struct {
	dir string
	// exists は dir が hook の実行時点で存在するかを示す。偽なら dir は scratchpad の中である。
	exists bool
	// pending は、まだ無い scratchpad のディレクトリへ移った cd のトークンの位置である（無ければ -1）。
	// その cd が実行時に失敗すると前のディレクトリに留まるので、そこから破棄までが && だけでつながっていなければ特定できないものとする。
	pending int
	// vars は値が静的に決まる変数である。無い変数は値が分からない（環境変数、条件つきの代入、$(...) の結果など）。
	vars map[string]string
}

func (t track) clone() track {
	t.vars = maps.Clone(t.vars)
	return t
}

// resolveTargets は snapshot の対象を出現順に返す。静的に特定できなければ nil を返す。
// bodies は、シェルへ流し込む here-document の本文の範囲である（ファイルへ書き出す本文は呼び出し側で取り除いてある）。
//
// cd / git -C が別のリポジトリを指しているのに cwd を見ると、無関係なリポジトリを保存して本来の対象を無防備に破棄させる。
// そこで cd を出現順に辿ってシェルの作業ディレクトリを模擬し、破棄系の git ごとに「その時点の作業ディレクトリ（-C があればその先）」を候補にする。
// cd / -C の行き先が存在しなければ特定できないものとする。存在しない cd は実行時に失敗して前のディレクトリに留まるので、
// その後の相対パス（cd ..）を取り違えると本来の対象を保存し損ねる。
// (cd dir && git ...) の括弧や make -C のような無関係な -C を正規表現の境界だけで見分けるのは無理があるので、トークン単位で走査する。
//
// 次の 2 点は移植元の Python 実装から意図して変えている。どちらも移植元では別のリポジトリを保存して通し、本来の対象の変更を失っていた。
//   - ( ... ) と $( ... ) はサブシェルなので、中の cd を閉じ括弧で取り消す。移植元は括弧を空白として読み、閉じた後も cd を残していた。
//     ただし括弧はクォートや case の分岐を見分けずに数えるので、実際には閉じていない括弧で cd を取り消しうる。
//     そこで括弧を無視した作業ディレクトリ（移植元の意味）も並行して辿り、両方を候補にする。移植元より保存先が減る入力は無い。
//   - 1 つの git に -C が複数あれば、git と同じく出現順に適用し、相対パスは直前の -C の先から辿る。移植元は最後の -C だけを cwd から解決していた。
//
// 移植元に無い機能として、同じコマンドの中で値の決まる変数（S=/abs; D=$S/x）を cd / git -C のパスとして展開し、
// セッションの scratchpad の中の対象は snapshot の候補から外す（使い捨ての clone での破棄を止めないため）。
// 変数は、その名前がコマンドの中で 1 回だけ、条件なしの単独の代入として現れる場合だけ信用する（trustedNames）。
func resolveTargets(command, cwd string, bodies []span, scratch scratchpad) (*resolution, error) {
	base := cwd
	if base == "" {
		var err error
		if base, err = py.Getcwd(); err != nil {
			return nil, err
		}
	}
	if !isDir(base) {
		return nil, nil
	}
	r := newResolver(command, base, bodies, scratch)
	for index := range r.tokens {
		if !r.step(index) {
			return nil, nil
		}
	}
	// 破棄系の git をトークンとして拾えなかった（クォートの中など）が、discardRules には当たっている。
	// 多めに拾う方針なので、最後の作業ディレクトリを保存しておく。
	if !r.found {
		for _, t := range []track{r.current, r.flat} {
			if t.pending >= 0 || !r.candidate(t, t.dir, t.exists, true, len(r.tokens)-1) {
				return nil, nil
			}
		}
	}
	return &r.result, nil
}

// resolver は resolveTargets の走査の状態である。
type resolver struct {
	tokens  []token
	words   []string
	bodies  []span
	scratch scratchpad
	result  resolution
	found   bool

	// current は括弧をサブシェルとして辿り、flat は括弧を無視して辿る（閉じ括弧で cd も変数も取り消さない）。
	current, flat track
	// outer はサブシェルに入る前の状態を積む。対応の無い閉じ括弧（case の分岐など）は無視する。
	outer []track

	// trusted は値の変わらない変数の名前である（trustedNames）。
	trusted map[string]bool
	// linked は ln を含むかを示す。同じコマンドで symlink を作れば、scratchpad の中のまだ無いパスが外のリポジトリを指しうる。
	linked bool
	// varsDisabled は eval / source を過ぎたかを示す。これらは任意の変数を書き換えうるので、以降は変数を展開しない。
	varsDisabled bool
	// errexit は set -e が効いているかを示す。効いていれば、単独の cd の失敗でシェルが終わる。
	errexit bool
	// blocks は if / for などの複合コマンドと { ... } の深さである。中の代入と cd は条件つきでしか実行されない。
	blocks int
	// assigning は、直前のトークンがコマンドの位置から続く代入（または export）であることを示す。runStart はその先頭の位置である。
	assigning bool
	runStart  int
}

func newResolver(command, base string, bodies []span, scratch scratchpad) *resolver {
	tokens := tokenize(command)
	words := make([]string, len(tokens))
	for index, token := range tokens {
		words[index] = token.text
	}
	current := track{dir: base, exists: true, pending: -1, vars: map[string]string{}}
	return &resolver{
		tokens:  tokens,
		words:   words,
		bodies:  bodies,
		scratch: scratch,
		current: current,
		flat:    current.clone(),
		trusted: trustedNames(command),
		linked:  slices.ContainsFunc(words, func(word string) bool { return word == "ln" || strings.HasSuffix(word, "/ln") }),
	}
}

// step は tokens[index] を読んで状態を進める。対象を特定できなければ偽を返す。
func (r *resolver) step(index int) bool {
	token := r.tokens[index]
	r.enterOrLeave(token.seps)
	word := token.text
	atCommand := index == 0 || token.seps != "" || slices.Contains(keywordsBeforeCommand, r.words[index-1])
	next := ""
	if index+1 < len(r.words) {
		next = r.words[index+1]
	}

	if hasUnsafeEnvPrefix(word) {
		return false
	}
	if name, value, ok := assignment(word); ok && (atCommand || r.assigning) {
		r.assign(index, name, value)
		return true
	}
	r.assigning = atCommand && word == "export"
	if r.assigning {
		r.runStart = index
		return true
	}

	switch {
	case atCommand && slices.Contains(blockOpeners, word):
		r.blocks++
	case atCommand && slices.Contains(blockClosers, word):
		r.blocks = max(r.blocks-1, 0)
	case word == "eval" || word == "source" || (atCommand && word == "."):
		r.varsDisabled = true
	case atCommand && word == "set" && !r.inBody(index) && r.blocks == 0 && len(r.outer) == 0:
		if on, ok := errexitFlag(r.words, index+1); ok {
			r.errexit = on
		}
	case word == "cd" || word == "pushd" || word == "popd":
		// pushd / popd は行き先を追えない。引数の無い cd（ホームへ移る）と cd - も追わない。
		return word == "cd" && next != "" && !strings.HasPrefix(next, "-") && r.cd(index, atCommand)
	case isShell(word):
		// 別のシェル経由（sh -c '...' / bash -lc '...'）は、中身をトークンとして追えない。
		return !strings.HasPrefix(next, "-")
	case word == "git" || strings.HasSuffix(word, "/git"):
		return r.git(index)
	}
	return true
}

// enterOrLeave は語の前の区切りにある括弧でサブシェルに出入りし、波括弧の深さを数える。
func (r *resolver) enterOrLeave(seps string) {
	for _, separator := range []byte(seps) {
		switch separator {
		case '(':
			r.outer = append(r.outer, r.current.clone())
		case ')':
			if len(r.outer) > 0 {
				r.current = r.outer[len(r.outer)-1]
				r.outer = r.outer[:len(r.outer)-1]
			}
		case '{':
			r.blocks++
		case '}':
			r.blocks = max(r.blocks-1, 0)
		}
	}
}

func (r *resolver) inBody(index int) bool {
	return inSpans(r.bodies, r.tokens[index].offset)
}

// vars は index の語の展開に使う変数を返す。シェルへ流し込む本文の中（外側の変数は見えない）と eval / source の後は nil を返す。
func (r *resolver) vars(t track, index int) map[string]string {
	if r.inBody(index) || r.varsDisabled {
		return nil
	}
	return t.vars
}

// assign は NAME=VALUE の代入を読む。条件なしに単独で走り、名前が 1 回しか現れない代入だけを変数の値にし、それ以外は値を分からなくする。
func (r *resolver) assign(index int, name, value string) {
	if !r.assigning {
		r.runStart = index
	}
	r.assigning = true
	valid := !r.inBody(index) && !r.varsDisabled && r.blocks == 0 && r.trusted[name] &&
		!strings.ContainsAny(r.tokens[r.runStart].seps, "&|") && assignmentStandsAlone(r.tokens, index)
	for _, t := range []*track{&r.current, &r.flat} {
		if valid {
			if expanded, ok := expandWord(value, t.vars, true); ok {
				t.vars[name] = expanded
				continue
			}
		}
		delete(t.vars, name)
	}
}

// cd は tokens[index] の cd で作業ディレクトリを移す。行き先を特定できなければ偽を返す。
func (r *resolver) cd(index int, atCommand bool) bool {
	path, last := takeValue(r.words, index+1)
	// set -e の下で条件なしに単独で走る cd は、失敗すればシェルごと終わる。
	standalone := r.errexit && atCommand && r.blocks == 0 && !strings.ContainsAny(r.tokens[index].seps, "&|") &&
		(last+1 >= len(r.tokens) || r.tokens[last+1].seps != "" && !strings.ContainsAny(r.tokens[last+1].seps, "&|"))
	for _, t := range []*track{&r.current, &r.flat} {
		destination, state, absolute := r.resolve(t.dir, r.vars(*t, index), path)
		if state == unresolvedPath {
			return false
		}
		t.dir, t.exists = destination, state == existingPath
		switch {
		case t.exists:
			t.pending = -1
		case absolute || t.pending < 0:
			// 絶対パスの cd は前の作業ディレクトリに依らないので、この cd から判定し直す。
			// 相対パスなら、前の cd が失敗していれば行き先も変わるので、最初の cd から判定する。
			t.pending = index
			if standalone {
				t.pending = -1
			}
		}
	}
	return true
}

// git は tokens[index] の git が破棄系なら、走るディレクトリを候補に足す。特定できなければ偽を返す。
func (r *resolver) git(index int) bool {
	directories, subcommand, ok := parseGitOptions(r.words, index+1)
	if !ok {
		return false
	}
	// -C は破棄系のサブコマンドに付いていた場合だけ採用する（git -C <other> fetch && git reset --hard に乗っ取られないため）。
	if !slices.Contains(discardSubcommands, subcommand) {
		return true
	}
	r.found = true
	for _, t := range []track{r.current, r.flat} {
		vars := r.vars(t, index)
		target, exists, dependsOnBase := t.dir, t.exists, true
		for _, directory := range directories {
			resolved, state, absolute := r.resolve(target, vars, directory)
			if state == unresolvedPath {
				return false
			}
			// 絶対パスの -C は、それより前の作業ディレクトリ（失敗したかもしれない cd の結果）に依らない。
			dependsOnBase = dependsOnBase && !absolute
			target, exists = resolved, state == existingPath
		}
		if !r.candidate(t, target, exists, dependsOnBase, index) {
			return false
		}
	}
	return true
}

// resolve は from を基準に word を解決する。先頭の ~ / ~/ だけは展開する（hook の HOME はシェルと同じなので決まる）。
// クォートつきの "~/x" はシェルも展開しない。~user の形は追わない。3 つ目の戻り値は word が絶対パスだったかを示す。
func (r *resolver) resolve(from string, vars map[string]string, word string) (string, pathState, bool) {
	// 語が空白を含むのは、クォートの中の空白や区切り文字で分けた断片を takeValue が 1 つの空白でつないだときだけである。
	// つないだ値は元のパスと違いうる（'a  b'、"a;b"、全角空白）ので追わない。
	if strings.ContainsFunc(word, py.IsSpace) {
		return "", unresolvedPath, false
	}
	path, ok := expandPath(word, vars)
	if !ok {
		return "", unresolvedPath, false
	}
	absolute := strings.HasPrefix(path, "/")
	full := path
	if !absolute {
		full = py.Join(from, path)
	}
	if isDir(full) {
		return full, existingPath, absolute
	}
	if !r.linked && r.scratch.contains(full) {
		return full, missingScratchPath, absolute
	}
	return "", unresolvedPath, absolute
}

// chainSafe は、from の cd から to の git までが && だけでつながっているかを返す（cd が失敗すれば git まで届かない）。
func (r *resolver) chainSafe(from, to int) bool {
	if strings.ContainsRune(r.tokens[from].seps, '|') {
		return false
	}
	for index := from + 1; index <= to; index++ {
		if !onlyAndThen(r.tokens[index].seps) {
			return false
		}
	}
	return true
}

// candidate は、破棄が t の状態で target に対して走るときの候補を足す。特定できなければ偽を返す。
func (r *resolver) candidate(t track, target string, exists, dependsOnBase bool, at int) bool {
	if dependsOnBase && t.pending >= 0 && !r.chainSafe(t.pending, at) {
		return false
	}
	switch {
	case !exists:
		// scratchpad の中のまだ無いディレクトリ。git -C は失敗し、cd は失敗すれば破棄まで届かない。
	case r.scratch.contains(target):
		r.result.scratch = appendNew(r.result.scratch, target)
	default:
		r.result.dirs = appendNew(r.result.dirs, target)
	}
	return true
}

// parseGitOptions は tokens[start:] を git の大域オプションとして読み、-C の値を出現順に並べたものとサブコマンドを返す。
// サブコマンドが無ければ空文字列を返す。--git-dir / --work-tree と、値の無い -C は特定できないものとして ok を偽にする。
func parseGitOptions(tokens []string, start int) (directories []string, subcommand string, ok bool) {
	count := len(tokens)
	index := start
	for ; index < count; index++ {
		option := tokens[index]
		switch {
		case option == "-C":
			if index+1 >= count {
				return nil, "", false
			}
			var directory string
			directory, index = takeValue(tokens, index+1)
			directories = append(directories, directory)
		case strings.HasPrefix(option, "-C"):
			var value string
			value, index = takeValue(tokens, index)
			directories = append(directories, value[2:])
		case strings.HasPrefix(option, "--git-dir") || strings.HasPrefix(option, "--work-tree"):
			// 作業ツリーが cwd から外れるが、対象の特定までは踏み込まない。
			return nil, "", false
		case option == "-c":
			if index+1 < count {
				// -c k=v の値を読み飛ばす。
				_, index = takeValue(tokens, index+1)
			}
		case strings.HasPrefix(option, "-"):
		default:
			return directories, option, true
		}
	}
	return directories, "", true
}
