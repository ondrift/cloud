#!/usr/bin/env python3
"""probe_check_test.py - the probe controller, held to its own word.

    python3 sdk/conformance/probe_check_test.py

probe_check.py decides whether a language's program answers the start check the
way a slice needs. A controller that cannot fail proves nothing, so this runs it
against probe_fake.py, a program that answers correctly and breaks exactly one
rule with each defect, and asserts that the controller

  - passes the correct program on every case,
  - fails every defect on the cases that state the rule it breaks and on no
    other (a program that answers one entry and not the others fails the others),
  - fails a program that answers the check when it was not asked, or never
    reaches its handler when it is not, on the control cases,
  - ends a program that never exits at the deadline and says so, and
  - refuses a language with no program, and one whose program cannot be run.

The cases themselves are in expected.json under "$probe".
"""

import json
import os
import re
import subprocess
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
CONTROLLER = os.path.join(HERE, "probe_check.py")
FAKE = os.path.join(HERE, "probe_fake.py")
EXPECTED = os.path.join(HERE, "expected.json")

# Short, so a program that never exits costs the test two seconds and not the
# platform's ten. A fake answers in milliseconds; the margin is for a busy runner.
DEADLINE = "2"

PROBES = ["probe_run", "probe_run_sse", "probe_run_ws"]
CONTROLS = ["probe_control_run", "probe_control_run_sse", "probe_control_run_ws"]


def load_cases():
    with open(EXPECTED) as f:
        return json.load(f)["$probe"]["cases"]


def run_controller(*, defect=None, program=True, program_argv=None):
    """Run the controller against the fake and return (exit status, output)."""
    cmd = [sys.executable, CONTROLLER, "fake", "--deadline", DEADLINE]
    if program_argv is not None:
        cmd += ["--"] + program_argv
    elif program:
        cmd += ["--", sys.executable, FAKE]
        if defect:
            cmd += ["--defect", defect]
    done = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
    return done.returncode, done.stdout + done.stderr


def failed_cases(output):
    """The cases the controller reported as failing, in the order it named them."""
    return re.findall(r"^\s+\u2717 (\S+)", output, re.MULTILINE)


class TheControllerPassesACorrectProgram(unittest.TestCase):
    def test_a_program_that_answers_passes_every_case(self):
        status, output = run_controller()
        self.assertEqual(status, 0, output)
        self.assertEqual(failed_cases(output), [], output)
        cases = len(load_cases())
        self.assertGreater(cases, 0, "expected.json holds no probe cases")
        self.assertRegex(output, r"%d/%d cases pass" % (cases, cases), output)


class TheControllerFailsAProgramThatBreaksTheCheck(unittest.TestCase):
    def assert_fails_exactly(self, defect, cases):
        status, output = run_controller(defect=defect)
        self.assertEqual(status, 1, "%s did not fail the run:\n%s" % (defect, output))
        self.assertEqual(sorted(failed_cases(output)), sorted(cases), "%s:\n%s" % (defect, output))
        return output

    def test_a_program_that_says_nothing_fails_the_check(self):
        self.assert_fails_exactly("no-hello", PROBES)

    def test_a_program_that_dies_reading_its_request_fails_the_check(self):
        self.assert_fails_exactly("decode-error", PROBES)

    def test_a_program_that_reaches_its_handler_fails_the_check(self):
        output = self.assert_fails_exactly("calls-handler", PROBES)
        self.assertIn('"handler_called": true', output)

    def test_a_hello_with_spaces_in_it_fails(self):
        self.assert_fails_exactly("spaced", PROBES)

    def test_a_hello_without_its_newline_fails(self):
        self.assert_fails_exactly("no-newline", PROBES)

    def test_a_line_after_the_hello_fails(self):
        self.assert_fails_exactly("extra-line", PROBES)

    def test_a_hello_at_another_version_fails(self):
        self.assert_fails_exactly("wrong-version", PROBES)

    def test_a_hello_on_stderr_fails(self):
        self.assert_fails_exactly("hello-on-stderr", PROBES)

    def test_an_exit_status_that_is_not_zero_fails(self):
        output = self.assert_fails_exactly("exit-nonzero", PROBES)
        self.assertIn('"exit": 3', output)

    def test_a_program_that_never_exits_is_ended_at_the_deadline_and_fails(self):
        output = self.assert_fails_exactly("hangs", PROBES)
        self.assertIn("still running after %ss" % DEADLINE, output)

    def test_an_entry_that_does_not_answer_fails_that_entry_alone(self):
        self.assert_fails_exactly("only-run", ["probe_run_sse", "probe_run_ws"])


class TheControlsCanFail(unittest.TestCase):
    """Without them, a program that never reaches its handler passes the probe cases by saying nothing."""

    def assert_fails_exactly(self, defect, cases):
        status, output = run_controller(defect=defect)
        self.assertEqual(status, 1, "%s did not fail the run:\n%s" % (defect, output))
        self.assertEqual(sorted(failed_cases(output)), sorted(cases), "%s:\n%s" % (defect, output))

    def test_a_program_that_answers_a_start_nobody_asked_for_fails_the_controls(self):
        self.assert_fails_exactly("answers-everything", CONTROLS)

    def test_a_program_whose_handler_cannot_be_reached_fails_the_controls(self):
        self.assert_fails_exactly("never-reaches-handler", CONTROLS)


class TheLanguageMustHaveAProgram(unittest.TestCase):
    def test_a_language_with_no_program_is_an_error_not_a_skip(self):
        status, output = run_controller(program=False)
        self.assertEqual(status, 2, output)
        self.assertIn("no program", output)

    def test_a_program_that_cannot_be_run_fails_every_case(self):
        status, output = run_controller(program_argv=[os.path.join(HERE, "no-such-program")])
        self.assertEqual(status, 1, output)
        self.assertEqual(sorted(failed_cases(output)), sorted(load_cases()), output)
        self.assertIn("could not be run", output)


class TheCasesAreHonest(unittest.TestCase):
    def test_every_case_states_why_and_what_it_expects(self):
        for name, case in load_cases().items():
            self.assertTrue(name.startswith("probe_"), name)
            self.assertTrue(case.get("why", "").strip(), "%s has no why" % name)
            self.assertIn("expect", case, "%s has no expect" % name)
            self.assertIn(case.get("entry"), ("run", "run_sse", "run_ws"), name)

    def test_every_entry_has_a_probe_case_and_a_control(self):
        cases = load_cases()
        for entry in ("run", "run_sse", "run_ws"):
            probing = [c for c in cases.values() if c["entry"] == entry and c.get("invoke") == "probe1"]
            controls = [c for c in cases.values() if c["entry"] == entry and c.get("invoke") is None]
            self.assertEqual(len(probing), 1, "%s needs one probe case" % entry)
            self.assertEqual(len(controls), 1, "%s needs one control" % entry)
            self.assertTrue(probing[0]["expect"]["stdout"].endswith("\n"))
            self.assertEqual(probing[0]["expect"]["exit"], 0)
            self.assertFalse(probing[0]["expect"]["handler_called"])
            self.assertTrue(controls[0]["expect"]["handler_called"])


if __name__ == "__main__":
    unittest.main()
