package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestManualQueueUIExercisesActualTemplate checks the in-page queue editor
// logic without a browser or a live management key.
func TestManualQueueUIExercisesActualTemplate(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for management page script tests")
	}
	start := strings.Index(statusTemplateSource, "/* ---- manual Codex rotation queue ---- */")
	end := strings.Index(statusTemplateSource, "/* ---- render: logs ---- */")
	if start < 0 || end <= start {
		t.Fatal("queue editor is missing from embedded management page")
	}
	script := `const assert=require('node:assert/strict');
const clone=value=>JSON.parse(JSON.stringify(value));
const tr=(zh,en)=>zh;
const E=value=>String(value);
const pill=(text,color)=>'<span>'+text+'</span>';
const renderHeader=title=>'<h1>'+title+'</h1>',rosterWarning=()=>'',ic=()=>'',button=action=>'<button data-action="'+action+'"></button>';
const renderKeepScroll=()=>{};
const state={
 accounts:[{auth_id:'a',alias:'Account A',available:true},{auth_id:'b',available:false},{auth_id:'c',available:true}],
 nextId:'a',queueSaved:null,queueDraft:null,queueDirty:false
};
` + statusTemplateSource[start:end] + `
const empty=queueFromAPI({});
assert.equal(empty.mode,'auto');
const draft=queueAddAccounts(empty);
assert.deepEqual(draft.entries.map(item=>item.auth_id),['a','b','c']);
assert.equal(draft.entries.every(item=>item.weight===1),true);
draft.mode='manual';
draft.entries[0].weight=3;draft.entries[1].weight=2;
state.queueDraft=draft;
assert.doesNotThrow(()=>validateQueue(draft));
let html=renderQueue();
assert.match(html,/账号轮转队列/);
assert.match(html,/data-action="queue-sort-weight"/);
assert.match(html,/data-action="queue-save"/);
assert.match(html,/data-queue-drag="a"/);
assert.match(html,/data-queue-weight="a"/);
assert.match(html,/账号未加载|暂不可用/);
queueMove(2,0);
assert.deepEqual(state.queueDraft.entries.map(item=>item.auth_id),['c','a','b']);
assert.equal(state.queueDirty,true);
const bad=queueAddAccounts(queueFromAPI({mode:'manual',entries:[{auth_id:'a',weight:30}]}));
assert.throws(()=>validateQueue(bad),/权重/);
const duplicate={mode:'manual',entries:[{auth_id:'a',weight:1},{auth_id:'a',weight:1}]};
assert.throws(()=>validateQueue(duplicate),/重复/);
`
	path := filepath.Join(t.TempDir(), "manual-queue-ui.cjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("queue editor script failed: %v\n%s", err, output)
	}
}
