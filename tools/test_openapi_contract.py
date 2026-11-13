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
        cls.contracts = Path(__file__).resolve().parents[1] / "docs/contracts"
        cls.document = api.read_yaml((cls.contracts / "openapi.yaml").read_text(encoding="utf-8"))
        cls.catalog = read_json((cls.contracts / "schemas/messages.schema.json").read_bytes())
        cls.fixtures = read_json((cls.contracts / "acceptance/messages.json").read_bytes())

    def validate(self, document):
        return api.validate(document, self.contracts, self.catalog, self.fixtures, ContractValidator, FormatChecker())

    def test_yaml_rejects_ambiguous_or_non_json_input(self):
        for text in ("info: {}\ninfo: {}", "responses:\n  200: {}", "value: .nan",
                     "value: 2026-11-12", "value: &v [*v]", "value: !!python/object:object {}"):
            with self.subTest(text=text), self.assertRaises((ValueError, TypeError, api.yaml.YAMLError)):
                api.read_yaml(text)

    def test_external_and_missing_refs_fail_before_resolution(self):
        for reference in ("https://example.invalid/schema.json", "../outside.json#/x",
                          "./schemas/messages.schema.json#/$defs/Missing", "#/components/schemas/Missing"):
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
            contracts = Path(directory)
            for channel in ("agent", "admin"):
                (contracts / (channel + ".md")).write_text(
                    "preserved introduction\n" + api.endpoint_table(self.document, channel) + "\npreserved rules\n",
                    encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "stale http.generated.json"):
                api.sync_views(self.document, contracts, False)
            api.sync_views(self.document, contracts, True)
            api.sync_views(self.document, contracts, False)
            path = contracts / "agent.md"
            path.write_text(path.read_text(encoding="utf-8").replace("201 Registration", "200 Registration"), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "stale endpoint table"):
                api.sync_views(self.document, contracts, False)
            api.sync_views(self.document, contracts, True)
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


if __name__ == "__main__":
    unittest.main()
