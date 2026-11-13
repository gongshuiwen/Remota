"""Offline OpenAPI validation and deterministic views of the authoritative YAML."""
import json
import os
import re
from urllib.parse import unquote, urlsplit, urlunsplit

import yaml
from jsonschema_path import SchemaPath
from openapi_spec_validator import OpenAPIV31SpecValidator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

SCHEMA_REF = "../docs/contracts/schemas/messages.schema.json#/$defs/"


class StrictLoader(yaml.SafeLoader):
    def construct_mapping(self, node, deep=False):
        result = {}
        for key_node, value_node in node.value:
            key = self.construct_object(key_node, deep=deep)
            if not isinstance(key, str):
                raise ValueError("OpenAPI YAML keys must be strings; quote HTTP status codes")
            if key in result:
                raise ValueError("duplicate YAML key: " + key)
            result[key] = self.construct_object(value_node, deep=deep)
        return result


def read_yaml(text):
    document = yaml.load(text, Loader=StrictLoader)
    # Reject cyclic aliases and non-JSON scalars (timestamps, non-finite floats, etc.).
    json.dumps(document, allow_nan=False)
    if not isinstance(document, dict):
        raise ValueError("OpenAPI YAML must contain an object")
    return document


def walk(value):
    yield value
    if isinstance(value, dict):
        for child in value.values():
            yield from walk(child)
    elif isinstance(value, list):
        for child in value:
            yield from walk(child)


def pointer(document, reference):
    if reference in ("", "#"):
        return document
    if not reference.startswith("#/"):
        raise ValueError("expected a local OpenAPI reference: " + reference)
    value = document
    for part in reference[2:].split("/"):
        value = value[part.replace("~1", "/").replace("~0", "~")]
    return value


def load(root):
    """Assemble local OpenAPI objects; keep the shared message catalog external."""
    folder = (root / "api").resolve()
    entry = folder / "openapi.yaml"
    catalog = (root / "docs/contracts/schemas/messages.schema.json").resolve()
    documents = {}

    def read(path):
        if not path.is_relative_to(folder) or path.suffix != ".yaml":
            raise ValueError("OpenAPI reference outside api/: " + str(path))
        if path not in documents:
            documents[path] = read_yaml(path.read_text(encoding="utf-8"))
        return documents[path]

    def expand(value, owner, active=frozenset()):
        if isinstance(value, list):
            return [expand(child, owner, active) for child in value]
        if not isinstance(value, dict):
            return value
        if "$ref" in value:
            if set(value) != {"$ref"}:
                raise ValueError("OpenAPI references must be pure $ref; edit the referenced object instead")
            reference = urlsplit(value["$ref"])
            if reference.scheme or reference.netloc or reference.query:
                raise ValueError("external OpenAPI resource forbidden: " + value["$ref"])
            target = (owner.parent / unquote(reference.path)).resolve() if reference.path else owner
            fragment = "#" + unquote(reference.fragment) if reference.fragment else ""
            if target == catalog:
                if not re.fullmatch(r"#/\$defs/\w+", fragment):
                    raise ValueError("message reference must select a canonical model")
                return {"$ref": SCHEMA_REF + fragment.rsplit("/", 1)[1]}
            if target == entry and fragment.startswith("#/components/"):
                pointer(read(entry), fragment)
                return {"$ref": fragment}
            key = (target, fragment)
            if key in active:
                raise ValueError("cyclic OpenAPI object reference: " + value["$ref"])
            return expand(pointer(read(target), fragment), target, active | {key})
        result = {key: expand(child, owner, active) for key, child in value.items()}
        if "externalDocs" in result:
            url = urlsplit(result["externalDocs"]["url"])
            if not url.scheme and not url.netloc:
                destination = (owner.parent / unquote(url.path)).resolve()
                relative = os.path.relpath(destination, folder).replace("\\", "/")
                result["externalDocs"]["url"] = urlunsplit(("", "", relative, url.query, url.fragment))
        return result

    return expand(read(entry), entry)


def resolve(document, value):
    seen = set()
    while "$ref" in value and value["$ref"].startswith("#/"):
        reference = value["$ref"]
        if reference in seen:
            raise ValueError("cyclic OpenAPI object reference: " + reference)
        seen.add(reference)
        value = pointer(document, reference)
    return value


def model(document, schema):
    if set(schema) != {"$ref"}:
        raise ValueError("HTTP message schemas must only reference the canonical message catalog")
    value = resolve(document, schema)
    match = re.fullmatch(re.escape(SCHEMA_REF) + r"(\w+)", value.get("$ref", ""))
    if not match or len(value) != 1:
        raise ValueError("HTTP message schemas must reference the canonical message catalog")
    return match[1]


def operations(document):
    for path, item in document["paths"].items():
        for method, operation in item.items():
            if method in ("get", "post", "put", "patch", "delete", "head", "options", "trace"):
                yield path, method, operation


def validate(document, root, catalog, fixtures, validator_class, formats):
    source_uri = (root / "api/openapi.yaml").as_uri()
    catalog_uri = (root / "docs/contracts/schemas/messages.schema.json").as_uri()
    prefix = SCHEMA_REF
    if document.get("openapi") != "3.1.2":
        raise ValueError("HTTP contract must use OpenAPI 3.1.2")
    # Preflight every reference before invoking library resolvers. Only this local
    # catalog may be loaded; an added URL must never trigger network retrieval.
    for node in walk(document):
        if not isinstance(node, dict) or "$ref" not in node:
            continue
        reference = node["$ref"]
        if reference.startswith("#/"):
            pointer(document, reference)
        elif reference.startswith(prefix) and reference[len(prefix):] in catalog["$defs"]:
            pass
        else:
            raise ValueError("unapproved or missing OpenAPI reference: " + reference)

    def offline(uri):
        if uri in (catalog_uri, catalog["$id"]):
            return catalog
        raise ValueError("external OpenAPI resource forbidden: " + uri)

    path = SchemaPath.from_dict(document, base_uri=source_uri,
                                handlers={key: offline for key in ("file", "urn", "http", "https", "<all_urls>")})
    OpenAPIV31SpecValidator(path).validate()
    operation_count = 0
    for route, method, operation in operations(document):
        identifier = operation["operationId"]
        operation_count += 1
        if not (route == "/healthz" or route.startswith(("/api/v1/agent/", "/api/v1/admin/"))):
            raise ValueError("HTTP operation outside the documented scope: " + route)
        if not operation.get("summary") or "security" not in operation or "externalDocs" not in operation:
            raise ValueError(identifier + ": summary, explicit security and domain link required")
        for security in operation["security"]:
            if not set(security) <= document["components"]["securitySchemes"].keys():
                raise ValueError(identifier + ": unknown security scheme")
        if "requestBody" in operation:
            body = resolve(document, operation["requestBody"])
            if not body.get("required"):
                raise ValueError(identifier + ": JSON request body must be required")
            model(document, body["content"]["application/json"]["schema"])
        if not any(status.startswith("2") for status in operation["responses"]):
            raise ValueError(identifier + ": missing success response")
        for status, response in operation["responses"].items():
            if not re.fullmatch(r"[1-5][0-9]{2}", status):
                raise ValueError(identifier + ": list exact response statuses")
            response = resolve(document, response)
            if status == "204":
                if "content" in response:
                    raise ValueError(identifier + ": 204 must not declare content")
            else:
                model(document, response["content"]["application/json"]["schema"])

    # Exercise the *OpenAPI reference chain*, including catalog $id scope and
    # nested $defs, with the existing exact-number positive/negative fixtures.
    registry = Registry().with_resources([
        (source_uri, Resource.from_contents(document, default_specification=DRAFT202012)),
        (catalog_uri, Resource.from_contents(catalog)),
        (catalog["$id"], Resource.from_contents(catalog)),
    ])
    sample_count = 0
    for name, schema in document["components"]["schemas"].items():
        name_in_catalog = model(document, schema)
        indirect = validator_class({"$ref": source_uri + "#/components/schemas/" + name},
                                   registry=registry, format_checker=formats)
        for group in ("valid", "invalid"):
            for entry in fixtures[group]:
                if entry["model"] == name_in_catalog:
                    actual = indirect.is_valid(entry["value"])
                    if actual != (group == "valid"):
                        raise ValueError(entry["id"] + ": OpenAPI reference changed message validation")
                    sample_count += 1
    return {"openapi": operation_count, "openapi_samples": sample_count}


def test_index(document):
    """Resolve OpenAPI objects once; Go consumes bindings, not OpenAPI syntax."""
    index = {"generated_from": "api/openapi.yaml; DO NOT EDIT; use --refresh-openapi",
             "headers": {}, "responses": {}, "operations": []}

    def response_id(value, name):
        key = value.get("$ref", name)
        if key not in index["responses"]:
            response = resolve(document, value)
            headers = {}
            for header_name, reference in response.get("headers", {}).items():
                header_key = reference.get("$ref", key + ":" + header_name)
                header = resolve(document, reference)
                index["headers"][header_key] = {"required": header.get("required", False), "schema": header["schema"]}
                headers[header_name] = header_key
            index["responses"][key] = {"model": model(document, response["content"]["application/json"]["schema"])
                                      if response.get("content") else "", "headers": headers}
        return key

    # Global routing errors also apply when no operation matches the request.
    for name in ("NotFound", "MethodNotAllowed"):
        response_id({"$ref": "#/components/responses/" + name}, name)
    for path, method, operation in operations(document):
        pattern = "/".join("(?P<" + part[1:-1] + ">[^/]+)" if part.startswith("{") and part.endswith("}")
                           else re.escape(part) for part in path.split("/"))
        body = resolve(document, operation["requestBody"]) if "requestBody" in operation else None
        parameters = [resolve(document, value) for value in operation.get("parameters", [])]
        index["operations"].append({
            "method": method.upper(), "path": path, "pattern": "^" + pattern + "$",
            "request_model": model(document, body["content"]["application/json"]["schema"]) if body else "",
            "path_models": {p["name"]: model(document, p["schema"]) for p in parameters if p["in"] == "path"},
            "responses": {status: response_id(value, operation["operationId"] + ":" + status)
                          for status, value in operation["responses"].items()},
        })
    return index


def generated_json(document):
    return json.dumps(test_index(document), ensure_ascii=False, indent=2, allow_nan=False) + "\n"


def endpoint_table(document, channel):
    prefix = "/api/v1/" + channel
    lines = ["<!-- BEGIN GENERATED OPENAPI: " + channel + " -->",
             "<!-- Generated from api/openapi.yaml; use --refresh-openapi. -->",
             "| 方法与路径 | 请求 Schema | 成功响应（状态、Schema） |", "|---|---|---|"]
    for path, method, operation in operations(document):
        if not path.startswith(prefix + "/"):
            continue
        request = "无请求体"
        if "requestBody" in operation:
            body = resolve(document, operation["requestBody"])
            request = model(document, body["content"]["application/json"]["schema"])
        success = []
        for status, response in operation["responses"].items():
            if status.startswith("2"):
                response = resolve(document, response)
                success.append(status + ("，无 JSON 正文" if status == "204" else " " + model(document, response["content"]["application/json"]["schema"])))
        lines.append("| " + method.upper() + " " + path[len(prefix):] + " | " + request + " | " + "；".join(success) + " |")
    lines.append("<!-- END GENERATED OPENAPI: " + channel + " -->")
    return "\n".join(lines)


def sync_views(document, root, refresh):
    contracts = root / "docs/contracts"
    destination = root / "api/http.generated.json"
    expected = generated_json(document)
    if refresh:
        destination.write_text(expected, encoding="utf-8", newline="\n")
    elif not destination.is_file() or destination.read_text(encoding="utf-8") != expected:
        raise ValueError("stale http.generated.json; use --refresh-openapi")
    for channel in ("agent", "admin"):
        path = contracts / (channel + ".md")
        text = path.read_text(encoding="utf-8")
        pattern = r"<!-- BEGIN GENERATED OPENAPI: " + channel + r" -->.*?<!-- END GENERATED OPENAPI: " + channel + r" -->"
        if len(re.findall(pattern, text, re.S)) != 1:
            raise ValueError(channel + ".md: expected one generated endpoint table")
        expected = re.sub(pattern, lambda _: endpoint_table(document, channel), text, flags=re.S)
        if refresh:
            path.write_text(expected, encoding="utf-8", newline="\n")
        elif text != expected:
            raise ValueError(channel + ".md: stale endpoint table; use --refresh-openapi")
