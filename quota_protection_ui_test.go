package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestQuotaProtectionUIAdapters 验证保护设置可往返保存，且非法百分比不会提交。
func TestQuotaProtectionUIAdapters(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("需要 Node.js 执行管理页设置校验")
	}
	start := strings.Index(statusTemplateSource, "const SETTING_MAP=")
	end := strings.Index(statusTemplateSource, "/* ---- render: shell ---- */")
	if start < 0 || end <= start {
		t.Fatal("未找到管理页设置适配器")
	}
	uiStart := strings.Index(statusTemplateSource, "/* ---- render: settings ---- */")
	uiEnd := strings.Index(statusTemplateSource, "/* ---- render: retry ---- */")
	if uiStart < 0 || uiEnd <= uiStart {
		t.Fatal("未找到管理页设置控件")
	}
	script := `const assert=require('node:assert/strict');
const clone=value=>JSON.parse(JSON.stringify(value));
const tr=(zh,en)=>zh;
const E=value=>String(value);
const isDuration=value=>/^\d+(\.\d+)?[smhd]$/.test(value);
let state={settingsDraft:{quota_protection_enabled:true,five_hour_remaining_threshold:20,
quota_protection_interval:'5m',long_window_remaining_threshold:20,quota_recovery_threshold:30,quota_auto_recover:true}};
const ic=()=>'';
const button=()=>'';
const renderHeader=()=>'';
const rosterWarning=()=>'';
` + statusTemplateSource[start:end] + `
` + statusTemplateSource[uiStart:uiEnd] + `
const api={quota_protection_enabled:true,five_hour_remaining_threshold:20,
long_window_remaining_threshold:15.5,quota_recovery_threshold:30,
quota_auto_recover:false,quota_refresh_interval:'30m',quota_protection_interval:'7m'};
const draft=settingsFromAPI(api);
validateSettings(draft);
const saved=settingsToPayload(draft);
for(const [key,value] of Object.entries(api))assert.equal(saved[key],value,key);
assert.equal(SETTING_DEFAULTS.refresh_interval,'30m');
assert.equal(SETTING_DEFAULTS.quota_protection_interval,'5m');
assert.equal(SETTING_DEFAULTS.quota_recovery_threshold,30);
assert.equal(SETTING_DEFAULTS.quota_auto_recover,true);
assert.equal(SETTING_GROUPS.find(group=>group.id==='quota_protection').items.length,6);
assert.match(settingsControl(['five_hour_remaining_threshold','percent']),/type="number"[^>]*min="0" max="100"/);
assert.match(settingsControl(['quota_auto_recover','toggle']),/type="checkbox"/);
for(const value of [-1,101,NaN,Infinity]){
 assert.throws(()=>validateSettings({...draft,five_hour_remaining_threshold:value}));
}
validateSettings({...draft,five_hour_remaining_threshold:0,long_window_remaining_threshold:100});
`
	path := filepath.Join(t.TempDir(), "quota-protection-ui.cjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("设置校验失败: %v\n%s", err, output)
	}
}

// TestManagementPageCustomPaths 验证反向代理子路径及自定义入口的地址生成。
func TestManagementPageCustomPaths(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("需要 Node.js 执行后台入口校验")
	}
	start := strings.Index(statusTemplateSource, "const DEFAULT_MANAGEMENT_PATH=")
	end := strings.Index(statusTemplateSource, "const LOCALE_STORAGE_KEY=")
	if start < 0 || end <= start {
		t.Fatal("未找到后台路径生成逻辑")
	}
	pathCode := statusTemplateSource[start:end]
	pathJSON, err := json.Marshal(pathCode)
	if err != nil {
		t.Fatal(err)
	}
	script := `const assert=require('node:assert/strict');
const vm=require('node:vm');
const source=` + string(pathJSON) + `;
function check(path,status,root,management,resource){
 const context={location:{pathname:path},STATUS:status};
 vm.runInNewContext(source+` + "`" + `
  if(ROOT_PREFIX!==${JSON.stringify(root)})throw Error('root: '+ROOT_PREFIX);
  if(managementBase()!==${JSON.stringify(management)})throw Error('management: '+managementBase());
  if(resourceStatusPath()!==${JSON.stringify(resource)})throw Error('resource: '+resourceStatusPath());
 ` + "`" + `,context);
}
check('/proxy/v0/resource/plugins/codex-quota-scheduler/status',{},'/proxy',
 '/proxy/v0/management/plugins/codex-quota-scheduler','/proxy/v0/resource/plugins/codex-quota-scheduler/status');
check('/proxy/v0/resource/plugins/codex-quota-scheduler/private/admin/status',{management_path:'/private/admin'},'/proxy',
 '/proxy/v0/management/private/admin','/proxy/v0/resource/plugins/codex-quota-scheduler/private/admin/status');
check('/proxy/v0/management/private/admin/status',{settings:{management_path:'/private/admin'}},'/proxy',
 '/proxy/v0/management/private/admin','/proxy/v0/resource/plugins/codex-quota-scheduler/private/admin/status');
check('/v0/resource/plugins/codex-quota-scheduler/status',{management_path:'//outside'},'',
 '/v0/management/plugins/codex-quota-scheduler','/v0/resource/plugins/codex-quota-scheduler/status');
`
	path := filepath.Join(t.TempDir(), "management-paths.cjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("后台入口校验失败: %v\n%s", err, output)
	}
}

// TestQuotaProtectionAccountMarkup 验证账号暂停原因及检查时间可见并转义上游文本。
func TestQuotaProtectionAccountMarkup(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("需要 Node.js 执行账号保护状态校验")
	}
	start := strings.Index(statusTemplateSource, "function quotaProtectionMarkup(a)")
	end := strings.Index(statusTemplateSource, "function accountCard(a)")
	if start < 0 || end <= start {
		t.Fatal("未找到账号保护状态逻辑")
	}
	script := `const assert=require('node:assert/strict');
const tr=(zh,en)=>zh;
const E=value=>String(value).replaceAll('<','&lt;').replaceAll('>','&gt;');
const localTime=value=>'local:'+value;
const relativeUntil=value=>'until:'+value;
` + statusTemplateSource[start:end] + `
assert.equal(quotaProtectionMarkup({quota_protection_paused:false}),'');
const account={quota_protection_paused:true,quota_protection_reason:'<script>threshold</script>',
quota_protection_disabled_at:'2026-11-01T10:00:00Z',quota_protection_next_check_at:'2026-11-01T10:05:00Z'};
const markup=quotaProtectionMarkup(account);
assert.match(markup,/额度保护已暂停/);
assert.match(markup,/暂停原因: &lt;script&gt;threshold&lt;\/script&gt;/);
assert.match(markup,/暂停时间: local:2026-11-01T10:00:00Z/);
assert.match(markup,/下次检查: local:2026-11-01T10:05:00Z/);
assert.equal(reasonText(account),'额度保护暂停');
assert.equal(reasonText({...account,auth_failure:true}),'请重新登录');
assert.match(quotaProtectionMarkup({quota_protection_paused:true}),/等待后台验证额度/);
`
	path := filepath.Join(t.TempDir(), "quota-protection-account.cjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("账号保护状态校验失败: %v\n%s", err, output)
	}
}
