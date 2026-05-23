import asyncio
from playwright.async_api import async_playwright
import os

async def capture_screenshots():
    outdir = "audit_data/screenshots"
    os.makedirs(outdir, exist_ok=True)
    
    async with async_playwright() as p:
        browser = await p.chromium.launch(headless=True)
        
        # --- AIRFLOW 3.2.1 ---
        print("Capturing Airflow...")
        context_a = await browser.new_context()
        page_a = await context_a.new_page()
        
        try:
            # Login Airflow
            await page_a.goto("http://localhost:8081")
            await page_a.fill("input[name='username']", "admin")
            await page_a.fill("input[name='password']", "4C4GRSKwyt99SEDH")
            await page_a.click("button[type='submit']")
            await page_a.wait_for_selector("text=DAGs")
            
            # Home/Dags
            await page_a.goto("http://localhost:8081/home")
            await page_a.wait_for_timeout(2000)
            await page_a.screenshot(path=f"{outdir}/airflow_home.png", full_page=True)
            print("  airflow_home.png captured")
            
            # DAG Detail - Grid (tutorial)
            await page_a.goto("http://localhost:8081/dags/tutorial/grid")
            await page_a.wait_for_timeout(2000)
            await page_a.screenshot(path=f"{outdir}/airflow_grid.png", full_page=True)
            print("  airflow_grid.png captured")
            
            # DAG Detail - Graph (tutorial)
            await page_a.goto("http://localhost:8081/dags/tutorial/graph")
            await page_a.wait_for_timeout(2000)
            await page_a.screenshot(path=f"{outdir}/airflow_graph.png", full_page=True)
            print("  airflow_graph.png captured")
        except Exception as e:
            print(f"Airflow capture failed: {e}")

        # --- LEOFLOW ---
        print("Capturing Leoflow...")
        context_l = await browser.new_context()
        page_l = await context_l.new_page()
        
        try:
            # Login Leoflow
            await page_l.goto("http://localhost:8080")
            await page_l.fill("input[name='username']", "admin@leoflow.local")
            await page_l.fill("input[name='password']", "admin")
            await page_l.click("button[type='submit']")
            await page_l.wait_for_selector("text=DAGs")
            
            # Home/Dags
            await page_l.goto("http://localhost:8080/home")
            await page_l.wait_for_timeout(2000)
            await page_l.screenshot(path=f"{outdir}/leoflow_home.png", full_page=True)
            print("  leoflow_home.png captured")
            
            # DAG Detail - Grid (demo_http_chain)
            await page_l.goto("http://localhost:8080/dags/demo_http_chain/grid")
            await page_l.wait_for_timeout(2000)
            await page_l.screenshot(path=f"{outdir}/leoflow_grid.png", full_page=True)
            print("  leoflow_grid.png captured")
            
            # DAG Detail - Graph (demo_http_chain)
            await page_l.goto("http://localhost:8080/dags/demo_http_chain/graph")
            await page_l.wait_for_timeout(2000)
            await page_l.screenshot(path=f"{outdir}/leoflow_graph.png", full_page=True)
            print("  leoflow_graph.png captured")
        except Exception as e:
            print(f"Leoflow capture failed: {e}")
            
        await browser.close()
        print("Done capturing.")

if __name__ == "__main__":
    asyncio.run(capture_screenshots())
