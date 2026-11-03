package install

import (
	"errors"
	"fmt"

	"github.com/HappyOnigiri/hhx/internal/hookrt"
)

// absentMatcher は matcher キーを持たないグループの識別子である。空文字の matcher とは区別する。
const absentMatcher = "\x00absent"

// managedGroup は hhx が書く 1 つのグループである。CLI・イベント・matcher の組ごとに 1 つ作る。
type managedGroup struct {
	event   string
	matcher string
	hooks   []*jsonNode
}

func (g managedGroup) matcherKey() string {
	if g.matcher == "" {
		return absentMatcher
	}
	return g.matcher
}

func (g managedGroup) node() *jsonNode {
	group := &jsonNode{kind: jsonObject}
	if g.matcher != "" {
		group.setField("matcher", scalarNode(g.matcher))
	}
	group.setField("hooks", &jsonNode{kind: jsonArray, items: g.hooks})
	return group
}

// desiredGroups は definitions から agent に書くグループを作る。
// グループとグループ内のエントリは、definitions と Registrations に現れた順に並ぶ。
func desiredGroups(agent hookrt.Agent, binary string, definitions []hookrt.Definition) ([]managedGroup, error) {
	var groups []managedGroup
	index := map[[2]string]int{}
	for _, definition := range definitions {
		for _, registration := range definition.Registrations {
			if registration.Agent != agent {
				continue
			}
			if binary == "" {
				return nil, errors.New("the hhx executable path is not set")
			}
			command, err := HookCommand(binary, definition.Name)
			if err != nil {
				return nil, err
			}
			key := [2]string{registration.Event, registration.Matcher}
			position, ok := index[key]
			if !ok {
				position = len(groups)
				index[key] = position
				groups = append(groups, managedGroup{event: registration.Event, matcher: registration.Matcher})
			}
			groups[position].hooks = append(groups[position].hooks, entryNode(command, registration))
		}
	}
	return groups, nil
}

// entryNode は hook 1 本のエントリを作る。零値の任意項目は書かない（null はグループごと無視される原因になる）。
func entryNode(command string, registration hookrt.Registration) *jsonNode {
	entry := &jsonNode{kind: jsonObject}
	entry.setField("type", scalarNode("command"))
	entry.setField("command", scalarNode(command))
	if registration.Timeout > 0 {
		entry.setField("timeout", numberNode(registration.Timeout))
	}
	if registration.StatusMessage != "" {
		entry.setField("statusMessage", scalarNode(registration.StatusMessage))
	}
	if registration.AdditionalContextLimit > 0 {
		entry.setField("additionalContextLimit", numberNode(registration.AdditionalContextLimit))
	}
	return entry
}

// applyGroups は document の hhx エントリをすべて取り除き、groups を書き直す。groups が空なら取り除くだけになる。
//
// グループの位置は安定させる。Codex は hooks.json の中の位置（イベント・グループ・エントリの番号）で
// hook の信頼状態を記録するため、他のグループの位置が動くと再承認が要る。
// そのため、既存の hhx のグループがあった位置へ置き直し、新しいグループはイベントの末尾に足す。
func applyGroups(document *jsonNode, groups []managedGroup) error {
	hooks, ok := document.field("hooks")
	if ok && hooks.kind != jsonObject {
		return errors.New("the hooks entry is not a JSON object")
	}
	if !ok {
		if len(groups) == 0 {
			return nil
		}
		hooks = &jsonNode{kind: jsonObject}
		document.setField("hooks", hooks)
	}
	byEvent := map[string][]managedGroup{}
	var newEvents []string
	for _, group := range groups {
		if _, seen := byEvent[group.event]; !seen {
			if _, exists := hooks.field(group.event); !exists {
				newEvents = append(newEvents, group.event)
			}
		}
		byEvent[group.event] = append(byEvent[group.event], group)
	}
	pruned := false
	for _, event := range append([]string(nil), hooks.keys...) {
		list, _ := hooks.field(event)
		if list.kind != jsonArray {
			if len(byEvent[event]) > 0 {
				return fmt.Errorf("the %s entry is not a JSON array", event)
			}
			continue
		}
		removed := placeGroups(list, byEvent[event])
		pruned = pruned || removed
		if removed && len(list.items) == 0 {
			// 空の配列を残すと、利用者が書いた設定と見分けが付かない痕跡になる。キーごと落とす。
			hooks.removeField(event)
		}
	}
	for _, event := range newEvents {
		list := &jsonNode{kind: jsonArray}
		for _, group := range byEvent[event] {
			list.items = append(list.items, group.node())
		}
		hooks.setField(event, list)
	}
	if pruned && len(hooks.keys) == 0 {
		document.removeField("hooks")
	}
	return nil
}

// placeGroups は 1 つのイベントの配列から hhx のエントリを取り除き、groups を置き直す。
// hhx のエントリを取り除いたかを返す。
func placeGroups(list *jsonNode, groups []managedGroup) bool {
	slots := map[string]int{}
	kept := make([]*jsonNode, 0, len(list.items))
	removed := false
	for _, group := range list.items {
		entries, ok := group.field("hooks")
		if !ok || entries.kind != jsonArray {
			kept = append(kept, group)
			continue
		}
		remaining := make([]*jsonNode, 0, len(entries.items))
		for _, entry := range entries.items {
			if command, ok := hookCommandOf(entry); ok && isHHXHookCommand(command) {
				continue
			}
			remaining = append(remaining, entry)
		}
		if len(remaining) == len(entries.items) {
			kept = append(kept, group)
			continue
		}
		removed = true
		if len(remaining) > 0 {
			// 利用者の hook が残るグループの位置は挿入先にしない。手前に差し込むと、そのグループの番号が動き再承認が要る。
			entries.items = remaining
			kept = append(kept, group)
			continue
		}
		if key := groupMatcherKey(group); key != "" {
			if _, seen := slots[key]; !seen {
				slots[key] = len(kept)
			}
		}
	}
	placed := make([][]*jsonNode, len(kept)+1)
	var tail []*jsonNode
	for _, group := range groups {
		if slot, ok := slots[group.matcherKey()]; ok {
			placed[slot] = append(placed[slot], group.node())
			continue
		}
		tail = append(tail, group.node())
	}
	items := make([]*jsonNode, 0, len(kept)+len(groups))
	for index, group := range kept {
		items = append(items, placed[index]...)
		items = append(items, group)
	}
	items = append(items, placed[len(kept)]...)
	items = append(items, tail...)
	list.items = items
	return removed
}

// groupMatcherKey はグループの matcher を managedGroup.matcherKey と同じ規則で返す。
// 文字列でない matcher は hhx が書く形ではないので、空を返して位置の記録に使わない。
func groupMatcherKey(group *jsonNode) string {
	matcher, ok := group.field("matcher")
	if !ok {
		return absentMatcher
	}
	value, ok := matcher.stringValue()
	if !ok || value == "" {
		return ""
	}
	return value
}

// hookCommandOf はエントリの command 文字列を返す。type が command でないものは対象外とする。
func hookCommandOf(entry *jsonNode) (string, bool) {
	kind, ok := entry.field("type")
	if !ok {
		return "", false
	}
	if value, ok := kind.stringValue(); !ok || value != "command" {
		return "", false
	}
	command, ok := entry.field("command")
	if !ok {
		return "", false
	}
	return command.stringValue()
}
