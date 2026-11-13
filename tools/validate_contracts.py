"""Validate contract artifacts offline. Never runs sample commands or product code."""
import argparse
import json
import re
import sys
from decimal import Decimal
from functools import lru_cache
from pathlib import Path
from urllib.parse import unquote

try:
    import rfc3339_validator  # Required: date-time assertions must not be skipped.
    from jsonschema import Draft202012Validator, FormatChecker, validators
    from referencing import Registry, Resource
    import openapi_contract
except ImportError:
    print("Missing dependencies: python -m pip install -r tools/requirements-contracts.txt", file=sys.stderr)
    sys.exit(2)


def object_members(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON member")
        result[key] = value
    return result


def reject_constant(_value):
    raise ValueError("non-finite JSON number")


def exact_integer(_checker, value):
    if isinstance(value, Decimal):
        return value.is_finite() and value == value.to_integral_value()
    return Draft202012Validator.TYPE_CHECKER.is_type(value, "integer")


ContractValidator = validators.extend(
    Draft202012Validator,
    type_checker=Draft202012Validator.TYPE_CHECKER.redefine("integer", exact_integer),
)


def read_json(data):
    if isinstance(data, bytes):
        data = data.decode("utf-8")
    value = json.loads(data, object_pairs_hook=object_members, parse_constant=reject_constant, parse_float=Decimal)
    for node in walk(value):
        if isinstance(node, str):
            node.encode("utf-8")  # Reject lone surrogates, including escaped ones.
        elif isinstance(node, Decimal) and not node.is_finite():
            raise ValueError("non-finite JSON number")
    return value


def walk(node):
    yield node
    if isinstance(node, dict):
        for key, value in node.items():
            yield key
            yield from walk(value)
    elif isinstance(node, list):
        for value in node:
            yield from walk(value)


def local_refs(schema):
    for node in walk(schema):
        if isinstance(node, dict) and "$ref" in node:
            target = node["$ref"]
            if not target.startswith("#/"):
                raise ValueError("schema references must be local JSON pointers")
            resolved = schema
            for part in unquote(target[2:]).split("/"):
                resolved = resolved[part.replace("~1", "/").replace("~0", "~")]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1], help="Repository root")
    parser.add_argument("--refresh-tools-list", action="store_true", help="Refresh embedded schemas from canonical definitions, then validate")
    parser.add_argument("--refresh-openapi", action="store_true", help="Refresh the HTTP test index and endpoint tables from YAML, then validate")
    args = parser.parse_args()
    root = args.root.resolve()
    contracts = root / "docs/contracts"
    catalog = read_json((contracts / "schemas/messages.schema.json").read_bytes())
    fixtures = read_json((contracts / "acceptance/messages.json").read_bytes())
    listing_path = contracts / "acceptance/tools-list.json"
    listing = read_json(listing_path.read_bytes())
    ContractValidator.check_schema(catalog)
    local_refs(catalog)
    if fixtures["contract"] != "Draft 2" or (contracts / "acceptance" / fixtures["schema"]).resolve() != (contracts / "schemas/messages.schema.json").resolve():
        raise ValueError("fixture contract or schema source does not match")
    registry = Registry().with_resource(catalog["$id"], Resource.from_contents(catalog))
    formats = FormatChecker()
    if formats.conforms("2026-13-08T04:00:00.000Z", "date-time"):
        raise ValueError("date-time assertion is unavailable")
    failures = []
    counts = {"valid": 0, "invalid": 0, "raw_json": 0, "markdown": 0, "links": 0, "mcp": 0}
    openapi = openapi_contract.read_yaml((contracts / "openapi.yaml").read_text(encoding="utf-8"))
    counts.update(openapi_contract.validate(openapi, contracts, catalog, fixtures, ContractValidator, formats))
    openapi_contract.sync_views(openapi, contracts, args.refresh_openapi)

    def require(condition, label):
        if not condition:
            failures.append(label)

    @lru_cache(maxsize=None)
    def validator(model):
        if model not in catalog["$defs"]:
            raise ValueError("unknown schema model: " + model)
        return ContractValidator({"$schema": catalog["$schema"], "$ref": catalog["$id"] + "#/$defs/" + model}, registry=registry, format_checker=formats)

    def message(value, model, label, invalid=False):
        error = next(validator(model).iter_errors(value), None)
        if invalid:
            require(error is not None, label + ": invalid sample was accepted")
        elif error is not None:
            failures.append(label + ": " + str(list(error.path)) + " violates " + str(error.validator))

    # The MCP table owns output mapping; conditional argument refs own input shapes.
    outputs = {}
    for line in (contracts / "mcp.md").read_text(encoding="utf-8").splitlines():
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        if len(cells) == 5 and cells[-1].endswith("ToolBody"):
            outputs[cells[0]] = cells[-1]
    inputs = {}
    for node in walk(catalog["$defs"]["MCPToolCall"]):
        if isinstance(node, dict) and "if" in node and "then" in node:
            name = node["if"].get("properties", {}).get("name", {}).get("const")
            if name:
                inputs[name] = node["then"]["properties"]["arguments"]["$ref"].split("/")[-1]
    require(set(inputs) == set(outputs) and len(inputs) == 8, "tool mappings must name the same eight tools")

    def standalone(model):
        names = set()

        def include(name):
            if name in names:
                return
            names.add(name)
            for node in walk(catalog["$defs"][name]):
                if isinstance(node, dict) and "$ref" in node:
                    include(node["$ref"].removeprefix("#/$defs/").split("/")[0])

        include(model)
        return {"$schema": catalog["$schema"], "type": "object", "$ref": "#/$defs/" + model, "$defs": {name: value for name, value in catalog["$defs"].items() if name in names}}

    if args.refresh_tools_list:
        for tool in listing["result"]["tools"]:
            name = tool["name"]
            tool["inputSchema"] = standalone(inputs[name])
            tool["outputSchema"] = standalone(outputs[name])
        listing_path.write_text(json.dumps(listing, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    message(listing, "MCPToolsListResponse", "tools-list.json")
    tool_defs = {tool["name"]: tool for tool in listing["result"]["tools"]}
    require(len(tool_defs) == 8 and set(tool_defs) == set(inputs), "tools/list has duplicate or missing tools")
    require(len(json.dumps(listing, ensure_ascii=False).encode("utf-8")) <= 1024 * 1024, "tools/list exceeds 1 MiB")
    for name, tool in tool_defs.items():
        require(tool["annotations"]["readOnlyHint"] == (name not in ("request_command", "cancel_task")), name + ": readOnlyHint")
        for key, mapping in (("inputSchema", inputs), ("outputSchema", outputs)):
            actual = tool[key]
            ContractValidator.check_schema(actual)
            local_refs(actual)
            require(actual == standalone(mapping[name]), name + ": stale " + key + "; use --refresh-tools-list")

    def mcp_result(value, tool_name, label):
        result = value["result"]
        parsed = read_json(result["content"][0]["text"])
        require(parsed == result["structuredContent"], label + ": text differs from structuredContent")
        output = tool_defs[tool_name]["outputSchema"]
        require(ContractValidator(output, format_checker=formats).is_valid(result["structuredContent"]), label + ": wrong tool output schema")
        counts["mcp"] += 1

    seen = set()
    for group in ("valid", "invalid"):
        for entry in fixtures[group]:
            label = entry["id"]
            require(label not in seen, "duplicate fixture id: " + label)
            seen.add(label)
            message(entry["value"], entry["model"], label, invalid=(group == "invalid"))
            counts[group] += 1
            if group == "valid" and entry["model"] == "MCPToolResponse":
                mcp_result(entry["value"], entry["tool"], label)
            if group == "valid" and entry["model"] == "MCPToolCall":
                params = entry["value"]["params"]
                shape = tool_defs[params["name"]]["inputSchema"]
                require(ContractValidator(shape, format_checker=formats).is_valid(params.get("arguments", {})), label + ": advertised input schema differs")

    for entry in fixtures["raw_json"]:
        label = entry["id"]
        require(label not in seen, "duplicate fixture id: " + label)
        seen.add(label)
        require(("json" in entry) != ("hex" in entry), label + ": exactly one raw source required")
        expected = entry["expected"]
        require(expected in ("valid", "invalid_json", "invalid_model"), label + ": unknown expectation")
        require(expected != "invalid_model" or "model" in entry, label + ": missing model")
        raw = bytes.fromhex(entry["hex"]) if "hex" in entry else entry["json"]
        try:
            value = read_json(raw)
        except (ValueError, UnicodeError):
            require(expected == "invalid_json", label + ": unexpected JSON rejection")
        else:
            require(expected != "invalid_json", label + ": invalid JSON accepted")
            if "model" in entry:
                message(value, entry["model"], label, invalid=(expected == "invalid_model"))
        counts["raw_json"] += 1

    # Existing prose examples are checked against their explicit domain models.
    examples = {
        "README.md": ["Error"], "agent.md": ["Registration", "PollInput", "PollResult", "TaskCancelEvent"],
        "tasks.md": ["TaskMeta", "StartGrant", "ResultInput"],
        "mcp.md": ["MCPInitialize", "MCPInitializeResponse", "MCPToolCall", "MCPToolResponse"],
        "admin.md": ["CredentialCreate"],
    }
    for filename, models in examples.items():
        blocks = re.findall(r"```json\s*\n(.*?)\n```", (contracts / filename).read_text(encoding="utf-8"), re.S)
        require(len(blocks) == len(models), filename + ": update prose example model mapping")
        last_tool = None
        for index, (block, model) in enumerate(zip(blocks, models), 1):
            value = read_json(block)
            label = filename + " JSON " + str(index)
            message(value, model, label)
            if model == "MCPToolCall":
                last_tool = value["params"]["name"]
            if model == "MCPToolResponse":
                mcp_result(value, last_tool, label)
            counts["markdown"] += 1

    link_sources = [(path, re.findall(r"\[[^\]]+\]\(([^)]+)\)", path.read_text(encoding="utf-8")))
                    for path in [root / "README.md", *sorted((root / "docs").rglob("*.md"))]]
    link_sources.append((contracts / "openapi.yaml", [node["externalDocs"]["url"]
                        for node in openapi_contract.walk(openapi)
                        if isinstance(node, dict) and "externalDocs" in node]))
    for path, targets in link_sources:
        for target in targets:
            if re.match(r"[a-zA-Z][a-zA-Z0-9+.-]*:", target):
                continue
            filename, separator, anchor = unquote(target.strip("<>")).partition("#")
            dest = path.parent / filename if filename else path
            require(dest.is_file(), str(path.relative_to(root)) + ": missing " + target)
            if dest.is_file() and separator and dest.suffix == ".md":
                headings = re.findall(r"^#{1,6}\s+(.+)$", dest.read_text(encoding="utf-8"), re.M)
                anchors = [re.sub(r"[^\w\s-]", "", heading.strip().lower()).replace(" ", "-") for heading in headings]
                require(anchor in anchors, str(path.relative_to(root)) + ": missing anchor " + target)
            counts["links"] += 1

    for failure in failures:
        print("FAIL " + failure, file=sys.stderr)
    print(("PASS " if not failures else "FAIL ") + json.dumps(counts, sort_keys=True))
    return 1 if failures else 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:
        print("FAIL " + type(exc).__name__ + ": " + str(exc).splitlines()[0], file=sys.stderr)
        sys.exit(1)
