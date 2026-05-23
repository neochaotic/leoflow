import json
import os
import sys

outdir = "audit_data"

def deep_keys(obj, prefix=""):
    """Recursively extract all keys with their types"""
    result = {}
    if isinstance(obj, dict):
        for k, v in obj.items():
            full = f"{prefix}.{k}" if prefix else k
            typename = type(v).__name__
            if v is None:
                typename = "null"
            elif isinstance(v, bool):
                typename = "boolean"
            result[full] = typename
            result.update(deep_keys(v, full))
    elif isinstance(obj, list) and len(obj) > 0:
        result.update(deep_keys(obj[0], prefix + "[]"))
    return result

def compare_endpoint(name):
    af = os.path.join(outdir, f"airflow_{name}.json")
    lf = os.path.join(outdir, f"leoflow_{name}.json")
    if not os.path.exists(af) or not os.path.exists(lf):
        return None
    try:
        with open(af) as f:
            ac = f.read().strip()
        with open(lf) as f:
            lc = f.read().strip()
        if not ac or not lc:
            return None
        # Handle NDJSON
        if ac.startswith('{') and '\n{' in ac:
            a = [json.loads(line) for line in ac.split('\n') if line.strip()]
        else:
            a = json.loads(ac)
        if lc.startswith('{') and '\n{' in lc:
            b = [json.loads(line) for line in lc.split('\n') if line.strip()]
        else:
            b = json.loads(lc)
    except Exception as e:
        return f"Parse error: {e}"

    a_keys = deep_keys(a)
    b_keys = deep_keys(b)

    missing_in_leoflow = {k: v for k, v in a_keys.items() if k not in b_keys}
    extra_in_leoflow = {k: v for k, v in b_keys.items() if k not in a_keys}
    type_mismatches = {}
    for k in set(a_keys.keys()) & set(b_keys.keys()):
        if a_keys[k] != b_keys[k]:
            type_mismatches[k] = (a_keys[k], b_keys[k])

    return {
        "missing_in_leoflow": missing_in_leoflow,
        "extra_in_leoflow": extra_in_leoflow,
        "type_mismatches": type_mismatches,
    }

# Find all pairs
names = set()
for f in os.listdir(outdir):
    if f.startswith("airflow_") and f.endswith(".json"):
        names.add(f[8:-5])

for name in sorted(names):
    result = compare_endpoint(name)
    if result is None:
        continue
    if isinstance(result, str):
        print(f"\n### {name}\n{result}")
        continue
    if not result["missing_in_leoflow"] and not result["extra_in_leoflow"] and not result["type_mismatches"]:
        print(f"\n### {name}: ✅ PERFECT MATCH")
        continue
    print(f"\n### {name}")
    if result["missing_in_leoflow"]:
        print("  MISSING in Leoflow:")
        for k, v in sorted(result["missing_in_leoflow"].items()):
            print(f"    - {k} ({v})")
    if result["extra_in_leoflow"]:
        print("  EXTRA in Leoflow:")
        for k, v in sorted(result["extra_in_leoflow"].items()):
            print(f"    + {k} ({v})")
    if result["type_mismatches"]:
        print("  TYPE MISMATCHES:")
        for k, (at, lt) in sorted(result["type_mismatches"].items()):
            print(f"    ! {k}: Airflow={at} Leoflow={lt}")
