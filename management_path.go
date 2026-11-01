package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var managementPathPattern = regexp.MustCompile(`^/[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+)*$`)

// validateManagementPath 限制入口为站内静态路径，避免路径规范化或路由通配符绕过。
func validateManagementPath(path string) (string, error) {
	if path == "" {
		return managementBasePath, nil
	}
	if len(path) > 160 || !managementPathPattern.MatchString(path) || strings.HasPrefix(path, "/v0/") {
		return "", fmt.Errorf("management_path 必须是以 / 开头、由字母数字及 -_ 组成的静态路径，最长 160 字符，不含 /v0 前缀")
	}
	return path, nil
}

// managementConfigFields 声明 CPA 原生配置弹窗支持的后台入口字段。
func managementConfigFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{{Name: "management_path", Type: pluginapi.ConfigFieldTypeString, Default: managementBasePath,
		Description: "自定义后台路径，例如 /sentinel-secret。保存插件配置后生效；原路径立即失效。后台仍需 CPA 管理密钥。"}}
}

// configuredManagementPath 返回经过校验的管理路由基址。
func configuredManagementPath(cfg Config) string {
	path, err := validateManagementPath(cfg.ManagementPath)
	if err != nil {
		return managementBasePath
	}
	return path
}

// configuredResourcePath 将自定义入口映射到宿主要求的插件资源命名空间。
func configuredResourcePath(cfg Config) string {
	path := configuredManagementPath(cfg)
	if path == managementBasePath {
		return ""
	}
	return path
}

// normalizeConfiguredManagementPath 只接受当前已配置入口，避免旧入口继续分派业务请求。
func normalizeConfiguredManagementPath(path string, cfg Config) string {
	base := configuredManagementPath(cfg)
	resource := "/v0/resource" + managementBasePath + configuredResourcePath(cfg)
	for _, prefix := range []string{"/v0/management" + base, resource, base} {
		if path == prefix {
			return "/"
		}
		if strings.HasPrefix(path, prefix+"/") {
			return strings.TrimPrefix(path, prefix)
		}
	}
	if base == managementBasePath && !strings.HasPrefix(path, "/v0/") && !strings.HasPrefix(path, managementBasePath) {
		return path
	}
	return ""
}
