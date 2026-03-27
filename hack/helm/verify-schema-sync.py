#!/usr/bin/env python3
"""Verify that values.yaml and values.schema.json stay in sync.

Walks both trees in parallel, comparing keys at each level.  Only recurses
into a nested object when BOTH values.yaml has a non-empty dict AND the
schema defines properties for it.  This avoids false positives from user-data
keys (e.g. annotation values) and from schema-only sub-properties under
values that default to {}.
"""

import json
import os
import sys

import yaml

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
VALUES_PATH = os.path.join(REPO_ROOT, "install", "helm", "kgateway", "values.yaml")
SCHEMA_PATH = os.path.join(REPO_ROOT, "install", "helm", "kgateway", "values.schema.json")


def compare_keys(values_dict, schema_obj, prefix=""):
    """Compare keys between a values dict and a schema object, returning mismatches."""
    missing_from_schema = []
    extra_in_schema = []

    yaml_keys = set(values_dict.keys()) if isinstance(values_dict, dict) else set()
    schema_props = schema_obj.get("properties", {}) if isinstance(schema_obj, dict) else {}
    schema_keys = set(schema_props.keys())

    for key in sorted(yaml_keys - schema_keys):
        missing_from_schema.append(f"{prefix}{key}")

    for key in sorted(schema_keys - yaml_keys):
        extra_in_schema.append(f"{prefix}{key}")

    # Recurse into keys present on both sides
    for key in sorted(yaml_keys & schema_keys):
        val = values_dict[key]
        sub_schema = schema_props[key]
        # Only recurse when values.yaml has a non-empty dict AND schema defines properties
        if (isinstance(val, dict) and val
                and isinstance(sub_schema, dict) and "properties" in sub_schema):
            m, e = compare_keys(val, sub_schema, prefix=f"{prefix}{key}.")
            missing_from_schema.extend(m)
            extra_in_schema.extend(e)

    return missing_from_schema, extra_in_schema


def main():
    with open(VALUES_PATH) as f:
        values = yaml.safe_load(f) or {}
    with open(SCHEMA_PATH) as f:
        schema = json.load(f)

    missing_from_schema, extra_in_schema = compare_keys(values, schema)

    ok = True

    if missing_from_schema:
        ok = False
        print("ERROR: keys in values.yaml but missing from values.schema.json:")
        for k in missing_from_schema:
            print(f"  - {k}")

    if extra_in_schema:
        ok = False
        print("ERROR: keys in values.schema.json but missing from values.yaml:")
        for k in extra_in_schema:
            print(f"  - {k}")

    if ok:
        top_level = len(set(values.keys()))
        print(f"OK: values.yaml and values.schema.json are in sync ({top_level} top-level keys)")
    else:
        sys.exit(1)


if __name__ == "__main__":
    main()
