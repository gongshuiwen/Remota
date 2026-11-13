"""Regression checks for the contract gate's independent failure modes."""
import copy
import tempfile
import unittest
from pathlib import Path

import openapi_contract as api
from validate_contracts import ContractValidator, FormatChecker, read_json


class OpenAPIContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.root = Path(__file__).resolve().parents[1]
        cls.contracts = cls.root / "docs/contracts"
        cls.document = api.load(cls.root)
        cls.catalog = read_json((cls.contracts / "schemas/messages.schema.json").read_bytes())
        cls.fixtures = read_json((cls.contracts / "acceptance/messages.json").read_bytes())

    def validate(self, document):
        return api.validate(document, self.root, self.catalog, self.fixtures, ContractValidator, FormatChecker())

    def test_yaml_rejects_ambiguous_or_non_json_input(self):
        for text in ("info: {}\ninfo: {}", "responses:\n  200: {}", "value: .nan",
                     "value: 2026-11-12", "value: &v [*v]", "value: !!python/object:object {}"):
            with self.subTest(text=text), self.assertRaises((ValueError, TypeError, api.yaml.YAMLError)):
                api.read_yaml(text)

    def test_yaml_preserves_noncyclic_aliases(self):
        document = api.read_yaml("first: &headers {X-Request-ID: {$ref: '#/components/headers/X-Request-ID'}}\nsecond: *headers")
        self.assertEqual(document["first"], document["second"])

    def test_external_and_missing_refs_fail_before_resolution(self):
        for reference in ("https://example.invalid/schema.json", "../outside.json#/x",
                          api.SCHEMA_REF + "Missing", "#/components/schemas/Missing"):
            document = copy.deepcopy(self.document)
            document["components"]["schemas"]["Empty"] = {"$ref": reference}
            with self.subTest(reference=reference), self.assertRaises((ValueError, KeyError)):
                self.validate(document)

    def test_204_content_is_rejected(self):
        document = copy.deepcopy(self.document)
        response = document["paths"]["/api/v1/admin/logout"]["post"]["responses"]["204"]
        response["content"] = {"application/json": {"schema": {"$ref": "#/components/schemas/Empty"}}}
        with self.assertRaisesRegex(ValueError, "204 must not"):
            self.validate(document)

    def test_generated_views_detect_drift_and_refresh_from_yaml(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "api").mkdir()
            contracts = root / "docs/contracts"
            contracts.mkdir(parents=True)
            for channel in ("agent", "admin"):
                (contracts / (channel + ".md")).write_text(
                    "preserved introduction\n" + api.endpoint_table(self.document, channel) + "\npreserved rules\n",
                    encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "stale http.generated.json"):
                api.sync_views(self.document, root, False)
            api.sync_views(self.document, root, True)
            api.sync_views(self.document, root, False)
            path = contracts / "agent.md"
            path.write_text(path.read_text(encoding="utf-8").replace("201 Registration", "200 Registration"), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "stale endpoint table"):
                api.sync_views(self.document, root, False)
            api.sync_views(self.document, root, True)
            text = path.read_text(encoding="utf-8")
            self.assertTrue(text.startswith("preserved introduction\n"))
            self.assertTrue(text.endswith("\npreserved rules\n"))

    def test_schema_ref_siblings_cannot_create_a_second_message_contract(self):
        schema = {"$ref": "#/components/schemas/Empty", "additionalProperties": True}
        with self.assertRaisesRegex(ValueError, "must only reference"):
            api.model(self.document, schema)

    def test_index_resolves_nested_refs_and_preserves_http_bindings(self):
        index = api.test_index(self.document)
        document = copy.deepcopy(self.document)
        for kind, name in (("responses", "BadRequest"), ("headers", "X-Remota-Epoch"), ("parameters", "sid"), ("schemas", "Empty")):
            entries = document["components"][kind]
            entries[name + "Base"] = entries[name]
            entries[name] = {"$ref": "#/components/" + kind + "/" + name + "Base"}
        self.assertEqual(api.test_index(document), index)
        operation = next(op for op in index["operations"] if op["method"] == "POST" and op["path"] == "/api/v1/agent/sessions")
        self.assertEqual(operation["request_model"], "Empty")
        self.assertEqual(index["responses"][operation["responses"]["201"]]["model"], "Registration")
        health = next(op for op in index["operations"] if op["path"] == "/healthz")
        self.assertEqual(index["responses"][health["responses"]["503"]]["model"], "HealthResponse")

    def test_raw_multifile_entry_is_standard_openapi(self):
        entry = self.root / "api/openapi.yaml"
        resources = {path.as_uri(): api.read_yaml(path.read_text(encoding="utf-8"))
                     for path in (self.root / "api").rglob("*.yaml")}
        invalid_urls = [node["externalDocs"]["url"] for resource in resources.values()
                        for node in api.walk(resource)
                        if isinstance(node, dict) and "externalDocs" in node
                        and not node["externalDocs"]["url"].isascii()]
        self.assertFalse(invalid_urls, "externalDocs.url must percent-encode non-ASCII characters")
        resources[(self.contracts / "schemas/messages.schema.json").as_uri()] = self.catalog
        resources[self.catalog["$id"]] = self.catalog
        path = api.SchemaPath.from_dict(resources[entry.as_uri()], base_uri=entry.as_uri(),
                                        handlers={key: resources.__getitem__ for key in ("file", "urn", "http", "https", "<all_urls>")})
        api.OpenAPIV31SpecValidator(path).validate()

    def test_source_loader_rejects_escapes_cycles_and_invalid_parts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            folder = root / "api"
            folder.mkdir()
            entry = folder / "openapi.yaml"
            part = folder / "part.yaml"
            (root / "outside.yaml").write_text("item: {}\n", encoding="utf-8")
            for reference, body in (("https://example.invalid/api.yaml#/item", "item: {}"),
                                    ("../outside.yaml#/item", "item: {}"),
                                    ("./part.yaml#/missing", "item: {}"),
                                    ("./part.yaml#/item", "item: {x: 1, x: 2}"),
                                    ("./part.yaml#/item", "item: {$ref: './part.yaml#/item'}")):
                entry.write_text("child:\n  $ref: '" + reference + "'\n", encoding="utf-8")
                part.write_text(body + "\n", encoding="utf-8")
                with self.subTest(reference=reference, body=body), self.assertRaises((ValueError, KeyError)):
                    api.load(root)

    def test_source_refs_are_pure_instead_of_merging_annotations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            folder = root / "api"
            folder.mkdir()
            (folder / "parameters.yaml").write_text("sid: {name: sid, in: path, required: true, schema: {type: string}}\n", encoding="utf-8")
            for reference in ("./parameters.yaml#/sid", "#/components/parameters/sid"):
                for field in ("summary", "description"):
                    (folder / "openapi.yaml").write_text(
                        "components:\n  parameters:\n    sid:\n      $ref: ./parameters.yaml#/sid\n"
                        "child:\n  $ref: '" + reference + "'\n  " + field + ": override\n", encoding="utf-8")
                    with self.subTest(reference=reference, field=field), self.assertRaisesRegex(ValueError, "pure.*\\$ref"):
                        api.load(root)


if __name__ == "__main__":
    unittest.main()
