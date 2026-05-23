import json
import glob
import os

def compare_keys(a, b, path=""):
    if isinstance(a, dict) and isinstance(b, dict):
        keys_a = set(a.keys())
        keys_b = set(b.keys())
        missing_in_b = keys_a - keys_b
        extra_in_b = keys_b - keys_a
        if missing_in_b:
            print(f"[{path}] Missing in Leoflow: {missing_in_b}")
        if extra_in_b:
            print(f"[{path}] Extra in Leoflow: {extra_in_b}")
        
        for k in keys_a.intersection(keys_b):
            compare_keys(a[k], b[k], path + "." + k if path else k)
    elif isinstance(a, list) and isinstance(b, list):
        if len(a) > 0 and len(b) > 0:
            compare_keys(a[0], b[0], path + "[]")

for filepath in glob.glob("airflow_*.json"):
    leoflow_path = filepath.replace("airflow_", "leoflow_")
    if os.path.exists(leoflow_path):
        print(f"\n=== Comparing {filepath} ===")
        try:
            # ndjson fix
            with open(filepath) as f:
                content = f.read().strip()
                if content.startswith('{') and '\n{' in content:
                    a = [json.loads(line) for line in content.split('\n')]
                else:
                    a = json.loads(content)
            with open(leoflow_path) as f:
                content = f.read().strip()
                if content.startswith('{') and '\n{' in content:
                    b = [json.loads(line) for line in content.split('\n')]
                else:
                    b = json.loads(content)
            compare_keys(a, b)
        except Exception as e:
            print(f"Error comparing {filepath}: {e}")
