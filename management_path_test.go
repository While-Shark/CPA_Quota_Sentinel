package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestManagementPathConfig 验证自定义路径不能引入 URL、路由通配符或编码绕过。
func TestManagementPathConfig(t *testing.T) {
	for _, path := range []string{"/sentinel_ABC-123", "/private/sentinel"} {
		cfg, err := DecodeConfig([]byte("management_path: " + path))
		if err != nil || cfg.ManagementPath != path {
			t.Fatalf("path %q: cfg=%#v err=%v", path, cfg, err)
		}
	}
	for _, path := range []string{"https://example.com", "/../admin", "/v0/management", "/secret/", "/secret%2fstatus", "/secret?x=1", "/secret/*all", "/secret//nested"} {
		if _, err := DecodeConfig([]byte("management_path: '" + path + "'")); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
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
		"/v0/management/plugins/codex-quota-scheduler/settings",
		"/v0/resource/plugins/codex-quota-scheduler/status",
		"/v0/resource/plugins/codex-quota-scheduler/private/sentinel/settings",
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
