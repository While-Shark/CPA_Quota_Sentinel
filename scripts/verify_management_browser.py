"""使用本地接口夹具验证登录、保护设置及窄屏入口。"""

import json
from pathlib import Path

from playwright.sync_api import sync_playwright


def main():
    """加载 Go 导出的页面并检查真实浏览器中的交互。"""
    root = Path(__file__).resolve().parents[1]
    fixtures = root / "build" / "smoke"
    html = (fixtures / "page.html").read_text(encoding="utf-8")
    snapshot = json.loads((fixtures / "status.json").read_text(encoding="utf-8"))
    calls = []
    provider_accounts = [
        {"auth_id": "claude-test", "provider": "claude", "quota_status": "metered",
         "windows": [{"name": "seven_day", "remaining_percent": 42}]},
        {"auth_id": "gemini-test", "provider": "gemini", "quota_status": "unknown", "windows": []},
    ]

    def respond(route):
        request = route.request
        if request.url.endswith("/status") and request.resource_type == "document":
            route.fulfill(content_type="text/html", body=html)
            return
        if "/v0/management/" in request.url:
            calls.append((request.method, request.url))
            if request.headers.get("authorization") != "Bearer browser-test-key":
                route.fulfill(status=401, json={"error": "invalid management key"})
                return
            if request.method == "PUT" and "/settings" in request.url:
                snapshot["settings"].update(request.post_data_json)
            if request.url.endswith("/provider-accounts"):
                route.fulfill(json={"accounts": provider_accounts})
                return
            route.fulfill(json=snapshot)
            return
        route.fulfill(status=404, body="not found")

    with sync_playwright() as runtime:
        browser = runtime.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1080, "height": 720})
        errors = []
        page.on("pageerror", lambda error: errors.append(str(error)))
        page.route("http://sentinel.test/**", respond)
        page.goto("http://sentinel.test/v0/resource/plugins/cpa-quota-sentinel/status")
        page.locator("#loginKeyInput").wait_for(state="visible")
        assert page.locator(".sidebar").bounding_box()["y"] == 0
        page.locator("#loginKeyInput").fill("browser-test-key")
        page.locator(".not-loaded [data-action=load]").click()
        page.get_by_text("claude-test", exact=True).wait_for()
        page.get_by_text("gemini-test", exact=True).wait_for()
        page.locator('[data-action="refresh-provider-accounts"]').click()
        page.wait_for_function("state.providerAccounts.length === 2")
        page.locator('a[href="#settings"]').click()
        protection = page.locator('[data-setting="quota_protection_enabled"]')
        protection.set_checked(True)
        page.locator('[data-setting="five_hour_remaining_threshold"]').fill("21")
        page.locator('[data-action="save-settings"]').click()
        page.wait_for_function("state.settingsDirty === false")
        assert snapshot["settings"]["quota_protection_enabled"] is True
        assert snapshot["settings"]["five_hour_remaining_threshold"] == 21
        assert any(method == "POST" and url.endswith("/provider-accounts") for method, url in calls)
        page.evaluate("window.scrollTo(0,0)")
        page.screenshot(path=str(fixtures / "settings-browser.png"), full_page=True)
        page.set_viewport_size({"width": 390, "height": 844})
        page.reload()
        login = page.locator("#loginKeyInput")
        login.wait_for(state="visible")
        login.scroll_into_view_if_needed()
        box = login.bounding_box()
        assert box["x"] >= 0 and box["x"] + box["width"] <= 390
        page.screenshot(path=str(fixtures / "login-mobile.png"), full_page=True)
        # 自定义路径必须被带入页面，再从该路径发送带鉴权的请求。
        html = html.replace('"management_path":"/plugins/cpa-quota-sentinel"',
                            '"management_path":"/my-cpa-quota-sentinel"')
        snapshot["management_path"] = "/my-cpa-quota-sentinel"
        page.goto("http://sentinel.test/v0/resource/plugins/cpa-quota-sentinel/my-cpa-quota-sentinel/status")
        page.locator("#loginKeyInput").fill("browser-test-key")
        page.locator(".not-loaded [data-action=load]").click()
        page.wait_for_function("state.loaded === true")
        assert any("/v0/management/my-cpa-quota-sentinel/status" in url for _, url in calls)
        assert not errors, errors
        assert any(method == "PUT" for method, _ in calls), calls
        browser.close()
    print("Browser login and quota protection settings save passed")


if __name__ == "__main__":
    main()
