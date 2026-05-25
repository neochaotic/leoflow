"""weather_open_meteo — fetch current weather from a public API (no key)."""
from __future__ import annotations

from airflow.sdk import DAG, task

CITIES = {"London": (51.51, -0.13), "Tokyo": (35.69, 139.69), "Sao Paulo": (-23.55, -46.63)}


@task
def fetch_all() -> dict:
    import requests

    out: dict[str, float] = {}
    for city, (lat, lon) in CITIES.items():
        r = requests.get(
            "https://api.open-meteo.com/v1/forecast",
            params={"latitude": lat, "longitude": lon, "current_weather": True},
            timeout=30,
        )
        r.raise_for_status()
        out[city] = r.json()["current_weather"]["temperature"]
        print(f"fetch: {city} = {out[city]}°C")
    return out


@task
def report(temps: dict) -> None:
    warmest = max(temps, key=temps.get)
    print(f"report: warmest of {len(temps)} cities is {warmest} at {temps[warmest]}°C")


with DAG("weather_open_meteo", schedule=None, catchup=False, tags=["example"]):
    report(fetch_all())
