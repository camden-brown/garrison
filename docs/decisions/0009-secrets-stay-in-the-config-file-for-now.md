# 0009 — Secrets stay in the config file, and the form will not type one

**Status:** Accepted · 2026-09-09

## Context

The settings form can now edit text, which raises a question it could previously
duck: where does a password go when somebody types one into it?

`games.TypeSecret` existed from M3 and its doc comment claimed a secret was
"never rendered, never written to the config file". The first half was true —
the form shows `••••••••`. The second half was enforced by nothing. Nothing in
`internal/config` or the apply path filters a secret out on save, so a password
in a draft would have been written to `servers/<name>.toml` in plain text next
to the port numbers.

That was harmless only because no password could reach a draft: without a text
input, `TypeSecret` fields were display-only. Adding the input removes the
accident that was keeping the claim true.

README describes the intended answer — Windows Credential Manager under
`garrison/<instance>/<key>`, which is what makes the config directory safe to
sync. It is not built.

## Decision

`TypeSecret` fields are not editable in the settings form. A password is
changed by editing the server's TOML file, by someone who can see they are
writing a secret into a file.

The type's doc comment now says what actually happens rather than what was
intended, and the plugin schema's help text for a password says where it lives.

## Consequences

- Valheim's `ServerPass` is set by hand, the same way it was before the form
  could edit text at all. Nothing regressed; a promise was withdrawn.
- The config directory is **not** safe to sync or commit, contrary to README.
  That sentence describes the Credential Manager design, not the code.
- `editable()` in the settings view is the single place the rule lives, so
  reversing this is one predicate and a test.
- Credential Manager becomes a piece of work with a visible reason to exist,
  rather than a footnote inside a form change. It is the first thing in the
  project that would be genuinely Windows-only — the fake `host.Driver` cannot
  stand in for it — so it needs a build-tagged fallback for CI and WSL, which
  is the design question this decision defers rather than answers.

## Rejected

**Let the form edit secrets and write them to the TOML.** One predicate
simpler, and it makes a security claim false in the one direction that matters:
a user who sees a masked field reasonably concludes the value is stored masked.
A password that is plainly a file edit is safer than one the UI implies is
protected.

**Implement Credential Manager now, as part of this change.** It is the right
end state and the wrong moment: it is a platform decision with a CI story and a
fallback design, and bolting it onto a text-input change would get it decided
by whoever was closest to the keyboard.

## How we would know this was wrong

If people set passwords often enough that hand-editing TOML becomes the normal
way to use Garrison, the form is not the settings screen it claims to be, and
the deferral has become the design. Mitigation: this is the only field type the
form refuses, and the refusal is one predicate wide.
