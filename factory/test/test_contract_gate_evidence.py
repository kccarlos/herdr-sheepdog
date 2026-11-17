import home_guard  # noqa: F401  (first: every test on a throwaway home, never the real one)
import json, os, re, unittest
from contract_gate_evidence import validate_review_run, validate_review, validate_check, validate_verdict

DOC = open(os.path.join(os.path.dirname(__file__), "..", "contracts", "gate-evidence.md")).read()
CASES = {"review_runs": validate_review_run, "reviews": validate_review, "checks": validate_check,
         "verdicts": validate_verdict}


def example(name):
    section = DOC.split(f'`t["{name}"][]`')[1]
    return json.loads(re.search(r"```json\n(.*?)\n```", section, re.S).group(1))


class GateEvidence(unittest.TestCase):
    def test_examples_pass(self):
        for name, validate in CASES.items():
            validate(example(name))

    def test_malformed_fail(self):
        def bad(name, **change):
            r = {**example(name), **change}
            with self.assertRaises(ValueError, msg=f"{name} {change}"):
                CASES[name](r)
        bad("review_runs", head="abc")
        bad("reviews", verdict="ok")
        bad("reviews", started_by="worker")
        bad("reviews", must_fix=2)
        bad("review_runs", round=0)
        bad("review_runs", model="")
        bad("reviews", must_fix="0")
        bad("reviews", report="x" * 2001)
        bad("checks", exit=True)
        bad("checks", output_tail="x" * 4001)
        bad("checks", ci={"state": "green", "url": "u"})
        bad("verdicts", commands="ls")
        bad("verdicts", extra=1)

    def test_edge_cases(self):
        validate_review_run({**example("review_runs"), "model": None})
        with self.assertRaises(ValueError):
            validate_verdict({1: 2})
        r = example("reviews"); del r["started_by"]
        with self.assertRaises(ValueError):
            validate_review(r)
        validate_review({**example("reviews"), "verdict": "changes", "must_fix": 2})
        validate_check({**example("checks"), "ci": {"state": "none", "url": None}})
        validate_check({**example("checks"), "ci": None})
        with self.assertRaises(ValueError):
            validate_check({k: v for k, v in example("checks").items() if k != "ci"})


if __name__ == "__main__":
    unittest.main()
