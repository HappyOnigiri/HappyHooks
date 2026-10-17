package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/pflag"

	"github.com/HappyOnigiri/hhx/internal/i18n"
	"github.com/HappyOnigiri/hhx/internal/pycompat"
	"github.com/HappyOnigiri/hhx/internal/waitci"
)

// waitCIAdapters は wait-ci が触る外部（PATH・プロセスの実行・時計・監視の本体）の差し替え点である。
type waitCIAdapters struct {
	lookPath func(string) (string, error)
	runner   waitci.Runner
	clock    waitci.Clock
	// watch は監視を実行する。テストは結果を差し替え、渡された設定を調べる。
	watch func(*waitci.Waiter) waitci.Outcome
	// language は結論と進捗の表示言語を返す。
	language func() i18n.Language
}

func defaultWaitCIAdapters() waitCIAdapters {
	return waitCIAdapters{
		lookPath: exec.LookPath,
		runner:   waitci.ExecRunner{},
		clock:    waitci.NewClock(),
		watch:    (*waitci.Waiter).Run,
		language: displayLanguage,
	}
}

// waitCICommand は runWaitCI が使う実装である。テストだけが差し替える。
var waitCICommand = defaultWaitCIAdapters()

type waitCIOptions struct {
	reference       string
	sha             string
	anySHA          bool
	interval        int
	timeout         int
	startTimeout    int
	settle          int
	noCITimeout     int
	prLookupTimeout int
	progress        bool
	allChecks       bool
}

// pythonInt は Python の int(str) と同じ書式（前後の空白、符号、数字の間の _）を受け付ける flag の値である。
// pflag の既定の整数は 0x などの接頭辞も受け付けるので使わない。
type pythonInt struct{ value *int }

func (p pythonInt) String() string { return strconv.Itoa(*p.value) }

func (pythonInt) Type() string { return "int" }

func (p pythonInt) Set(text string) error {
	trimmed := pycompat.Strip(text)
	digits := strings.TrimLeft(trimmed, "+-")
	if len(trimmed)-len(digits) > 1 || digits == "" || strings.HasPrefix(digits, "_") ||
		strings.HasSuffix(digits, "_") || strings.Contains(digits, "__") {
		return fmt.Errorf("invalid int value: %s", pythonReprArg(text))
	}
	number, err := strconv.Atoi(trimmed[:len(trimmed)-len(digits)] + strings.ReplaceAll(digits, "_", ""))
	if err != nil {
		return fmt.Errorf("invalid int value: %s", pythonReprArg(text))
	}
	*p.value = number
	return nil
}

func pythonReprArg(text string) string { return "'" + text + "'" }

func newWaitCIFlags(language i18n.Language, options *waitCIOptions) *pflag.FlagSet {
	text := func(id string) string { return messages.T(language, id) }
	flags := pflag.NewFlagSet("wait-ci", pflag.ContinueOnError)
	// 使い方は自分で出す。pflag の既定はエラーのたびに stderr へ一覧を出し、--help でも stderr に出す。
	flags.Usage = func() {}
	flags.SetOutput(io.Discard)
	flags.SortFlags = false
	flags.StringVar(&options.sha, "sha", "", text(idFlagSHA))
	flags.BoolVar(&options.anySHA, "any-sha", false, text(idFlagAnySHA))
	intFlag := func(target *int, name string, value int, usage string) {
		*target = value
		flags.Var(pythonInt{target}, name, usage)
	}
	intFlag(&options.interval, "interval", 20, text(idFlagInterval))
	intFlag(&options.timeout, "timeout", 1800, text(idFlagTimeout))
	intFlag(&options.startTimeout, "start-timeout", 300, text(idFlagStart))
	intFlag(&options.settle, "settle", 30, text(idFlagSettle))
	intFlag(&options.noCITimeout, "no-ci-timeout", 20, text(idFlagNoCI))
	intFlag(&options.prLookupTimeout, "pr-lookup-timeout", 60, text(idFlagPRLookup))
	flags.BoolVarP(&options.progress, "progress", "v", false, text(idFlagProgress))
	flags.BoolVar(&options.allChecks, "all-checks", false, text(idFlagAllChecks))
	return flags
}

// expandAbbreviations は、Python の argparse と同じく長いオプションの一意な前方一致を正式な名前へ展開する。
// 曖昧な前方一致はエラーにする。`--` より後ろは位置引数なので触らない。
func expandAbbreviations(flags *pflag.FlagSet, args []string) ([]string, error) {
	var names []string
	flags.VisitAll(func(flag *pflag.Flag) { names = append(names, flag.Name) })
	names = append(names, "help")
	expanded := make([]string, 0, len(args))
	for index, arg := range args {
		if arg == "--" {
			return append(expanded, args[index:]...), nil
		}
		if !strings.HasPrefix(arg, "--") || len(arg) == 2 {
			expanded = append(expanded, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg[2:], "=")
		var matches []string
		for _, candidate := range names {
			if candidate == name {
				matches = []string{candidate}
				break
			}
			if strings.HasPrefix(candidate, name) {
				matches = append(matches, candidate)
			}
		}
		switch {
		case len(matches) > 1:
			return nil, fmt.Errorf("ambiguous option: --%s could match --%s", name, strings.Join(matches, ", --"))
		case len(matches) == 1:
			arg = "--" + matches[0]
			if hasValue {
				arg += "=" + value
			}
		}
		expanded = append(expanded, arg)
	}
	return expanded, nil
}

// argparseNegativeNumber は argparse が負の数とみなし、オプションではなく値として読む引数の形である。
var argparseNegativeNumber = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

// checkSHAValue は、`=` を付けない `--sha` の直後がオプションに見えるときにエラーを返す。
// pflag は `-` で始まる次の引数もそのまま値に取るが、argparse は負の数と空白を含むものを除いて値に取らず、
// 引数エラーにする。値の渡し忘れ（`--sha $SHA` の SHA が空）で、存在しない commit を待ち続けないようにする。
func checkSHAValue(args []string) error {
	for index, arg := range args {
		if arg == "--" {
			return nil
		}
		if arg != "--sha" || index+1 >= len(args) {
			continue
		}
		next := args[index+1]
		if len(next) > 1 && strings.HasPrefix(next, "-") && !argparseNegativeNumber.MatchString(next) &&
			!strings.Contains(next, " ") {
			return errors.New("argument --sha: expected one argument")
		}
	}
	return nil
}

// parseWaitCIArgs は引数を読む。done が真なら code で終える（使い方を出した・引数が誤っている）。
func parseWaitCIArgs(
	language i18n.Language, args []string, stdout, stderr io.Writer,
) (options waitCIOptions, code int, done bool) {
	flags := newWaitCIFlags(language, &options)
	usageHeader := messages.T(language, idWaitCIUsage)
	fail := func(err error) (waitCIOptions, int, bool) {
		_, _ = fmt.Fprintf(stderr, "%s%s\nhhx wait-ci: error: %v\n", usageHeader, flags.FlagUsages(), err)
		return options, 2, true
	}
	args, err := expandAbbreviations(flags, args)
	if err != nil {
		return fail(err)
	}
	if err := checkSHAValue(args); err != nil {
		return fail(err)
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			_, _ = fmt.Fprint(stdout, usageHeader+flags.FlagUsages())
			return options, 0, true
		}
		return fail(err)
	}
	if flags.NArg() > 1 {
		return fail(fmt.Errorf("unrecognized arguments: %s", strings.Join(flags.Args()[1:], " ")))
	}
	options.reference = flags.Arg(0)
	return options, 0, false
}

// runWaitCI は PR の CI が全ジョブ完了したときに 1 回だけ結果を返す。
//
// 出力は「1 行目に結論、最終行に `wait-ci: exit=<code> failed=<n> total=<n>`」で固定する。
// 読み手がパイプや background 実行で exit status を失っても、出力の先頭か末尾のどちらかが
// 残れば判定できるようにするためである。接頭辞も読み手との契約なので `wait-ci:` のまま残す。
// 結論・明細ともすべて stdout へ出し、stderr は --progress の進捗だけにする。
// 両方を合流させたときにバッファの差で順序が入れ替わり、stderr の結論が stdout の明細より先に届くのを避ける。
//
// 終了コード: 0 全て成功・PR が無い・repo に CI が無い、1 失敗あり、2 引数エラー、
// 3 timeout、4 gh の失敗、5 コンフリクトで check が起動しない。
// hook と違い終了コードが契約の一部なので、hook の実行時の保護（fail-open）は通さない。
func runWaitCI(args []string, stdout, stderr io.Writer) int {
	adapters := waitCICommand
	language := adapters.language()
	options, code, done := parseWaitCIArgs(language, args, stdout, stderr)
	if done {
		return code
	}
	say := func(line string) { _, _ = fmt.Fprintln(stdout, line) }
	// verdict は結論の行を出す。接頭辞 `wait-ci: ` は契約なので訳さない。
	verdict := func(id string, data map[string]any) { say("wait-ci: " + messages.Text(language, id, data)) }
	finish := func(code, failed, total int) int {
		// パイプや background 実行で exit status が失われても、末尾の 1 行だけで判定できる。
		say(fmt.Sprintf("wait-ci: exit=%d failed=%d total=%d", code, failed, total))
		return code
	}
	if options.interval < 1 {
		verdict(idIntervalTooLow, nil)
		return finish(2, 0, 0)
	}
	for _, tool := range []string{"gh", "git"} {
		if _, err := adapters.lookPath(tool); err != nil {
			verdict(idToolRequired, map[string]any{"Tool": tool})
			return finish(4, 0, 0)
		}
	}

	git := waitci.Git{Runner: adapters.runner}
	gh := waitci.GH{Runner: adapters.runner, Language: language}
	branch := ""
	if options.reference == "" {
		branch = git.Output("symbolic-ref", "-q", "--short", "HEAD")
	}
	// detached worktree から HEAD:main を push した直後も、更新済みの origin/main と HEAD で判定する。
	mainHead := branch == "main"
	if options.reference == "" && branch == "" && git.Output("rev-parse", "--git-dir") != "" {
		sha := git.HeadSHA()
		mainHead = sha != "" && sha == git.Output("rev-parse", "refs/remotes/origin/main")
	}
	if mainHead {
		verdict(idMainNoPR, nil)
		return finish(0, 0, 0)
	}
	detached := waitci.IsDetached(options.reference, git.Output)
	reference := options.reference
	sha := options.sha
	currentSHA := ""
	if detached {
		currentSHA = git.HeadSHA()
	}
	headOrCurrent := func() string {
		if currentSHA != "" {
			return currentSHA
		}
		return git.HeadSHA()
	}
	if sha == "HEAD" {
		sha = headOrCurrent()
	}
	if sha == "" && !options.anySHA && options.reference == "" {
		// 引数を省略した呼び出しは「今いる作業ツリーで push した直後」の用途。
		sha = headOrCurrent()
	}
	if options.anySHA {
		sha = ""
	}

	var progress func(string)
	if options.progress {
		// 接頭辞を分けるのは、合流表示で進捗行と結論行が見た目上ひと続きになるため。
		progress = func(text string) { _, _ = fmt.Fprintf(stderr, "wait-ci: [progress] %s\n", text) }
	}

	// resolveByCommit は commit の SHA から PR を探す。--any-sha でも PR の特定には現在の commit を使い、監視対象 SHA は任せる。
	resolveByCommit := func() (string, error) {
		lookupSHA := sha
		if lookupSHA == "" {
			lookupSHA = headOrCurrent()
		}
		return waitci.ResolvePR(language, lookupSHA, options.prLookupTimeout, options.interval, gh.FindPRBySHA,
			adapters.clock, progress)
	}
	if detached {
		number, code, done := resolveOrReport(resolveByCommit, idSkippedNoPR, idDetachedFailed, nil, verdict, finish)
		if done {
			return code
		}
		reference = number
	}

	watch := func() waitci.Outcome { return startWatch(adapters, gh, options, reference, sha, progress, language) }
	outcome := watch()
	byCommit := detached
	// `git push origin HEAD:<別名>` で push すると、gh は今のブランチの名前で PR を探して見つけられない。
	// 同じ名前で push 済み（origin/<branch> が HEAD と一致）なら PR がまだ無いだけなので、待たずにスキップする。
	if outcome.Status == waitci.StatusNoPR && branch != "" &&
		git.Output("rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch) != headOrCurrent() {
		number, code, done := resolveOrReport(resolveByCommit, idSkippedNoPRBranch, idCommitLookupFailed,
			map[string]any{"Branch": branch}, verdict, finish)
		if done {
			return code
		}
		reference = number
		byCommit = true
		outcome = watch()
	}
	return reportWaitCI(language, outcome, byCommit, options.allChecks, say, finish)
}

// startWatch は reference の PR の CI を監視し、結果を返す。
func startWatch(
	adapters waitCIAdapters, gh waitci.GH, options waitCIOptions, reference, sha string, progress func(string),
	language i18n.Language,
) waitci.Outcome {
	return adapters.watch(&waitci.Waiter{
		Fetch:        func() (waitci.Snapshot, error) { return gh.Fetch(reference) },
		SHA:          sha,
		Interval:     options.interval,
		Timeout:      options.timeout,
		StartTimeout: options.startTimeout,
		Settle:       options.settle,
		NoCITimeout:  options.noCITimeout,
		HasCI:        gh.CIEvidence,
		Clock:        adapters.clock,
		Progress:     progress,
		Language:     language,
	})
}

// resolveOrReport は commit から PR を探す。PR が無いときは skipID、失敗したときは failID の結論を出し、
// done を真にして終了コードを返す。data は結論の差し込みに足す値である。
func resolveOrReport(
	resolve func() (string, error), skipID, failID string, data map[string]any,
	verdict func(string, map[string]any), finish func(code, failed, total int) int,
) (number string, code int, done bool) {
	number, err := resolve()
	if err == nil {
		return number, 0, false
	}
	fields := map[string]any{}
	for key, value := range data {
		fields[key] = value
	}
	var noPR *waitci.NoPullRequestError
	if errors.As(err, &noPR) {
		fields["Reason"] = noPR.Message
		verdict(skipID, fields)
		return "", finish(0, 0, 0), true
	}
	fields["Error"] = waitci.DisplayMessage(err)
	verdict(failID, fields)
	return "", finish(4, 0, 0), true
}

// reportWaitCI は監視の結果を結論・警告・明細の順に出し、終了コードを返す。
func reportWaitCI(
	language i18n.Language, outcome waitci.Outcome, detached, allChecks bool, say func(string),
	finish func(code, failed, total int) int,
) int {
	verdict := func(id string, data map[string]any) { say("wait-ci: " + messages.Text(language, id, data)) }
	// check を 1 件も見ていない経路。結論はこの 1 行で言い切れる。
	switch outcome.Status {
	case waitci.StatusNoPR:
		target := idTargetBranch
		if detached {
			target = idTargetCommit
		}
		verdict(idNoPRFor, map[string]any{"Target": messages.T(language, target)})
		return finish(0, 0, 0)
	case waitci.StatusError:
		verdict(idGHKeptFailing, map[string]any{"Error": outcome.Message})
		return finish(4, 0, 0)
	case waitci.StatusHeadTimeout:
		current := outcome.Head
		if current == "" {
			current = messages.T(language, idUnknownHead)
		}
		verdict(idHeadTimeout, map[string]any{"Elapsed": outcome.Elapsed, "Target": outcome.Message, "Head": current})
		return finish(3, 0, 0)
	case waitci.StatusConflict:
		verdict(idConflictNoRun, nil)
		return finish(5, 0, 0)
	case waitci.StatusNoCI:
		verdict(idNoCI, map[string]any{"Elapsed": outcome.Elapsed})
		return finish(0, 0, 0)
	case waitci.StatusEmptyTimeout:
		verdict(idEmptyTimeout, map[string]any{"Elapsed": outcome.Elapsed})
		return finish(3, 0, 0)
	case waitci.StatusComplete, waitci.StatusTimeout:
	}

	// ここから先は check を見た経路。結論・警告・明細の順に出す。
	checks := outcome.Checks
	failed := len(outcome.Failed())
	code := 0
	switch {
	case outcome.Status == waitci.StatusTimeout:
		var pending []string
		for _, check := range checks {
			if !check.Done {
				pending = append(pending, check.Name)
			}
		}
		verdict(idTimeout, map[string]any{"Elapsed": outcome.Elapsed, "Pending": strings.Join(pending, ", ")})
		code = 3
	case failed > 0:
		verdict(idFailed, map[string]any{"Failed": failed, "Total": len(checks)})
		code = 1
	default:
		verdict(idAllPassed, map[string]any{"Total": len(checks)})
	}
	if outcome.Conflicting {
		// check は動いているので待ちは続けたが、この PR はこのままではマージできない。
		verdict(idConflicting, nil)
	}
	say(fmt.Sprintf("PR head: %s  (%d checks, %ds)", outcome.Head, len(checks), outcome.Elapsed))
	for _, line := range waitci.Summarize(checks, allChecks) {
		say(line)
	}
	return finish(code, failed, len(checks))
}
