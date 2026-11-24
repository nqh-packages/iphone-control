# Synthesis: edge-case autonomy (architect, Opus + Gemini 4 Neon)

Sources: /tmp/ic-design2/opus.md (primary shape) and /tmp/ic-design2/gemini.md.
Both converge on: one declarative book of "interrupts" (data, Apple strings only in data) consulted at the gateway's read points; one-time codes read from the RAW screen into a per-lease in-memory slot (never to agent output, logs or disk) and typed by the gateway; split-box entry by key events with a read-back barrier before the LAST character; instant refusals instead of 60 s Ask hangs; ledger of rejected codes.

## Chosen shape (Opus structure, Gemini additions)
- Interrupt book: kinds dismiss / consent / credential / report; consent and credential entries need `-expect NAME` declared by the run (so "Allow" on a sign-in request fires only for a sign-in the agent started). Rejected Gemini's always-on guard for consent actions.
- One value-reference grammar in phone_type: {{secret:NAME}}, {{account:NAME}}, {{code}}.
- Code detector shared by the masker and the reader (`policy.FindCodes`): number-in-label AND a row of one-digit elements. This also closes today's masking gap (split digits are NOT masked now, though mask_codes is ON by default; the owner's setting is on, the detector is the gap).
- Entry: TypeOptions.ByKeys (key events, not pasteboard paste), WDA software-keypad fallback (Gemini), read back boxes 0..N-2 before sending digit N; on mismatch retry once in the other mode, then stop without submitting. Never retype a rejected code. Resend invalidates older codes.
- Ledger: counts only, no digits; cap 2 rejected codes per account per 24 h (guess, labelled as such).
- Gates (no 60 s hangs): Erase All Content and Settings = refuse (instant); Delete Account = refuse; Turn Passcode Off = refuse; Payments: free "Get" / $0 = allow, nonzero = refuse (nobody answers an Ask); Sign Out = refuse until device probe P5 proves sign-in back with no person. Each overridable per gate in rules.yaml (`guards:`).
- Recovery table: as Opus section 7 + Gemini's transport fallback on read paths.

## Facts corrected
- Opus claimed fill/secret typing is refused while Hide codes is off. Hide codes is ON by default (HideCodes() returns true when mask_codes unset), and `phone fill -account` worked live. The claim is moot; no owner action needed.
- Open owner question from Opus (read of the agents' login moved into the daemon) is NOT taken: `{{account:}}` is deferred; `phone fill -account` stays as is.

## Slices (each accepted on its own tests; device probes by the orchestrator)
1. THIS SLICE: policy.FindCodes + masking of digit rows; code slot + `{{code}}`; split-box group entry by keys with N-1 read-back; TypeOptions.ByKeys; seam rows; tests.
2. Interrupt book + clearInterrupts + `-expect` + `phone interrupts match`.
3. Gates: `guards:` map, free-install refinement, instant refusals.
4. Recovery: read-path link fallback.

Device probes (orchestrator, later): P1 whether Safari boxes take key events; code lifetime; P5 sign-out recovery.
