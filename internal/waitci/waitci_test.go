package waitci

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HappyOnigiri/hhx/internal/i18n"
)

// fakeClock は sleep した分だけ進む単調時計である。poll の並びを実時間なしで再現する。
type fakeClock struct{ value time.Duration }

func (c *fakeClock) Now() time.Duration { return c.value }

func (c *fakeClock) Sleep(d time.Duration) { c.value += d }

func checkRun(name, status, conclusion string, extra ...any) map[string]any {
	node := map[string]any{
		"__typename": "CheckRun",
		"name":       name,
		"status":     status,
		"conclusion": conclusion,
		"detailsUrl": "https://example.test/" + name,
	}
	for index := 0; index+1 < len(extra); index += 2 {
		node[extra[index].(string)] = extra[index+1]
	}
	return node
}

func rollup(nodes ...map[string]any) []any {
	items := make([]any, len(nodes))
	for index, node := range nodes {
		items[index] = node
	}
	return items
}

// poll は偽の fetch が 1 回に返す応答である。err があればそれを返す。mergeable が空なら MERGEABLE。
type poll struct {
	head      string
	rollup    []any
	mergeable string
	err       error
}

func repeat(item poll, count int) []poll {
	items := make([]poll, count)
	for index := range items {
		items[index] = item
	}
	return items
}

func concat(groups ...[]poll) []poll {
	var items []poll
	for _, group := range groups {
		items = append(items, group...)
	}
	return items
}

// makeWaiter は responses を 1 poll ずつ返す Waiter を作る。最後の要素は以降ずっと返る。
func makeWaiter(responses []poll, configure ...func(*Waiter)) (*Waiter, *fakeClock) {
	clock := &fakeClock{}
	index := 0
	waiter := &Waiter{
		Fetch: func() (Snapshot, error) {
			response := responses[min(index, len(responses)-1)]
			index++
			if response.err != nil {
				return Snapshot{}, response.err
			}
			mergeable := response.mergeable
			if mergeable == "" {
				mergeable = "MERGEABLE"
			}
			return Snapshot{Head: response.head, Mergeable: mergeable, Checks: Normalize(response.rollup)}, nil
		},
		Interval: 10, Timeout: 600, StartTimeout: 100, Settle: 20,
		Clock: clock, Language: testLanguage,
	}
	for _, apply := range configure {
		apply(waiter)
	}
	return waiter, clock
}

func names(checks []Check) []string {
	result := []string{}
	for _, check := range checks {
		result = append(result, check.Name)
	}
	return result
}

// --- Normalize ---

func TestCheckRunUsesConclusionAfterCompletion(t *testing.T) {
	checks := Normalize(rollup(
		checkRun("build", "COMPLETED", "SUCCESS", "workflowName", "CI"),
		checkRun("race", "IN_PROGRESS", ""),
	))
	if got := names(checks); !reflect.DeepEqual(got, []string{"CI / build", "race"}) {
		t.Fatalf("names=%v", got)
	}
	if !checks[0].Done || checks[1].Done {
		t.Fatalf("done=%v,%v", checks[0].Done, checks[1].Done)
	}
	if checks[0].Result != "SUCCESS" || checks[1].Result != "IN_PROGRESS" {
		t.Fatalf("results=%q,%q", checks[0].Result, checks[1].Result)
	}
}

func TestStatusContextPendingStatesAreNotDone(t *testing.T) {
	checks := Normalize(rollup(
		map[string]any{"__typename": "StatusContext", "context": "legacy", "state": "PENDING"},
		map[string]any{"__typename": "StatusContext", "context": "old", "state": "SUCCESS"},
		map[string]any{"__typename": "StatusContext", "context": "expected", "state": "expected"},
		map[string]any{"__typename": "StatusContext", "targetUrl": "https://example.test/s"},
	))
	var done []bool
	for _, check := range checks {
		done = append(done, check.Done)
	}
	if !reflect.DeepEqual(done, []bool{false, true, false, false}) {
		t.Fatalf("done=%v", done)
	}
	if checks[3].Name != "?" || checks[3].Result != "?" || checks[3].URL != "https://example.test/s" {
		t.Fatalf("missing fields: %+v", checks[3])
	}
}

func TestFailedCoversEveryNonPassingConclusion(t *testing.T) {
	for conclusion, failed := range map[string]bool{
		"SUCCESS": false, "NEUTRAL": false, "SKIPPED": false, "success": false,
		"FAILURE": true, "TIMED_OUT": true, "CANCELLED": true, "ACTION_REQUIRED": true,
	} {
		check := Normalize(rollup(checkRun("x", "COMPLETED", conclusion)))[0]
		if check.Failed() != failed {
			t.Errorf("%s: failed=%v", conclusion, check.Failed())
		}
	}
	if Normalize(rollup(checkRun("x", "IN_PROGRESS", "FAILURE")))[0].Failed() {
		t.Error("an unfinished check is not a failure")
	}
}

func TestDurationIsComputedInUTC(t *testing.T) {
	check := Normalize(rollup(checkRun("build", "COMPLETED", "SUCCESS",
		"startedAt", "2026-09-09T23:20:01Z", "completedAt", "2026-09-09T23:21:31Z")))[0]
	if check.Seconds != 90 {
		t.Fatalf("seconds=%d", check.Seconds)
	}
}

// 期待値は Python の calendar.timegm(time.strptime(value, "%Y-%m-%dT%H:%M:%SZ")) で確かめた。
func TestTimestampsFollowPythonStrptime(t *testing.T) {
	for value, want := range map[string]int64{
		"2026-09-09T23:20:01Z":   1788996001,
		"2026-9-9T1:2:3Z":        1788915723,
		"2026-09-09t23:20:01z":   1788996001,
		"2026-09- 9T23:20:01Z":   1788996001,
		"2026-09-09T23:20:60Z":   1788996060,
		"2026-09-09T23:20:61Z":   1788996061,
		"0026-09-09T00:00:00Z":   -61324992000,
		"2024-02-29T00:00:00Z":   1709164800,
		"2026-02-30T00:00:00Z":   -1,
		"2026-09-09T23:20:01.5Z": -1,
		"2026-09-09T24:00:00Z":   -1,
		" 2026-09-09T23:20:01Z":  -1,
		"2026-09-09T23:20:01Z ":  -1,
		"2026-09-09T23:20:01Z\n": -1,
		"0000-01-01T00:00:00Z":   -1,
		"":                       -1,
	} {
		got, ok := parseTimestamp(value)
		if want == -1 {
			if ok {
				t.Errorf("%q: parsed as %d", value, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%q: got %d (%v), want %d", value, got, ok, want)
		}
	}
}

func TestMissingOrReversedTimesGiveZero(t *testing.T) {
	for _, pair := range [][2]string{
		{"", "2026-09-09T23:21:31Z"},
		{"2026-09-09T23:21:31Z", ""},
		{"2026-09-09T23:21:31Z", "2026-09-09T23:20:01Z"},
		{"2026-09-09T23:20:01.123Z", "2026-09-09T23:21:31Z"},
	} {
		if got := secondsBetween(pair[0], pair[1]); got != 0 {
			t.Errorf("%v: %d", pair, got)
		}
	}
}

func TestNodesWithoutTypenameAreCheckRunsAndOthersAreSkipped(t *testing.T) {
	checks := Normalize([]any{
		map[string]any{"status": "COMPLETED", "conclusion": "SUCCESS"},
		"not an object", nil, 3.0,
		map[string]any{"__typename": nil, "context": "null typename", "state": "SUCCESS"},
	})
	if len(checks) != 2 || checks[0].Name != "?" || !checks[0].Done || checks[1].Name != "null typename" {
		t.Fatalf("checks=%+v", checks)
	}
	// conclusion が無ければ status を、どちらも無ければ ? を結果にする。
	checks = Normalize(rollup(map[string]any{"name": "a"}, map[string]any{"name": "b", "status": "QUEUED"}))
	if checks[0].Result != "?" || checks[1].Result != "QUEUED" {
		t.Fatalf("results=%+v", checks)
	}
}

// --- Wait ---

func TestWaitsForThePushedCommitBeforeJudging(t *testing.T) {
	// push 直後に見える旧 commit の完了済み check で成功と答えない。
	old := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	running := rollup(checkRun("build", "IN_PROGRESS", ""))
	done := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	waiter, _ := makeWaiter(concat(
		[]poll{{head: "old-sha", rollup: old}, {head: "old-sha", rollup: old}, {head: "new-sha", rollup: running}},
		repeat(poll{head: "new-sha", rollup: done}, 5),
	), func(w *Waiter) { w.SHA = "new-sha" })
	outcome := waiter.Run()
	if outcome.Status != StatusComplete || outcome.Head != "new-sha" || len(outcome.Failed()) != 0 {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestHeadThatNeverArrivesTimesOut(t *testing.T) {
	waiter, _ := makeWaiter([]poll{{head: "old-sha"}}, func(w *Waiter) { w.SHA = "new-sha"; w.StartTimeout = 30 })
	outcome := waiter.Run()
	if outcome.Status != StatusHeadTimeout || outcome.Head != "old-sha" || outcome.Message != "new-sha" ||
		outcome.Elapsed != 30 {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestLateRegisteredCheckIsNotMissed(t *testing.T) {
	// 先行ジョブの失敗で起動する後続 workflow を settle 待ちで拾う。
	first := rollup(checkRun("tests", "COMPLETED", "FAILURE"))
	second := rollup(checkRun("tests", "COMPLETED", "FAILURE"), checkRun("coverage", "IN_PROGRESS", ""))
	third := rollup(checkRun("tests", "COMPLETED", "FAILURE"), checkRun("coverage", "COMPLETED", "FAILURE"))
	waiter, _ := makeWaiter(concat(
		[]poll{{head: "sha", rollup: first}, {head: "sha", rollup: second}, {head: "sha", rollup: third}},
		repeat(poll{head: "sha", rollup: third}, 5),
	))
	outcome := waiter.Run()
	if outcome.Status != StatusComplete || len(outcome.Checks) != 2 || len(outcome.Failed()) != 2 {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestSettleRequiresTheSameSetTwice(t *testing.T) {
	done := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	waiter, clock := makeWaiter([]poll{{head: "sha", rollup: done}}, func(w *Waiter) { w.Settle = 20; w.Interval = 10 })
	if outcome := waiter.Run(); outcome.Status != StatusComplete {
		t.Fatalf("outcome=%+v", outcome)
	}
	// 1 回目で記録し、settle を満たす 20 秒後の poll で確定する。
	if clock.value != 20*time.Second {
		t.Fatalf("clock=%v", clock.value)
	}
}

func TestChangedCheckSetRestartsTheSettle(t *testing.T) {
	// 完了済みの集合に別の完了済み check が加わったら、そこから settle を測り直す。
	one := rollup(checkRun("a", "COMPLETED", "SUCCESS"))
	two := rollup(checkRun("a", "COMPLETED", "SUCCESS"), checkRun("b", "COMPLETED", "SUCCESS"))
	waiter, clock := makeWaiter([]poll{{head: "sha", rollup: one}, {head: "sha", rollup: two}})
	if outcome := waiter.Run(); outcome.Status != StatusComplete || len(outcome.Checks) != 2 {
		t.Fatalf("outcome=%+v", outcome)
	}
	if clock.value != 30*time.Second {
		t.Fatalf("clock=%v", clock.value)
	}
}

func TestEmptyCheckListTimesOut(t *testing.T) {
	waiter, _ := makeWaiter([]poll{{head: "sha"}}, func(w *Waiter) { w.StartTimeout = 30 })
	if outcome := waiter.Run(); outcome.Status != StatusEmptyTimeout || outcome.Head != "sha" {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestConflictWithoutChecksReturnsAtOnce(t *testing.T) {
	// コンフリクトで workflow が起動しない PR を start timeout まで待たない。
	waiter, clock := makeWaiter([]poll{{head: "sha", mergeable: "CONFLICTING"}}, func(w *Waiter) { w.StartTimeout = 100 })
	outcome := waiter.Run()
	if outcome.Status != StatusConflict || !outcome.Conflicting || clock.value != 0 {
		t.Fatalf("outcome=%+v clock=%v", outcome, clock.value)
	}
}

func TestConflictWithRunningChecksWaitsForThem(t *testing.T) {
	// push トリガ等で check が動いていれば完了まで待ち、結果に併記する。
	running := rollup(checkRun("build", "IN_PROGRESS", ""))
	done := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	waiter, _ := makeWaiter(concat(
		[]poll{{head: "sha", rollup: running, mergeable: "CONFLICTING"}},
		repeat(poll{head: "sha", rollup: done, mergeable: "CONFLICTING"}, 5),
	))
	if outcome := waiter.Run(); outcome.Status != StatusComplete || !outcome.Conflicting {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestMergeabilityStillUnknownIsNotAConflict(t *testing.T) {
	// push 直後の計算中 (UNKNOWN) を衝突と誤判定しない。
	done := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	waiter, _ := makeWaiter(concat(
		[]poll{{head: "sha", mergeable: "UNKNOWN"}},
		repeat(poll{head: "sha", rollup: done, mergeable: "UNKNOWN"}, 5),
	))
	if outcome := waiter.Run(); outcome.Status != StatusComplete || outcome.Conflicting {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestConflictBeforeTheHeadArrivesIsNotJudged(t *testing.T) {
	// head 待ちの間は旧 commit の mergeable で打ち切らない。
	done := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	waiter, _ := makeWaiter(concat(
		[]poll{{head: "old-sha", mergeable: "CONFLICTING"}},
		repeat(poll{head: "new-sha", rollup: done}, 5),
	), func(w *Waiter) { w.SHA = "new-sha" })
	if outcome := waiter.Run(); outcome.Status != StatusComplete {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestPendingForeverTimesOut(t *testing.T) {
	waiter, _ := makeWaiter([]poll{{head: "sha", rollup: rollup(checkRun("build", "IN_PROGRESS", ""))}},
		func(w *Waiter) { w.Timeout = 50 })
	outcome := waiter.Run()
	if outcome.Status != StatusTimeout || outcome.Elapsed != 50 || len(outcome.Checks) != 1 || outcome.Head != "sha" {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestNewPushSwitchesTheTarget(t *testing.T) {
	done := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	running := rollup(checkRun("build", "IN_PROGRESS", ""))
	var reports []string
	waiter, _ := makeWaiter(concat(
		[]poll{{head: "first", rollup: done}, {head: "second", rollup: running}},
		repeat(poll{head: "second", rollup: done}, 5),
	), func(w *Waiter) { w.Progress = func(text string) { reports = append(reports, text) } })
	outcome := waiter.Run()
	if outcome.Status != StatusComplete || outcome.Head != "second" {
		t.Fatalf("outcome=%+v", outcome)
	}
	if !reflect.DeepEqual(reports[:2], []string{"0s total=1 pending=0 settling", messages.Text(testLanguage, idHeadChanged, map[string]any{"From": "first", "To": "second"})}) {
		t.Fatalf("reports=%q", reports)
	}
}

func TestNewPushRestartsTheOverallTimeout(t *testing.T) {
	// 別 push の時点から --timeout を数え直す。
	running := rollup(checkRun("build", "IN_PROGRESS", ""))
	waiter, clock := makeWaiter(concat(
		repeat(poll{head: "first", rollup: running}, 3),
		[]poll{{head: "second", rollup: running}},
	), func(w *Waiter) { w.Timeout = 40 })
	outcome := waiter.Run()
	if outcome.Status != StatusTimeout || outcome.Head != "second" || outcome.Elapsed != 40 || clock.value != 70*time.Second {
		t.Fatalf("outcome=%+v clock=%v", outcome, clock.value)
	}
}

func TestTimeoutsAreMeasuredFromTheArrivedHead(t *testing.T) {
	// head 待ちが明けた瞬間から --timeout を数える。head_timeout だけが最初の poll から数える。
	running := rollup(checkRun("build", "IN_PROGRESS", ""))
	waiter, clock := makeWaiter(concat(
		repeat(poll{head: "old"}, 3),
		[]poll{{head: "new", rollup: running}},
	), func(w *Waiter) { w.SHA = "new"; w.Timeout = 40 })
	outcome := waiter.Run()
	if outcome.Status != StatusTimeout || outcome.Elapsed != 40 || clock.value != 70*time.Second {
		t.Fatalf("outcome=%+v clock=%v", outcome, clock.value)
	}
}

func TestTransientFailuresDoNotEndTheWatch(t *testing.T) {
	done := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	var reports []string
	waiter, _ := makeWaiter(concat(
		[]poll{{err: &FetchError{Message: "boom", Retryable: true}}},
		repeat(poll{head: "sha", rollup: done}, 6),
	), func(w *Waiter) { w.Progress = func(text string) { reports = append(reports, text) } })
	if outcome := waiter.Run(); outcome.Status != StatusComplete {
		t.Fatalf("outcome=%+v", outcome)
	}
	if reports[0] != messages.Text(testLanguage, idFetchFailed, map[string]any{"Elapsed": 0, "Count": 1}) {
		t.Fatalf("reports=%q", reports)
	}
}

func TestRepeatedFailuresEndTheWatch(t *testing.T) {
	waiter, clock := makeWaiter([]poll{{err: &FetchError{Message: "boom", Retryable: true}}})
	outcome := waiter.Run()
	if outcome.Status != StatusError || outcome.Message != "boom" || clock.value != 40*time.Second {
		t.Fatalf("outcome=%+v clock=%v", outcome, clock.value)
	}
}

func TestFailureCountResetsAfterASuccessfulPoll(t *testing.T) {
	// 連続でない失敗は数えない。4 回の失敗と成功を繰り返しても監視を落とさない。
	failure := poll{err: &FetchError{Message: "boom", Retryable: true}}
	running := poll{head: "sha", rollup: rollup(checkRun("build", "IN_PROGRESS", ""))}
	done := poll{head: "sha", rollup: rollup(checkRun("build", "COMPLETED", "SUCCESS"))}
	waiter, _ := makeWaiter(concat(repeat(failure, 4), []poll{running}, repeat(failure, 4), repeat(done, 3)))
	if outcome := waiter.Run(); outcome.Status != StatusComplete {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestUnrecoverableFailureEndsTheWatchAtOnce(t *testing.T) {
	// detached HEAD のように待っても直らない失敗でリトライしない。
	waiter, clock := makeWaiter([]poll{{err: &FetchError{Message: "could not determine current branch"}}})
	if outcome := waiter.Run(); outcome.Status != StatusError || clock.value != 0 {
		t.Fatalf("outcome=%+v clock=%v", outcome, clock.value)
	}
}

func TestMissingPullRequestIsReportedSeparately(t *testing.T) {
	waiter, _ := makeWaiter([]poll{{err: &NoPullRequestError{Message: "no pull requests found"}}})
	if outcome := waiter.Run(); outcome.Status != StatusNoPR || outcome.Message != "no pull requests found" {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestProgressReportsEveryPoll(t *testing.T) {
	var reports []string
	running := rollup(checkRun("build", "IN_PROGRESS", ""))
	done := rollup(checkRun("build", "COMPLETED", "SUCCESS"))
	waiter, _ := makeWaiter(concat(
		[]poll{{head: "old"}, {head: "", rollup: running}, {head: "sha", rollup: running}},
		repeat(poll{head: "sha", rollup: done}, 3),
	), func(w *Waiter) { w.SHA = "sha"; w.Progress = func(text string) { reports = append(reports, text) } })
	waiter.Run()
	want := []string{
		messages.Text(testLanguage, idWaitHead, map[string]any{"Elapsed": 0, "Head": "old", "Target": "sha"}),
		messages.Text(testLanguage, idWaitHead, map[string]any{"Elapsed": 10, "Head": "?", "Target": "sha"}),
		"0s total=1 pending=1",
		"10s total=1 pending=0 settling",
		"20s total=1 pending=0 settling",
	}
	if !reflect.DeepEqual(reports, want) {
		t.Fatalf("reports=%q", reports)
	}
}

// --- DetachedHead ---

func fakeGit(responses map[string]string) func(args ...string) string {
	return func(args ...string) string {
		for key, value := range responses {
			for _, arg := range args {
				if arg == key {
					return value
				}
			}
		}
		return ""
	}
}

func TestDetachedLookupDecision(t *testing.T) {
	cases := []struct {
		name      string
		reference string
		git       map[string]string
		want      bool
	}{
		{"explicit reference is not detached lookup", "155", map[string]string{"--git-dir": ".git"}, false},
		{"on a branch is not detached lookup", "", map[string]string{"--git-dir": ".git", "symbolic-ref": "feature"}, false},
		{"outside a repository is not detached lookup", "", map[string]string{}, false},
		{"detached head is detected without a branch ref", "", map[string]string{"--git-dir": ".git"}, true},
	}
	for _, tc := range cases {
		if got := IsDetached(tc.reference, fakeGit(tc.git)); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

// --- ResolvePullRequest ---

// runResolve は results を 1 回ずつ返す find で ResolvePR を呼ぶ。要素は番号か error。
func runResolve(t *testing.T, results []any, lookupTimeout, interval int) (string, int, *fakeClock, error) {
	t.Helper()
	clock := &fakeClock{}
	count := 0
	find := func(string) (string, error) {
		result := results[min(count, len(results)-1)]
		count++
		if err, ok := result.(error); ok {
			return "", err
		}
		return result.(string), nil
	}
	number, err := ResolvePR(testLanguage, strings.Repeat("A", 40), lookupTimeout, interval, find, clock, nil)
	return number, count, clock, err
}

func TestPRThatAppearsLateIsStillFound(t *testing.T) {
	number, count, clock, err := runResolve(t, []any{&NoPullRequestError{Message: "no open PR"}, "213"}, 60, 10)
	if err != nil || number != "213" || count != 2 || clock.value != 10*time.Second {
		t.Fatalf("number=%q err=%v count=%d clock=%v", number, err, count, clock.value)
	}
}

func TestPRFoundAtOnceDoesNotWait(t *testing.T) {
	number, count, clock, err := runResolve(t, []any{"213"}, 60, 10)
	if err != nil || number != "213" || count != 1 || clock.value != 0 {
		t.Fatalf("number=%q err=%v count=%d clock=%v", number, err, count, clock.value)
	}
}

func TestStillMissingAfterTheTimeoutIsNoPullRequest(t *testing.T) {
	_, count, _, err := runResolve(t, []any{&NoPullRequestError{Message: "no open PR"}}, 30, 10)
	var noPR *NoPullRequestError
	if !errors.As(err, &noPR) || noPR.Message != messages.Text(testLanguage, idNoPR, map[string]any{"SHA": strings.Repeat("A", 40)})+messages.Text(testLanguage, idNoPRRetried, map[string]any{"Seconds": 30}) {
		t.Fatalf("err=%v", err)
	}
	// 0s, 10s, 20s, 30s の 4 回。30s + 10 > 30 なので待たずに確定する。
	if count != 4 {
		t.Fatalf("count=%d", count)
	}
}

func TestZeroTimeoutTriesExactlyOnce(t *testing.T) {
	_, count, clock, err := runResolve(t, []any{&NoPullRequestError{Message: "no open PR"}}, 0, 10)
	var noPR *NoPullRequestError
	if !errors.As(err, &noPR) || count != 1 || clock.value != 0 {
		t.Fatalf("err=%v count=%d", err, count)
	}
	// 待たずに確定したときは再試行の秒数を書かない。
	if noPR.Message != messages.Text(testLanguage, idNoPR, map[string]any{"SHA": strings.Repeat("A", 40)}) {
		t.Fatalf("message=%q", noPR.Message)
	}
}

func TestUnrecoverableGHFailureIsNotRetried(t *testing.T) {
	_, count, _, err := runResolve(t, []any{&FetchError{Message: "not a git repository"}}, 60, 10)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) || count != 1 {
		t.Fatalf("err=%v count=%d", err, count)
	}
}

func TestTransientGHFailureIsRetried(t *testing.T) {
	number, count, _, err := runResolve(t, []any{&FetchError{Message: "timeout", Retryable: true}, "213"}, 60, 10)
	if err != nil || number != "213" || count != 2 {
		t.Fatalf("number=%q err=%v count=%d", number, err, count)
	}
}

func TestRepeatedGHFailuresEndTheLookup(t *testing.T) {
	_, count, _, err := runResolve(t, []any{&FetchError{Message: "timeout", Retryable: true}}, 600, 10)
	if err == nil || err.Error() != "timeout" || count != FetchFailureLimit {
		t.Fatalf("err=%v count=%d", err, count)
	}
	// 上限を越える直前の失敗は、PR が無いではなく gh の失敗として返す。
	_, _, _, err = runResolve(t, []any{&FetchError{Message: "timeout", Retryable: true}}, 5, 10)
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("err=%v", err)
	}
}

func TestUnexpectedLookupErrorIsReturned(t *testing.T) {
	boom := errors.New("boom")
	if _, _, _, err := runResolve(t, []any{boom}, 60, 10); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}

func TestLookupReportsEachRetry(t *testing.T) {
	var reports []string
	clock := &fakeClock{}
	results := []error{&NoPullRequestError{Message: "x"}, &FetchError{Message: "y", Retryable: true}}
	count := 0
	_, _ = ResolvePR(testLanguage, "abc", 60, 10, func(string) (string, error) {
		if count < len(results) {
			count++
			return "", results[count-1]
		}
		return "7", nil
	}, clock, func(text string) { reports = append(reports, text) })
	want := []string{
		messages.Text(testLanguage, idRetryLookup, map[string]any{"Elapsed": 0, "SHA": "abc", "Reason": messages.T(testLanguage, idPRMissing)}),
		messages.Text(testLanguage, idRetryLookup, map[string]any{"Elapsed": 10, "SHA": "abc", "Reason": messages.Text(testLanguage, idGHFailedCount, map[string]any{"Count": 1})}),
	}
	if !reflect.DeepEqual(reports, want) {
		t.Fatalf("reports=%q", reports)
	}
}

// --- NoCiRepository ---

type fakeHasCI struct {
	result bool
	err    error
	calls  int
}

func (f *fakeHasCI) call() (bool, error) {
	f.calls++
	return f.result, f.err
}

func TestAbsentCIEndsTheWatchEarly(t *testing.T) {
	hasCI := &fakeHasCI{}
	waiter, clock := makeWaiter([]poll{{head: "sha"}},
		func(w *Waiter) { w.Interval = 20; w.StartTimeout = 100; w.NoCITimeout = 20; w.HasCI = hasCI.call })
	fetches := 0
	fetch := waiter.Fetch
	waiter.Fetch = func() (Snapshot, error) {
		fetches++
		return fetch()
	}
	outcome := waiter.Run()
	if outcome.Status != StatusNoCI || outcome.Elapsed != 20 || clock.value != 20*time.Second || fetches != 2 || hasCI.calls != 1 {
		t.Fatalf("outcome=%+v clock=%v fetches=%d evidence calls=%d", outcome, clock.value, fetches, hasCI.calls)
	}
}

func TestAbsentCINearTheFirstRetryBoundary(t *testing.T) {
	hasCI := &fakeHasCI{}
	waiter, clock := makeWaiter([]poll{{head: "sha"}}, func(w *Waiter) {
		w.Interval = 20
		w.NoCITimeout = 20
		w.HasCI = hasCI.call
	})
	fetches := 0
	fetch := waiter.Fetch
	waiter.Fetch = func() (Snapshot, error) {
		fetches++
		if fetches == 1 {
			clock.value += 999 * time.Millisecond
		}
		return fetch()
	}
	outcome := waiter.Run()
	if outcome.Status != StatusNoCI || outcome.Elapsed != 20 || clock.value != 20*time.Second+999*time.Millisecond || fetches != 2 {
		t.Fatalf("outcome=%+v clock=%v fetches=%d", outcome, clock.value, fetches)
	}
}

func TestCheckAtFirstRetryIsNotJudgedAbsent(t *testing.T) {
	hasCI := &fakeHasCI{}
	check := rollup(checkRun("build", "IN_PROGRESS", ""))
	waiter, clock := makeWaiter([]poll{{head: "sha"}, {head: "sha", rollup: check}}, func(w *Waiter) {
		w.Interval = 20
		w.NoCITimeout = 20
		w.HasCI = hasCI.call
		w.Timeout = 40
	})
	outcome := waiter.Run()
	if outcome.Status != StatusTimeout || outcome.Elapsed != 40 || clock.value != 40*time.Second || hasCI.calls != 0 {
		t.Fatalf("outcome=%+v clock=%v calls=%d", outcome, clock.value, hasCI.calls)
	}
}

func TestRegisteredCIStillWaitsForTheFullStartTimeout(t *testing.T) {
	hasCI := &fakeHasCI{result: true}
	waiter, clock := makeWaiter([]poll{{head: "sha"}},
		func(w *Waiter) { w.StartTimeout = 100; w.NoCITimeout = 30; w.HasCI = hasCI.call })
	outcome := waiter.Run()
	// 形跡は repo ごとに 1 回だけ問い合わせる。
	if outcome.Status != StatusEmptyTimeout || clock.value != 100*time.Second || hasCI.calls != 1 {
		t.Fatalf("outcome=%+v clock=%v calls=%d", outcome, clock.value, hasCI.calls)
	}
}

func TestCheckThatAppearsInTimeNeverAsksForEvidence(t *testing.T) {
	// 通常の repo では gh の呼び出しを増やさない。
	hasCI := &fakeHasCI{result: true}
	waiter, _ := makeWaiter([]poll{{head: "sha"}, {head: "sha", rollup: rollup(checkRun("build", "COMPLETED", "SUCCESS"))}},
		func(w *Waiter) { w.StartTimeout = 100; w.NoCITimeout = 30; w.HasCI = hasCI.call })
	if outcome := waiter.Run(); outcome.Status != StatusComplete || hasCI.calls != 0 {
		t.Fatalf("outcome=%+v calls=%d", outcome, hasCI.calls)
	}
}

func TestUndecidableEvidenceKeepsWaiting(t *testing.T) {
	// 判定できないときに打ち切ると、遅れている check を見ないまま終わってしまう。
	hasCI := &fakeHasCI{err: &FetchError{Message: "gh が落ちた", Retryable: true}}
	var reports []string
	waiter, clock := makeWaiter([]poll{{head: "sha"}}, func(w *Waiter) {
		w.StartTimeout = 100
		w.NoCITimeout = 30
		w.HasCI = hasCI.call
		w.Progress = func(text string) { reports = append(reports, text) }
	})
	outcome := waiter.Run()
	if outcome.Status != StatusEmptyTimeout || clock.value != 100*time.Second || hasCI.calls != 1 {
		t.Fatalf("outcome=%+v clock=%v calls=%d", outcome, clock.value, hasCI.calls)
	}
	found := false
	for _, report := range reports {
		found = found || report == messages.Text(testLanguage, idCIUnknown, map[string]any{"Error": "gh が落ちた"})
	}
	if !found {
		t.Fatalf("reports=%q", reports)
	}
}

func TestWithoutAnEvidenceProbeTheWatchWaitsForTheStartTimeout(t *testing.T) {
	waiter, clock := makeWaiter([]poll{{head: "sha"}}, func(w *Waiter) { w.StartTimeout = 100; w.NoCITimeout = 30 })
	if outcome := waiter.Run(); outcome.Status != StatusEmptyTimeout || clock.value != 100*time.Second {
		t.Fatalf("outcome=%+v clock=%v", outcome, clock.value)
	}
}

// --- Summary ---

func mixed() []Check {
	return Normalize(rollup(checkRun("build", "COMPLETED", "SUCCESS"), checkRun("tests", "COMPLETED", "FAILURE")))
}

func TestOnlyFailuresAreListedByDefault(t *testing.T) {
	// 成功を並べると出力が伸び、読み手が切り詰めて結論を落とす。
	lines := Summarize(mixed(), false)
	want := []string{"  FAILURE   tests  0s", "            https://example.test/tests"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines=%q", lines)
	}
}

func TestFailuresComeFirstWithTheirURL(t *testing.T) {
	lines := Summarize(mixed(), true)
	want := []string{"  FAILURE   tests  0s", "            https://example.test/tests", "  SUCCESS   build  0s"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines=%q", lines)
	}
}

func TestSummaryOrderAndPadding(t *testing.T) {
	checks := []Check{
		{Name: "b", Done: true, Result: "success"},
		{Name: "é", Done: true, Result: "FAILURE"},
		{Name: "a", Done: true, Result: "ACTION_REQUIRED"},
		{Name: "z", Done: false, Result: "IN_PROGRESS"},
		{Name: "a", Done: true, Result: "SKIPPED", Seconds: 3},
	}
	lines := Summarize(checks, true)
	want := []string{
		"  ACTION_REQUIRED a  0s",
		"  FAILURE   é  0s",
		"  SKIPPED   a  3s",
		"  SUCCESS   b  0s",
		"  IN_PROGRESS z  0s",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines=%q", lines)
	}
	if got := padRight("日本", 4); got != "日本  " {
		t.Fatalf("padRight=%q", got)
	}
}

// --- NoPullRequestDetection ---

func TestMarkerMatchesTheMessageGHPrints(t *testing.T) {
	err := classify("no pull requests found for branch \"feature\"\n")
	var noPR *NoPullRequestError
	if !errors.As(err, &noPR) || noPR.Message != `no pull requests found for branch "feature"` {
		t.Fatalf("err=%#v", err)
	}
}

// testLanguage はテストを流す表示言語である（hook のテストと同じ環境変数で選ぶ）。
var testLanguage = i18n.Normalize(os.Getenv("HHX_TEST_LANGUAGE"))
