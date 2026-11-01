package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestManagementPathConfig 验证自定义路径不能引入 URL、路由通配符或编码绕过。
func TestManagementPathConfig(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"/sentinel_ABC-123", "/sentinel_ABC-123"},
		{"/private/sentinel", "/private/sentinel"},
		{"sentinel-secret", "/sentinel-secret"},
		{"/sentinel-secret/", "/sentinel-secret"},
		{"  sentinel-secret/  ", "/sentinel-secret"},
	} {
		cfg, err := DecodeConfig([]byte("management_path: '" + tc.input + "'"))
		if err != nil || cfg.ManagementPath != tc.want {
			t.Fatalf("path %q: cfg=%#v err=%v; want %q", tc.input, cfg, err, tc.want)
		}
	}
	for _, path := range []string{"https://example.com", "/../admin", "/v0/management", "/secret%2fstatus", "/secret?x=1", "/secret/*all", "/secret//nested", "///"} {
		if _, err := DecodeConfig([]byte("management_path: '" + path + "'")); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
}

func TestManagementPathConfigFieldDocumentsDefaultAndJumpEntry(t *testing.T) {
	fields := managementConfigFields()
	if len(fields) != 1 {
		t.Fatalf("management config fields = %#v, want one path field", fields)
	}
	field := fields[0]
	if field.Name != "management_path" || field.Default != managementBasePath {
		t.Fatalf("management path field = %#v, want default %q", field, managementBasePath)
	}
	if !strings.Contains(field.Description, managementBasePath) || !strings.Contains(field.Description, "跳转按钮") {
		t.Fatalf("management path description does not document its default and jump entry: %q", field.Description)
	}
}

// TestCustomManagementRoutes 验证宿主仅注册新入口，并使旧地址及公共资源业务请求失效。
func TestCustomManagementRoutes(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ManagementPath = "/private/sentinel"
	registration := registerManagementWithConfig(cfg)
	for _, route := range registration.Routes {
		if route.Path == managementBasePath+"/status" {
			t.Fatal("old management path remains registered")
		}
	}
	if registration.Resources[0].Path != "/private/sentinel/status" {
		t.Fatalf("resource route = %q", registration.Resources[0].Path)
	}
	store := NewPluginState(cfg)
	for _, path := range []string{
		"/v0/management/plugins/cpa-quota-sentinel/settings",
		"/v0/resource/plugins/cpa-quota-sentinel/status",
		"/v0/resource/plugins/cpa-quota-sentinel/private/sentinel/settings",
		"/settings",
	} {
		response := HandleManagementRequest(store, pluginapi.ManagementRequest{Method: http.MethodGet, Path: path}, time.Now())
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d", path, response.StatusCode)
		}
	}
	for _, path := range []string{"/v0/management/private/sentinel/settings", "/private/sentinel/settings"} {
		response := HandleManagementRequest(store, pluginapi.ManagementRequest{Method: http.MethodGet, Path: path}, time.Now())
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d", path, response.StatusCode)
		}
	}
}
func TestConfigFromSettingsManagementPath(t *testing.T) {
	base := DefaultConfig()
	payload := SettingsFromConfig(base)
	payload.ManagementPath = "  my-secret-path/ "
	updated, err := ConfigFromSettings(base, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.ManagementPath != "/my-secret-path" {
		t.Fatalf("ManagementPath = %q, want %q", updated.ManagementPath, "/my-secret-path")
	}

	invalidPayload := SettingsFromConfig(base)
	invalidPayload.ManagementPath = "https://bad-url"
	if _, err := ConfigFromSettings(base, invalidPayload); err == nil {
		t.Fatal("expected error on invalid management path")
	}
}
