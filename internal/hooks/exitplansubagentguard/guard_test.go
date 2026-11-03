package exitplansubagentguard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HappyOnigiri/hhx/internal/hooktest"
)

// 判定材料は transcript（JSONL）だけなので、実物で確認した行の形をそのまま組み立てて渡す。確認した形は 4 つ。
//
//	起動の要求  assistant の tool_use ブロック（name=Agent, input.description）
//	起動の結果  user の tool_result ブロック。content は文字列とブロック列の両方がありうる
//	終了通知    queue-operation 行として先に出て、続けて user メッセージにも入る
//	再開        SendMessage の tool_result。停止済みのエージェントが起動の文面なしで走り出す
//
// 起動は tool_use と tool_result の対でだけ成立するので、起動のテストは必ず対で書く。
// 実害は誤爆なので、通過するケースを厚く保つ。

const launchText = "Async agent launched successfully. (This tool result is internal metadata.)\n" +
	"agentId: %[1]s (internal ID - do not mention to user.)\n" +
	"The agent is working in the background."

// 現行の実物。文面が伸びても拾えることを見るために丸ごと置く。
const realLaunchText = "Async agent launched successfully. (This tool result is internal metadata — never quote" +
	" or paste any part of it, including the agentId below, into a user-facing reply.)\n" +
	"agentId: %[1]s (internal ID - do not mention to user. Use SendMessage with to:" +
	" '%[1]s', summary: '<5-10 word recap>' to continue this agent.)\n" +
	"The agent is working in the background. You will be notified automatically when it" +
	" completes.\n" +
	"output_file: /private/tmp/claude-1000/-Users-alice-dev-X/sess/tasks/%[1]s.output\n"

// SendMessage で停止済みのエージェントを再開したときの実物。
const resumeText = `{"success":true,"message":"Agent \"%[1]s\" had no active task; resumed from` +
	` transcript in the background with your message. You'll be notified when it finishes."}`

// mustJSON は value を 1 行の JSON にする。Claude Code の transcript（と Python の json.dumps）と同じく < > & をエスケープしない。
// エスケープすると、生の行に対する <task-notification> の部分一致が当たらなくなる。
func mustJSON(value any) string {
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func toolUseLine(toolUseID, description, name string, sidechain bool) string {
	return mustJSON(map[string]any{
		"type":        "assistant",
		"isSidechain": sidechain,
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type":  "tool_use",
			"id":    toolUseID,
			"name":  name,
			"input": map[string]any{"description": description, "subagent_type": "Explore", "prompt": "..."},
		}}},
	})
}

func agentUseLine(toolUseID, description string) string {
	return toolUseLine(toolUseID, description, "Agent", false)
}

// resultLineWith は tool_result の行である。content には文字列かブロック列を渡す。
func resultLineWith(toolUseID string, content any, sidechain bool) string {
	return mustJSON(map[string]any{
		"type":        "user",
		"isSidechain": sidechain,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{
			"tool_use_id": toolUseID,
			"type":        "tool_result",
			"content":     content,
			"is_error":    false,
		}}},
	})
}

func resultLine(toolUseID, text string) string {
	return resultLineWith(toolUseID, []any{map[string]any{"type": "text", "text": text}}, false)
}

func launchLine(toolUseID, agent string) string {
	return resultLine(toolUseID, fmt.Sprintf(launchText, agent))
}

// launchPair は起動の最小形である。tool_use と tool_result の対でだけ起動として数える。
func launchPair(toolUseID, agent, description string) []string {
	return []string{agentUseLine(toolUseID, description), launchLine(toolUseID, agent)}
}

func sendMessageLine(toolUseID, agent string) string {
	return mustJSON(map[string]any{
		"type":        "assistant",
		"isSidechain": false,
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type":  "tool_use",
			"id":    toolUseID,
			"name":  "SendMessage",
			"input": map[string]any{"to": agent, "message": "続きをお願い"},
		}}},
	})
}

func notificationBody(agent, status, summary string) string {
	return "<task-notification>\n<task-id>" + agent + "</task-id>\n<status>" + status +
		"</status>\n<summary>" + summary + "</summary>\n</task-notification>"
}

func notificationLine(agent, status string) string {
	return notificationLineWithSummary(agent, status, "done")
}

func notificationLineWithSummary(agent, status, summary string) string {
	return mustJSON(map[string]any{
		"type":        "user",
		"isSidechain": false,
		"message":     map[string]any{"role": "user", "content": notificationBody(agent, status, summary)},
	})
}

// queueNotificationLine は実物の通知が最初に記録される queue-operation 行である。message を持たない。
func queueNotificationLine(agent, status string) string {
	return mustJSON(map[string]any{
		"type":      "queue-operation",
		"operation": "enqueue",
		"sessionId": "test-session",
		"content":   notificationBody(agent, status, "done"),
	})
}

// bashResultLine は Bash の出力である。agentId の文字列が混ざっても起動として拾ってはいけない。
func bashResultLine(text string) string {
	return resultLineWith("toolu_bash", text, false)
}

func writeTranscript(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n") + "\n"
}

func payload(transcriptPath string) string {
	return mustJSON(map[string]any{
		"session_id":      "test-session",
		"transcript_path": transcriptPath,
		"hook_event_name": "PreToolUse",
		"tool_name":       "ExitPlanMode",
		"tool_input":      map[string]any{},
		"cwd":             "/tmp",
	})
}

func decideContent(t *testing.T, content string) hooktest.Result {
	t.Helper()
	return hooktest.Stdin(t, Definition(), payload(writeTranscript(t, content)))
}

func decide(t *testing.T, lines ...string) hooktest.Result {
	t.Helper()
	return decideContent(t, joinLines(lines))
}

func concat(groups ...[]string) []string {
	var lines []string
	for _, group := range groups {
		lines = append(lines, group...)
	}
	return lines
}

func expectDeny(t *testing.T, got hooktest.Result, marks ...string) {
	t.Helper()
	if got.Decision != hooktest.Deny {
		t.Fatalf("decision=%q, want deny", got.Decision)
	}
	for _, mark := range marks {
		if !strings.Contains(got.Reason, mark) {
			t.Errorf("reason does not contain %q:\n%s", mark, got.Reason)
		}
	}
}

func expectPass(t *testing.T, got hooktest.Result) {
	t.Helper()
	if got.Decision != "" {
		t.Fatalf("decision=%q, want pass (reason: %q)", got.Decision, got.Reason)
	}
}

// --- L1: 止めるもの（移植元の BlockedTest） -------------------------------------

func TestBlockedLaunchedWithoutNotification(t *testing.T) {
	got := decide(t, agentUseLine("toolu_1", "Design the sharding"), launchLine("toolu_1", "a0b7129b0d13559d4"))
	expectDeny(t, got, "a0b7129b0d13559d4", "Design the sharding")
}

// content が文字列で来る形でも拾う。
func TestBlockedFlatToolResultContent(t *testing.T) {
	got := decide(t, agentUseLine("toolu_1", "調査"),
		resultLineWith("toolu_1", fmt.Sprintf(launchText, "aaa111bbb222"), false))
	expectDeny(t, got, "aaa111bbb222")
}

func TestBlockedOnlyThePendingAgentIsListed(t *testing.T) {
	got := decide(t,
		agentUseLine("toolu_1", "Explore the tools"), launchLine("toolu_1", "aaa111bbb222"),
		agentUseLine("toolu_2", "Design the sharding"), launchLine("toolu_2", "ccc333ddd444"),
		notificationLine("aaa111bbb222", "completed"))
	expectDeny(t, got, "ccc333ddd444")
	if strings.Contains(got.Reason, "aaa111bbb222") {
		t.Errorf("the finished agent is listed:\n%s", got.Reason)
	}
}

func TestBlockedNonTerminalStatusIsNotAFinish(t *testing.T) {
	for _, status := range []string{"running", "queued", "Completed", "completedx"} {
		got := decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"),
			[]string{notificationLine("aaa111bbb222", status)})...)
		expectDeny(t, got, "aaa111bbb222")
	}
}

func TestBlockedBrokenLineDoesNotHideAPendingAgent(t *testing.T) {
	got := decide(t, concat([]string{"{ not json", `{"message": "Agent"`, "[]", "null"},
		launchPair("toolu_1", "aaa111bbb222", "調査"))...)
	expectDeny(t, got, "aaa111bbb222")
}

// description が無い起動でも id だけで止め、括弧を付けない。
func TestBlockedDescriptionIsOptional(t *testing.T) {
	got := decide(t, launchPair("toolu_1", "aaa111bbb222", "")...)
	expectDeny(t, got, "- aaa111bbb222\n")
}

// 実物の起動文面（文面は伸び続ける）でも拾う。
func TestBlockedRealLaunchText(t *testing.T) {
	got := decide(t, agentUseLine("toolu_1", "設計の穴を洗う"),
		resultLine("toolu_1", fmt.Sprintf(realLaunchText, "a0a21ead91274b7c3")))
	expectDeny(t, got, "a0a21ead91274b7c3", "設計の穴を洗う")
}

// 旧 Task 名で記録された起動でも、理由文に説明が出る。
func TestBlockedOldTaskToolNameKeepsTheDescription(t *testing.T) {
	got := decide(t, toolUseLine("toolu_1", "Explore the tools", "Task", false), launchLine("toolu_1", "aaa111bbb222"))
	expectDeny(t, got, "Explore the tools")
}

// 完了後に SendMessage で再開したエージェントは、また待つ対象に戻る。
func TestBlockedResumeViaSendMessageRestartsTheWait(t *testing.T) {
	got := decide(t,
		agentUseLine("toolu_1", "Design the sharding"), launchLine("toolu_1", "aaa111bbb222"),
		notificationLine("aaa111bbb222", "completed"),
		sendMessageLine("toolu_2", "aaa111bbb222"),
		resultLine("toolu_2", fmt.Sprintf(resumeText, "aaa111bbb222")))
	// 再開は起動の説明を引き継ぐ（先に起動していれば上書きしない）。
	expectDeny(t, got, "- aaa111bbb222 (Design the sharding)")
}

// 起動を見ていないエージェントの再開も待つ対象にする。説明は無い。
func TestBlockedResumeWithoutLaunch(t *testing.T) {
	got := decide(t, sendMessageLine("toolu_2", "aaa111bbb222"),
		resultLine("toolu_2", fmt.Sprintf(resumeText, "aaa111bbb222")))
	expectDeny(t, got, "- aaa111bbb222\n")
	// 引用符の形が違っても id を取る（\" が残る形、' の形、引用符なし）。
	for _, text := range []string{
		`Agent \"bbb222ccc333\" had no active task; resumed from transcript in the background`,
		"Agent 'bbb222ccc333' had no active task; resumed from transcript in the background",
		"Agent bbb222ccc333 had no active task; resumed from transcript in the background",
	} {
		got := decide(t, sendMessageLine("toolu_2", "x"), resultLine("toolu_2", text))
		expectDeny(t, got, "- bbb222ccc333\n")
	}
}

// 理由文の形は移植元と同じ（見出し、一覧、案内の順）。一覧は説明で安定ソートする。
func TestBlockedReasonFormatAndOrder(t *testing.T) {
	got := decide(t, concat(
		launchPair("toolu_1", "zzz111zzz111", "b-second"),
		launchPair("toolu_2", "yyy222yyy222", ""),
		launchPair("toolu_3", "xxx333xxx333", "a-first"),
		launchPair("toolu_4", "www444www444", "b-second"),
	)...)
	want := messages.Text(hooktest.Language, idReason, map[string]any{
		"Head":   messages.T(hooktest.Language, idHead),
		"Agents": "- yyy222yyy222\n- xxx333xxx333 (a-first)\n- zzz111zzz111 (b-second)\n- www444www444 (b-second)",
		"Tail":   messages.T(hooktest.Language, idTail),
	})
	if got.Reason != want {
		t.Errorf("reason:\n%s\nwant:\n%s", got.Reason, want)
	}
}

// 同じ tool_result に agentId が複数あれば、どれも起動にする。同じ id の再起動は説明を上書きし、順序は最初の位置のまま。
func TestBlockedSeveralIDsAndRelaunch(t *testing.T) {
	got := decide(t,
		agentUseLine("toolu_1", "first"),
		resultLine("toolu_1", "Async agent launched successfully\nagentId: aaa111aaa111\nagentId: bbb222bbb222"),
		agentUseLine("toolu_2", "again"),
		launchLine("toolu_2", "aaa111aaa111"))
	expectDeny(t, got, "- aaa111aaa111 (again)\n- bbb222bbb222 (first)")
}

// 64KB を超える行の後ろにある起動も読む（bufio.Scanner の既定の上限で止まらない）。
func TestBlockedAfterALongLine(t *testing.T) {
	long := bashResultLine(strings.Repeat("x", 200_000))
	got := decide(t, concat([]string{long}, launchPair("toolu_1", "aaa111bbb222", "調査"))...)
	expectDeny(t, got, "aaa111bbb222")
}

// --- L1: 通すもの（移植元の AllowedTest） --------------------------------------

func TestAllowedNoAgents(t *testing.T) {
	expectPass(t, decide(t, bashResultLine("ok")))
	expectPass(t, decideContent(t, ""))
}

// transcript やこの hook を読んだ出力に起動の文面が載っても起動ではない。
// 実際にこれで実在しないエージェントが登録され、ExitPlanMode が恒久的に deny された。
func TestAllowedLaunchTextReadFromAFileIsNotALaunch(t *testing.T) {
	expectPass(t, decide(t,
		toolUseLine("toolu_2", "transcript を読む", "Bash", false),
		bashResultLine(fmt.Sprintf(realLaunchText, "ccc333ddd444"))))
	// Bash の tool_use の結果として同じ id に紐づけても、起動ではない。
	expectPass(t, decide(t,
		toolUseLine("toolu_2", "transcript を読む", "Bash", false),
		resultLine("toolu_2", fmt.Sprintf(realLaunchText, "ccc333ddd444"))))
}

// 再開の文面も、SendMessage の結果でなければ待つ対象に戻さない。
func TestAllowedResumeTextReadFromAFileIsNotAResume(t *testing.T) {
	expectPass(t, decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"), []string{
		notificationLine("aaa111bbb222", "completed"),
		bashResultLine(fmt.Sprintf(resumeText, "aaa111bbb222")),
	})...))
	// Agent の結果に再開の文面があっても、再開ではない。
	expectPass(t, decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"), []string{
		notificationLine("aaa111bbb222", "completed"),
		agentUseLine("toolu_3", "x"),
		resultLine("toolu_3", fmt.Sprintf(resumeText, "aaa111bbb222")),
	})...))
}

func TestAllowedCompletedNotification(t *testing.T) {
	expectPass(t, decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"),
		[]string{notificationLine("aaa111bbb222", "completed")})...))
}

// 終端状態はすべて終了として扱う。stopped は resume 時の合成通知、killed は TaskStop の実物で、理由文が案内する逃げ道そのもの。
func TestAllowedEveryTerminalStatus(t *testing.T) {
	for _, status := range []string{"completed", "stopped", "killed", "failed", "error", "cancelled"} {
		got := decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"),
			[]string{notificationLine("aaa111bbb222", status)})...)
		if got.Decision != "" {
			t.Errorf("status %q must finish the agent", status)
		}
	}
}

func TestAllowedKilledNotificationIsTheEscapeHatch(t *testing.T) {
	expectPass(t, decide(t,
		agentUseLine("toolu_1", "PR差分を機能整理"),
		launchLine("toolu_1", "aaa111bbb222"),
		queueNotificationLine("aaa111bbb222", "killed"),
		notificationLineWithSummary("aaa111bbb222", "killed", `Agent "PR差分を機能整理" was stopped by Claude`)))
}

// 通知は queue-operation 行として先に記録される。message を持たない形でも終了。
func TestAllowedQueueOperationNotificationOnly(t *testing.T) {
	expectPass(t, decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"),
		[]string{queueNotificationLine("aaa111bbb222", "completed")})...))
}

// resume 直後は前のセッションの分の通知が先に並ぶ。順序で取りこぼさない。
func TestAllowedNotificationBeforeLaunch(t *testing.T) {
	expectPass(t, decide(t, concat([]string{notificationLine("aaa111bbb222", "completed")},
		launchPair("toolu_1", "aaa111bbb222", "調査"))...))
}

// 1 つの通知に task-id が複数あれば、どれも終了にする。
func TestAllowedSeveralTaskIDsInOneNotification(t *testing.T) {
	line := mustJSON(map[string]any{"type": "user", "message": map[string]any{"content": notificationBody("aaa111bbb222", "completed", "x") +
		"<task-id>ccc333ddd444</task-id>"}})
	expectPass(t, decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "a"), launchPair("toolu_2", "ccc333ddd444", "b"),
		[]string{line})...))
}

// subagent が起動した孫は、親の待つ対象ではない。
func TestAllowedSidechainLaunchIsIgnored(t *testing.T) {
	expectPass(t, decide(t,
		toolUseLine("toolu_1", "Explore", "Agent", true),
		resultLineWith("toolu_1", []any{map[string]any{"type": "text", "text": fmt.Sprintf(launchText, "aaa111bbb222")}}, true)))
	// 起動の結果だけが sidechain でも拾わない。
	expectPass(t, decide(t,
		agentUseLine("toolu_1", "Explore"),
		resultLineWith("toolu_1", []any{map[string]any{"type": "text", "text": fmt.Sprintf(launchText, "aaa111bbb222")}}, true)))
}

// sidechain の終了通知は、親のエージェントを終了させない（isSidechain が真の行は丸ごと無視する）。
func TestBlockedSidechainNotificationIsIgnored(t *testing.T) {
	line := mustJSON(map[string]any{"isSidechain": true, "message": map[string]any{
		"content": notificationBody("aaa111bbb222", "completed", "x"),
	}})
	expectDeny(t, decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"), []string{line})...), "aaa111bbb222")
	// isSidechain が真の bool でなければ無視しない（移植元の `is True`）。
	for _, value := range []string{`"true"`, `1`, `null`, `false`} {
		line := `{"isSidechain": ` + value + `, "content": "` + strings.ReplaceAll(notificationBody("aaa111bbb222", "completed", "x"), "\n", `\n`) + `"}`
		expectPass(t, decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"), []string{line})...))
	}
}

// 起動の文面の無いテキストは、agentId の文字列があっても起動ではない。
func TestAllowedAgentIDInCommandOutputIsNotALaunch(t *testing.T) {
	expectPass(t, decide(t, bashResultLine("agentId: aaa111bbb222\nagentId: ccc333ddd444")))
	// Agent の結果でも、起動の文面が無ければ起動ではない（同期実行のエージェント）。
	expectPass(t, decide(t, agentUseLine("toolu_1", "sync"), resultLine("toolu_1", "done. agentId: aaa111bbb222")))
	// id が 6 文字未満なら拾わない。
	expectPass(t, decide(t, agentUseLine("toolu_1", "short"),
		resultLine("toolu_1", "Async agent launched successfully\nagentId: ab12")))
}

func TestAllowedUnreadableTranscriptFailsOpen(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	expectPass(t, hooktest.Stdin(t, Definition(), payload(missing)))
	// ディレクトリは開けても読めない。
	expectPass(t, hooktest.Stdin(t, Definition(), payload(t.TempDir())))
	expectPass(t, hooktest.Argv(t, Definition(), missing))
}

// matcher を取り違えて登録されても、payload に ExitPlanMode の語が無ければ一次ゲートで抜ける。
// 逆に、語を含む payload（この hook を扱う Bash など）なら tool_name に関わらず判定する。
func TestAllowedOtherToolsAreUntouched(t *testing.T) {
	path := writeTranscript(t, joinLines(launchPair("toolu_1", "aaa111bbb222", "調査")))
	raw := mustJSON(map[string]any{"transcript_path": path, "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	expectPass(t, hooktest.Stdin(t, Definition(), raw))
	raw = mustJSON(map[string]any{"transcript_path": path, "tool_name": "Bash", "tool_input": map[string]any{"command": "grep ExitPlanMode x"}})
	expectDeny(t, hooktest.Stdin(t, Definition(), raw), "aaa111bbb222")
	if gate([]byte(raw)) != true || gate([]byte(`{"tool_name":"Bash"}`)) {
		t.Error("the gate must look only for the word ExitPlanMode")
	}
}

// --- デバッグ経路と奇妙な入力 -------------------------------------------------

// argv[1] は transcript のパスで、stdin を読まず一次ゲートも通さない。
func TestArgvIsTheTranscriptPath(t *testing.T) {
	path := writeTranscript(t, joinLines(launchPair("toolu_1", "aaa111bbb222", "調査")))
	expectDeny(t, hooktest.Argv(t, Definition(), path), "aaa111bbb222")
	got := hooktest.Run(t, Definition(), payload("/nonexistent"), hooktest.Options{Args: []string{path}})
	expectDeny(t, got, "aaa111bbb222")
	expectPass(t, hooktest.Argv(t, Definition(), ""))
	// 相対パスはプロセスの作業ディレクトリから開く。
	t.Chdir(filepath.Dir(path))
	expectDeny(t, hooktest.Argv(t, Definition(), filepath.Base(path)), "aaa111bbb222")
}

func TestOddPayloadsPassSilently(t *testing.T) {
	path := writeTranscript(t, joinLines(launchPair("toolu_1", "aaa111bbb222", "調査")))
	quoted := mustJSON(path)
	for _, raw := range []string{
		"", " ", "ExitPlanMode", "null ExitPlanMode", `"ExitPlanMode"`, `["ExitPlanMode"]`,
		`{"tool_name": "ExitPlanMode"}`,
		`{"tool_name": "ExitPlanMode", "transcript_path": null}`,
		`{"tool_name": "ExitPlanMode", "transcript_path": ""}`,
		`{"tool_name": "ExitPlanMode", "transcript_path": [` + quoted + `]}`,
		`{"tool_name": "ExitPlanMode", "transcript_path": 1}`,
		`{"tool_name": "ExitPlanMode", "transcript_path": ` + quoted + `} x`,
		`{"tool_name": "ExitPlanMode", "transcript_path": ` + quoted + `}{}`,
		// 移植元は stdin を UTF-8 として厳密に読み、不正なバイト列で例外になる。
		"{\"tool_name\": \"ExitPlanMode\", \"transcript_path\": " + quoted + ", \"x\": \"\xff\"}",
	} {
		if got := hooktest.Stdin(t, Definition(), raw); got.Decision != "" {
			t.Errorf("%q: decision=%q", raw, got.Decision)
		}
	}
	// 末尾の改行と、transcript_path 以外の項目の有無は判定を変えない。
	raw := `{"tool_name": "ExitPlanMode", "transcript_path": ` + quoted + "}\n"
	expectDeny(t, hooktest.Stdin(t, Definition(), raw), "aaa111bbb222")
	expectDeny(t, hooktest.Stdin(t, Definition(), `{"transcript_path": `+quoted+`, "big": 1e400, "n": 123456789012345678901234567890, "ExitPlanMode": 1}`),
		"aaa111bbb222")
}

func TestDisabledByConfig(t *testing.T) {
	path := writeTranscript(t, joinLines(launchPair("toolu_1", "aaa111bbb222", "調査")))
	got := hooktest.Run(t, Definition(), payload(path), hooktest.Options{Config: "hooks:\n  exit-plan-subagent-guard:\n    enabled: false\n"})
	expectPass(t, got)
	got = hooktest.Run(t, Definition(), "", hooktest.Options{Config: "hooks:\n  exit-plan-subagent-guard:\n    enabled: false\n", Args: []string{path}})
	expectPass(t, got)
}

// 移植元で例外になって無出力で終わっていた形は、ほかに未完了のエージェントがあっても無出力にする。
func TestPythonExceptionsPassSilently(t *testing.T) {
	pending := launchPair("toolu_1", "aaa111bbb222", "調査")
	for _, line := range []string{
		// 真で dict でない message（.get の AttributeError）。
		`{"message": "Agent", "c": "Async agent launched successfully"}`,
		`{"message": ["Async agent launched successfully"]}`,
		`{"message": 1, "c": "Async agent launched successfully"}`,
		`{"message": true, "c": "Async agent launched successfully"}`,
		// 真で dict でない input。
		`{"message": {"content": [{"type": "tool_use", "name": "Agent", "id": "t9", "input": "x"}]}}`,
		`{"message": {"content": [{"type": "tool_use", "name": "Task", "id": "t9", "input": [1]}]}}`,
		// set に入れられない id と tool_use_id（TypeError）。
		`{"message": {"content": [{"type": "tool_use", "name": "Agent", "id": ["t9"]}]}}`,
		`{"message": {"content": [{"type": "tool_use", "name": "SendMessage", "id": {"a": 1}}]}}`,
		`{"message": {"content": [{"type": "tool_result", "tool_use_id": ["t9"], "content": "Async agent launched successfully"}]}}`,
	} {
		if got := decide(t, concat(pending, []string{line})...); got.Decision != "" {
			t.Errorf("%s: decision=%q, want silence", line, got.Decision)
		}
	}
	// 偽の値は空の dict として扱い、判定を続ける。
	for _, line := range []string{
		`{"message": null, "c": "Async agent launched successfully"}`,
		`{"message": "", "c": "Async agent launched successfully"}`,
		`{"message": 0, "c": "Async agent launched successfully"}`,
		`{"message": 0.0, "c": "Async agent launched successfully"}`,
		`{"message": -0e5, "c": "Async agent launched successfully"}`,
		`{"message": false, "c": "Async agent launched successfully"}`,
		`{"message": [], "c": "Async agent launched successfully"}`,
		`{"message": {}, "c": "Async agent launched successfully"}`,
		`{"message": {"content": [{"type": "tool_use", "name": "Agent", "id": "t9", "input": null}]}}`,
		`{"message": {"content": [{"type": "tool_use", "name": "Agent", "id": "t9", "input": 0}]}}`,
		// Agent / Task / SendMessage 以外の tool_use と、tool_result 以外のブロックは id を見ない。
		`{"message": {"content": [{"type": "tool_use", "name": "Bash", "id": ["t9"]}, {"type": "text", "tool_use_id": ["x"]}, 1, "Agent"]}}`,
		`{"message": {"content": "\"tool_use\" \"Agent\""}}`,
	} {
		expectDeny(t, decide(t, concat(pending, []string{line})...), "aaa111bbb222")
	}
}

// id の一致は Python の == と同じ（true == 1 == 1.0、整数と浮動小数点数は値で比べる）。
func TestIDsCompareLikePython(t *testing.T) {
	launch := func(id, toolUseID string) []string {
		return []string{
			`{"message": {"content": [{"type": "tool_use", "name": "Agent", "id": ` + id + `, "input": {"description": "d"}}]}}`,
			`{"message": {"content": [{"type": "tool_result", "tool_use_id": ` + toolUseID + `, "content": "Async agent launched successfully agentId: aaa111bbb222"}]}}`,
		}
	}
	for _, pair := range [][2]string{
		{"1", "1"},
		{"true", "1"},
		{"1", "1.0"},
		{"true", "1e0"},
		{"false", "0"},
		{"0", "-0.0"},
		{"null", "null"},
		{"100", "1e2"},
		{"123456789012345678901234567890", "123456789012345678901234567890"},
		{"1e400", "2e400"},
		{`"t"`, `"t"`},
		{"0.1", "0.10"},
		{"0.1", "0.1000000000000000055511151231257827"},
	} {
		if got := decide(t, launch(pair[0], pair[1])...); got.Decision != hooktest.Deny {
			t.Errorf("id %s and tool_use_id %s must match", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]string{
		{"1", `"1"`},
		{"true", `"true"`},
		{"null", `"null"`},
		{"null", "0"},
		{"0", "false2"},
		{"1", "2"},
		{"1e30", "1000000000000000000000000000000"},
		{"1e400", "-1e400"},
		{"0.1", "0.2"},
	} {
		if got := decide(t, launch(pair[0], pair[1])...); got.Decision != "" {
			t.Errorf("id %s and tool_use_id %s must not match", pair[0], pair[1])
		}
	}
}

// transcript の不正な UTF-8 は置換して読み、ほかの部分の判定を続ける。
func TestInvalidUTF8IsReplaced(t *testing.T) {
	use := `{"message": {"content": [{"type": "tool_use", "name": "Agent", "id": "t1", "input": {"description": "d` + "\xe3\x81" + `x"}}]}}`
	got := decideContent(t, use+"\n"+launchLine("t1", "aaa111bbb222")+"\n\xff\xfe\n")
	expectDeny(t, got, "- aaa111bbb222 (d\ufffdx)")
}

// 改行は \n・\r・\r\n のどれでも行を区切る（Python の open の既定）。
func TestUniversalNewlines(t *testing.T) {
	lines := launchPair("toolu_1", "aaa111bbb222", "調査")
	for _, separator := range []string{"\n", "\r", "\r\n"} {
		expectDeny(t, decideContent(t, strings.Join(lines, separator)), "aaa111bbb222")
		finished := strings.Join(append(append([]string{}, lines...), notificationLine("aaa111bbb222", "completed")), separator)
		expectPass(t, decideContent(t, finished))
	}
	// \r で区切った 2 つの JSON は別々の行として読む（1 行として読むと壊れた行になる）。
	expectDeny(t, decideContent(t, lines[0]+"\r"+lines[1]), "aaa111bbb222")
}

func TestSplitUniversalNewlines(t *testing.T) {
	for text, want := range map[string][]string{
		"":               nil,
		"a":              {"a"},
		"a\rb\r\nc\n\rd": {"a\n", "b\n", "c\n", "\n", "d"},
		"a\r":            {"a\n"},
		"\r\n\r\n":       {"\n", "\n"},
	} {
		if got := splitUniversalNewlines(text); !reflect.DeepEqual(got, want) {
			t.Errorf("splitUniversalNewlines(%q)=%q, want %q", text, got, want)
		}
	}
}

// 64KB を超える tool_result の行の後ろにある終了通知を読み、通す。
func TestLongLineBeforeTheNotificationPasses(t *testing.T) {
	long := resultLine("toolu_x", strings.Repeat("長い出力", 50_000))
	expectPass(t, decide(t, concat(launchPair("toolu_1", "aaa111bbb222", "調査"),
		[]string{long, notificationLine("aaa111bbb222", "completed")})...))
	// 最後の行に改行が無くても読む。
	content := joinLines(launchPair("toolu_1", "aaa111bbb222", "調査")) + long + "\n" + notificationLine("aaa111bbb222", "completed")
	expectPass(t, decideContent(t, content))
}

func TestDefinition(t *testing.T) {
	definition := Definition()
	if definition.Name != Name || !definition.DefaultEnabled || !definition.GateStdinOnly {
		t.Errorf("definition = %+v", definition)
	}
	if len(definition.Registrations) != 1 || definition.Registrations[0].Matcher != "ExitPlanMode" ||
		definition.Registrations[0].Agent != "claude" || definition.Registrations[0].Event != "PreToolUse" {
		t.Errorf("registrations = %+v", definition.Registrations)
	}
}
