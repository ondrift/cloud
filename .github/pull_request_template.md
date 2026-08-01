<!--
  Branch prefix decides the release. The PR check fails on anything else.

    feature/…   minor bump      bugfix/…   patch bump
    breaking/…  major bump      chore/ docs/ ci/ test/   no release

  Fill all three sections. They are the record of what shipped and why —
  the commit log says how, this says what and whether it works.
-->

## What this implements

<!--
  Prose, for a human. What changed and why it needed changing — not a
  restatement of the diff. If it fixes something, say what was broken and how
  it showed up, not just the symptom.
-->

## What was tested

<!--
  The specific behaviours you pinned, not "added tests". Name the ones that
  would catch a real regression, and say what you did NOT cover.
  If a bug is being fixed: confirm the test failed before the fix.
-->

## Test results

<!--
  Paste them. CI writes build/vet/test output to the job summary — copy the
  relevant part here so the PR is self-contained and still readable once the
  run has been garbage-collected.
-->

```
```

---

- [ ] Branch prefix matches the intended version bump
- [ ] `cli/cmd/drift/main.go`'s `version` var is **untouched** (CI sets the version from the tag)
- [ ] SDK only: all six manifests carry the same version
