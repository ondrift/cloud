#!/usr/bin/env python3
"""loop_check_test.py - the loop controller, held to its own word.

    python3 sdk/conformance/loop_check_test.py

loop_check.py decides whether a language's program serves the loop a slice's
warm worker speaks. A controller that cannot fail proves nothing, so this runs
it against loop_fake.py, a program that is a correct loop with no defect and
breaks exactly one rule with each, and asserts that the controller

  - passes the correct loop,
  - fails every defect, on the case that states the rule it breaks and on no
    other,
  - fails a language that is on the pending list when it passes, and says to
    take it off,
  - reports a pending language that still fails without failing the run, and
  - fails a language that is not on the pending list when it fails.

The cases themselves, and the lists, are in expected.json under "$loop".
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
CONTROLLER = os.path.join(HERE, "loop_check.py")
FAKE = os.path.join(HERE, "loop_fake.py")
EXPECTED = os.path.join(HERE, "expected.json")

# Short, so a program that never answers costs the test a second and a half and
# not the controller's default. A fake answers in milliseconds.
REPLY_TIMEOUT = "1.5"

ONE_SHOT_SPAWN = "loop_one_shot_spawn"
EOF_ENDS_THE_PROCESS = "loop_eof_ends_the_process"
TMPDIR_AND_HOME = "loop_tmpdir_and_home_are_the_envelopes"
SECRET_REPLACED = "loop_secret_is_what_the_envelope_carries"
SECRET_REMOVED = "loop_secret_removed_when_not_carried"
PREVIOUS_FOLLOWS = "loop_previous_secret_follows_the_same_rule"
PRINTED_LINE = "loop_a_printed_line_reaches_stderr_only"
RSS_BYTES = "loop_reply_carries_rss_bytes"
FAILING_HANDLER = "loop_a_failing_handler_ends_the_process"


# GUARD: the controller does not exist yet, so these tests are skipped until it
# does. The commit that adds loop_check.py (task #3550) deletes this and the
# decorators below.
needs_controller = unittest.skipUnless(
    os.path.exists(CONTROLLER), "loop_check.py is added by task #3550, which removes this guard"
)


def load_expected():
    with open(EXPECTED) as f:
        return json.load(f)


def loop_cases():
    """The cases a run on this platform asserts: all but those limited to another."""
    cases = load_expected().get("$loop", {}).get("cases", {})
    return [n for n, c in cases.items() if sys.platform.startswith(c.get("only_on", ""))]


def run_controller(language, *, defect=None, pending=(), not_applicable=(), program=True):
    """Run the controller against the fake and return (exit status, output).

    The lists in a temporary copy of expected.json are replaced by the ones the
    test names, so a fake language can be pending or not applicable without the
    real lists changing.
    """
    expected = load_expected()
    loop = expected.setdefault("$loop", {})
    loop["pending"] = {name: "the fake is on the pending list" for name in pending}
    loop["not_applicable"] = {name: "the fake does not serve this way" for name in not_applicable}
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "expected.json")
        with open(path, "w") as f:
            json.dump(expected, f)
        cmd = [sys.executable, CONTROLLER, language, "--expected", path, "--reply-timeout", REPLY_TIMEOUT]
        if program:
            cmd += ["--", sys.executable, FAKE]
            if defect:
                cmd += ["--defect", defect]
        done = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
    return done.returncode, done.stdout + done.stderr


def failed_cases(output):
    """The cases the controller reported as failing, in the order it named them."""
    return re.findall(r"^\s+\u2717 (\S+)", output, re.MULTILINE)


@needs_controller
class TheControllerPassesALoop(unittest.TestCase):
    def test_a_correct_loop_passes_every_case(self):
        status, output = run_controller("fake")
        self.assertEqual(status, 0, output)
        self.assertEqual(failed_cases(output), [], output)
        cases = len(loop_cases())
        self.assertGreater(cases, 0, "expected.json holds no loop cases")
        self.assertRegex(output, r"%d/%d cases pass" % (cases, cases), output)


@needs_controller
class TheControllerFailsABrokenLoop(unittest.TestCase):
    def assert_fails_exactly(self, defect, cases):
        status, output = run_controller("fake", defect=defect)
        self.assertEqual(status, 1, "%s did not fail the run:\n%s" % (defect, output))
        self.assertEqual(sorted(failed_cases(output)), sorted(cases), "%s:\n%s" % (defect, output))
        return output

    def test_a_one_shot_program_fails_every_case_but_the_one_shot_spawn(self):
        everything_else = [c for c in loop_cases() if c != ONE_SHOT_SPAWN]
        self.assertTrue(everything_else)
        self.assert_fails_exactly("one-shot", everything_else)

    def test_k_envelopes_answered_by_one_reply_fail(self):
        everything_else = [c for c in loop_cases() if c != ONE_SHOT_SPAWN]
        output = self.assert_fails_exactly("one-reply", everything_else)
        self.assertIn("envelope 2: no reply within", output)

    def test_a_final_envelope_without_a_newline_must_be_answered(self):
        self.assert_fails_exactly("needs-newline", [ONE_SHOT_SPAWN])

    def test_tmpdir_and_home_must_follow_every_envelope(self):
        self.assert_fails_exactly("tmpdir-once", [TMPDIR_AND_HOME])

    def test_home_must_follow_the_envelope_too(self):
        self.assert_fails_exactly("home-untouched", [TMPDIR_AND_HOME])

    def test_a_secret_is_the_one_the_envelope_carries(self):
        self.assert_fails_exactly("secret-not-replaced", [SECRET_REPLACED])

    def test_a_secret_that_stays_after_an_envelope_drops_it_fails(self):
        self.assert_fails_exactly("secret-stays", [SECRET_REMOVED])

    def test_a_previous_secret_that_stays_fails(self):
        self.assert_fails_exactly("previous-stays", [PREVIOUS_FOLLOWS])

    def test_a_printed_line_on_the_response_stream_fails(self):
        self.assert_fails_exactly("print-to-stdout", [PRINTED_LINE])

    @unittest.skipUnless(sys.platform.startswith("linux"), "rss_bytes is asserted on Linux")
    def test_a_reply_without_rss_bytes_fails(self):
        self.assert_fails_exactly("no-rss", [RSS_BYTES])

    def test_an_eof_that_does_not_end_the_process_fails(self):
        self.assert_fails_exactly("eof-hangs", [EOF_ENDS_THE_PROCESS, ONE_SHOT_SPAWN])

    def test_an_eof_that_ends_the_process_with_a_status_fails(self):
        self.assert_fails_exactly("eof-fails", [EOF_ENDS_THE_PROCESS, ONE_SHOT_SPAWN])

    def test_a_handler_failure_that_leaves_the_process_alive_fails(self):
        output = self.assert_fails_exactly("panic-survives", [FAILING_HANDLER])
        self.assertIn("still running", output)

    def test_a_handler_failure_that_is_answered_fails(self):
        self.assert_fails_exactly("panic-answers", [FAILING_HANDLER])


@needs_controller
class ThePendingListCannotRot(unittest.TestCase):
    def test_a_pending_language_that_passes_fails_the_run_and_says_to_remove_it(self):
        status, output = run_controller("fake", pending=["fake"])
        self.assertEqual(status, 1, output)
        self.assertIn("pending list", output)
        self.assertRegex(output, r"[Rr]emove")

    def test_a_pending_language_that_still_fails_is_reported_and_does_not_fail_the_run(self):
        status, output = run_controller("fake", defect="one-shot", pending=["fake"])
        self.assertEqual(status, 0, output)
        self.assertIn("PENDING", output)

    def test_a_language_off_the_list_that_fails_fails_the_run(self):
        status, output = run_controller("fake", defect="one-shot", pending=["another"])
        self.assertEqual(status, 1, output)
        self.assertNotIn("PENDING", output)


@needs_controller
class TheListsAreHonest(unittest.TestCase):
    def test_a_language_that_does_not_serve_the_loop_is_not_run(self):
        status, output = run_controller("fake", not_applicable=["fake"], program=False)
        self.assertEqual(status, 0, output)
        self.assertIn("not applicable", output)
        self.assertIn("the fake does not serve this way", output)

    def test_a_language_with_a_loop_and_no_program_is_an_error(self):
        status, output = run_controller("fake", program=False)
        self.assertEqual(status, 2, output)
        self.assertIn("no program", output)

    def test_every_language_is_named_once_and_go_must_pass(self):
        loop = load_expected()["$loop"]
        languages = {"go", "python", "node", "ruby", "php", "rust"}
        not_applicable, pending = set(loop["not_applicable"]), set(loop["pending"])
        self.assertEqual(not_applicable & pending, set(), "a language is both not applicable and pending")
        self.assertLessEqual(not_applicable | pending, languages)
        self.assertNotIn("go", not_applicable | pending, "Go is the reference loop and passes")
        self.assertEqual(not_applicable, {"python", "node"})
        for lists in (loop["not_applicable"], loop["pending"]):
            for name, reason in lists.items():
                self.assertTrue(reason.strip(), "%s is listed without a reason" % name)

    def test_every_case_states_why_and_what_it_expects(self):
        for name, case in load_expected()["$loop"]["cases"].items():
            self.assertTrue(name.startswith("loop_"), name)
            self.assertTrue(case.get("why", "").strip(), "%s has no why" % name)
            self.assertIn("expect", case, "%s has no expect" % name)


if __name__ == "__main__":
    unittest.main()
