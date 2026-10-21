"""Strict, dependency-free validation for the versioned watch input contract."""
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
ACTORS = {
    "initial-read": "client", "initial-park": "client", "cancel": "client", "reconnect": "client",
    "publish-begin": "publication", "install": "publication", "fanout": "publication", "publication-fail": "publication",
    "expire": "retry", "retry-begin": "retry", "retry-read": "retry", "retry-cancel": "retry", "retry-replace": "retry", "forward": "retry",
    "send": "transport", "ack": "client", "apply": "observer", "drop-old-response": "transport", "observe": "observer",
}
PROPERTIES = ["request-accounting", "parked-watch-retained", "retry-persistence", "generation-ownership",
              "installation-means-send", "ack-means-application"]


def schema_check(value, schema, root, path="$"):
    if "$ref" in schema:
        return schema_check(value, root["$defs"][schema["$ref"].split("/")[-1]], root, path)
    kind = schema.get("type")
    types = {"object": dict, "array": list, "string": str, "integer": int, "boolean": bool}
    if kind and type(value) is not types[kind]:
        raise ValueError(f"{path}: expected {kind}")
    if "const" in schema and value != schema["const"]:
        raise ValueError(f"{path}: wrong constant")
    if "enum" in schema and value not in schema["enum"]:
        raise ValueError(f"{path}: unsupported value")
    if kind == "object":
        if set(value) != set(schema["properties"]):
            raise ValueError(f"{path}: missing or unknown fields")
        for key, item in value.items():
            schema_check(item, schema["properties"][key], root, path + "." + key)
    if kind == "array":
        if len(value) < schema.get("minItems", 0) or len(value) > schema.get("maxItems", float("inf")):
            raise ValueError(f"{path}: array bound")
        if schema.get("uniqueItems") and len({json.dumps(v, sort_keys=True) for v in value}) != len(value):
            raise ValueError(f"{path}: duplicate item")
        for index, item in enumerate(value):
            schema_check(item, schema["items"], root, f"{path}[{index}]")
    if kind == "integer" and not schema.get("minimum", 0) <= value <= schema.get("maximum", float("inf")):
        raise ValueError(f"{path}: integer bound")
    if kind == "string" and len(value) < schema.get("minLength", 0):
        raise ValueError(f"{path}: empty identifier")


def validate(request):
    schema = json.loads((HERE / "schema.json").read_text())
    schema_check(request, schema, schema)
    if request["cacheProfile"] != ("per-type" if request["subjectId"] == "watch-pertype" else "pinned-ads"):
        raise ValueError("cache profile does not match subject")
    ids = set()
    if len(request["events"]) > request["bounds"]["events"]:
        raise ValueError("event bound exceeded")
    for event in request["events"]:
        if event["id"] in ids or not set(event["happensAfter"]) <= ids:
            raise ValueError("duplicate identity or forward/missing causal edge")
        if ACTORS[event["kind"]] != event["actor"]:
            raise ValueError("operation assigned to wrong actor")
        arguments, kind = event["arguments"], event["kind"]
        if (kind != "install" and arguments["names"]) or (kind not in ("install", "ack") and arguments["version"]):
            raise ValueError("unused operation arguments")
        if kind not in ("cancel", "reconnect") and arguments["generation"] != 0:
            raise ValueError("unused generation argument")
        ids.add(event["id"])
    for expectation in request["expectations"]:
        if not set(expectation["witnessOperationIds"]) <= ids:
            raise ValueError("unknown property witness")
