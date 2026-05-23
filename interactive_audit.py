import asyncio
import json
import os
from playwright.async_api import async_playwright

async def run_audit():
    errors = {
        "airflow": [],
        "leoflow": []
    }

    def create_handler(platform):
        def handle_page_error(err):
            errors[platform].append({"type": "pageerror", "error": str(err)})
            print(f"[{platform}] Page Error: {err}")
            
        def handle_console(msg):
            if msg.type == "error":
                errors[platform].append({"type": "console.error", "text": msg.text})
                print(f"[{platform}] Console Error: {msg.text}")
                
        def handle_response(response):
            # Ignore successful requests, capture 4xx/5xx network errors
            if response.status >= 400:
                # ignore some known assets 404s
                if "favicon" in response.url:
                    return
                errors[platform].append({
                    "type": "network_error",
                    "url": response.url,
                    "status": response.status
                })
                print(f"[{platform}] Network Error: {response.status} {response.url}")
        return handle_page_error, handle_console, handle_response

    async with async_playwright() as p:
        browser = await p.chromium.launch(headless=True)
        
        async def stress_platform(platform, url, username, password, dag_id):
            print(f"\n--- Stressing {platform} ---")
            context = await browser.new_context()
            page = await context.new_page()
            pe, ce, re = create_handler(platform)
            page.on("pageerror", pe)
            page.on("console", ce)
            page.on("response", re)
            
            try:
                # Login
                await page.goto(url)
                await page.fill("input[name='username']", username)
                await page.fill("input[name='password']", password)
                await page.click("button[type='submit']")
                await page.wait_for_selector("text=DAGs", timeout=10000)
                
                # DAGs List
                print(f"[{platform}] Interacting with DAGs List")
                await page.goto(f"{url}/home")
                await page.wait_for_timeout(3000)
                
                # Click a pause toggle
                toggles = await page.query_selector_all("input[type='checkbox']")
                if toggles:
                    try:
                        await toggles[0].click(force=True)
                        await page.wait_for_timeout(1000)
                    except Exception: pass
                    
                # Click any "Trigger" play button
                play_btns = await page.query_selector_all("button[aria-label='Trigger DAG']")
                if not play_btns:
                    play_btns = await page.query_selector_all("button[title='Trigger DAG']")
                if play_btns:
                    try:
                        await play_btns[0].click()
                        await page.wait_for_timeout(1000)
                        # click confirm if modal opens
                        submit = await page.query_selector("button:has-text('Trigger')")
                        if submit: await submit.click()
                    except Exception: pass

                # Grid View
                print(f"[{platform}] Navigating to Grid")
                await page.goto(f"{url}/dags/{dag_id}/grid")
                await page.wait_for_timeout(3000)
                
                # Click roughly in the middle to hit a task instance (since SVG is tricky)
                try:
                    await page.mouse.click(500, 300)
                    await page.wait_for_timeout(2000)
                except Exception: pass

                # Click tabs if panel is open
                print(f"[{platform}] Clicking Tabs")
                tabs = await page.query_selector_all("[role='tab']")
                for tab in tabs:
                    try:
                        await tab.click()
                        await page.wait_for_timeout(500)
                    except Exception: pass

                # Graph View
                print(f"[{platform}] Navigating to Graph")
                await page.goto(f"{url}/dags/{dag_id}/graph")
                await page.wait_for_timeout(3000)

                # Try Missing Menus / Routes
                print(f"[{platform}] Checking degraded routes")
                await page.goto(f"{url}/variables")
                await page.wait_for_timeout(2000)
                await page.goto(f"{url}/connections")
                await page.wait_for_timeout(2000)
                
            except Exception as e:
                print(f"[{platform}] Exception during stress: {e}")
            finally:
                await context.close()

        await stress_platform("airflow", "http://localhost:8081", "admin", "4C4GRSKwyt99SEDH", "tutorial")
        await stress_platform("leoflow", "http://localhost:8080", "admin@leoflow.local", "admin", "demo_http_chain")
        
        await browser.close()
        
    os.makedirs("audit_data", exist_ok=True)
    with open("audit_data/interactive_errors.json", "w") as f:
        json.dump(errors, f, indent=2)
    print("\nDone. Errors saved to audit_data/interactive_errors.json")

if __name__ == "__main__":
    asyncio.run(run_audit())
