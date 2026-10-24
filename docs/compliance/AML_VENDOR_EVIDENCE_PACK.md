# Signed-vendor evidence pack — closing G2 and G9

**What this is.** The register in `docs/compliance/CBN_AML_COMPLIANCE_SPEC.md` §5 is closed
everywhere the *code* can close a gap; the two screening lanes remain
register-closed on shipped seams, and the screening *purchase* is what
turns those seams into a live, list-proven vendor lane:

| Gap | Status | What proves the live lane |
|---|---|---|
| **G2** — payment-time counterparty screening | register-closed on the shipped seam: simulated default + Dojah adapter wired behind `ports.PaymentScreener` (`PAYMENT_SCREENING_PROVIDER=dojah`), list version recorded (migration 098), holds re-evaluated on list delta (§8(5)) | the §7 trial (T1–T7, T4/T5 decisive) + executed contract/DPA |
| **G9** — adverse media + domestic lists | register-closed on the shipped seam: simulated adapter behind `ports.AdverseMediaScreener` (migration 097); no vendor adapter is wired here yet, so list breadth and match quality are unproven | the §7 trial incl. **T5 (NSC coverage)** + written provenance |

This is the checklist an operator works through so those two lanes carry
evidence, not optimism. It pairs with `docs/compliance/AML_VENDOR_DECISION_RECORD.md` §7 (the
trial criteria) and §8 (the integration seams). Gate 4 is the engineering
commit list, and it has shipped with the code.

**Standing rule:** a gap flips to closed only when its *evidence cell* can be
filled with an artifact in this pack. Until then the register stays open — the
`TestAMLGapRegister*` drift guard will fail if the ids change without the doc.

---

## Gate 1 — pre-signature trial (vendor record §7, re-stated as pass/fail)

Run against a pre-production account, on our own load profile, before any
commitment. Each row is disqualifying on its own except where noted.

| # | Test | Pass condition | Evidence to keep |
|---|---|---|---|
| T1 | Payment-screening latency | p99 ≤ 500 ms warm at peak; hard deadline ≤ 1 s | load-test report with percentiles and concurrency |
| T2 | Degradation policy | timeout/5xx can be configured fail-closed outbound, flagged-open inbound | written confirmation + a forced-failure test |
| T3 | Hit-level detail | verdict **plus** hits with score, entity type, source list, list version | sample payload |
| T4 | Nigerian name matching | false-positive rate acceptable on our prepared Yoruba/Igbo/Hausa/transliteration set at our threshold | FP report over the name set |
| T5 | Domestic list coverage | **Nigeria Sanctions Committee (NSC)** list under TPPA 2022, plus CBN/NFIU designations, carried alongside OFAC/UN/EU/UK | written coverage confirmation — *this alone is fatal if it fails* |
| T6 | Commercial floor | entry contract starts at our volume without a bank-scale annual commitment | quote with per-search **and** per-transaction curves |
| T7 | List freshness | deltas arrive with a version identifier we can persist and reference | sample delta + version header |

Two or more failures stops the field and re-opens the vendor decision record
§4.

## Gate 2 — signature-day contract items

| Item | Why | Anchor |
|---|---|---|
| Executed contract + order form | the decision becomes a fact | vendor record §6 |
| **DPA carrying NDPA cross-border terms** | screening routes identity data abroad; the spec's §6.1 leaves this as a procurement gate | spec §6.1 |
| List-version provenance in the API contract | a disposition is only defensible later if the list version is recorded | `screening_results.list_version` / `payment_screenings.list_version` (migration 098; §8(1)) |
| **Bulk re-screen export clause** | neither finalist sells its lists as a dataset, so switching later means re-screening the whole book | vendor record §9 |
| SLA: availability, latency, support escalation | T2's degradation policy needs a counterparty obligation | vendor record §7(2) |
| Notification duty on list changes | G2's open holds must re-evaluate when a list version moves | vendor record §8(5) |

## Gate 3 — evidence artifacts collected

Filed with the compliance record so a reviewer can open them:

1. the T1–T7 results above, dated and signed by the tester;
2. the written domestic-list coverage confirmation (T5);
3. the executed contract and DPA;
4. the sample hit payload (T3) and a delta sample (T7);
5. the trial's false-positive report (T4) — this is also the baseline for the
   tuning loop (`TUNING_FP_PERCENT`, `TUNING_MIN_LABELLED`);
6. the vendor's list-version identification scheme, so
   `screening_results.list_version` has a defined vocabulary.

## Gate 4 — the code-side flip (engineering commit list; shipped)

Work items, in order — each tick is literal: the artifact exists in the
repo. The purchase cutovers remain gated on Gates 1–3.

- [x] **Customer screening adapter** — `internal/providers/screening/dojah.go`
      wires Dojah's `/api/v2/aml/screening` behind `ports.SanctionsScreener`
      (`SCREENING_PROVIDER=dojah`), mapping ranked hits into the port's decision
      vocabulary; httptest suite in `internal/providers/screening/dojah_test.go`.
- [x] **Payment screening adapter** — `internal/providers/paymentscreening/dojah.go`
      behind `ports.PaymentScreener` (`PAYMENT_SCREENING_PROVIDER=dojah`);
      the `simulated` default stays for tests, so callers do not change.
- [x] **List version recorded** — migration 098 adds `list_version` to
      `screening_results` and `payment_screenings`; both store inserts write it
      (adverse_media_screenings already had it, migration 097); the
      `/admin/payment-screenings` queue and the DSR export show it so a later
      reviewer knows which list produced the match.
- [x] **Open-hold re-evaluation** — the rescreen worker (`RescreenDue`, app)
      re-screens open-hold payout screenings whose recorded list version
      predates the newest one on the rail via
      `store.HeldScreeningsOnStaleList` + `store.ReevaluateHeldPayout`
      (vendor record §8(5)); a now-clear counterparty is released, a blocked
      one is interdicted, an ambiguous one stays held with refreshed detail.
- [x] **`.env.example` truthfulness** — the Dojah provider value, endpoints and
      keys documented because the controls now exist (M0.1): `DOJAH_*` block +
      `_{IDENTITY,AML,LIVENESS,DOC_AUTH}.PROVIDER=dojah` values.
- [x] **Tests** — Dojah adapter mapping suites for all four wired lanes plus a
      payment-screening store test, so CI stays hermetic; the `TestAMLGap*`
      register drift guard passes unchanged (all register gaps stay closed).
- [x] **Register + roadmap** — G2 row, §6.2 and the roadmap's vendor-track row in
      `docs/compliance/CBN_AML_COMPLIANCE_SPEC.md` updated to name the wired adapters and the
      shipped re-evaluation; `amlSpecClosed`/`amlSpecOpen` in
      `internal/app/aml_spec_guard_test.go` are unchanged (all closed), so the
      guard passes — flipped in the same wave as the code, by design.

## What this pack does **not** close

- **G10–G15** (liveness, duplicate identity, document authenticity, fraud
  engine, geography/expected-activity inputs, AIF reporting) are owned builds
  outside the vendor lane; see spec §3.7.
- **The freeze obligation gets real, not automatic.** Once the NSC list is
  carried, a designation triggers a freeze-and-report duty. The platform's
  screening lane produces the decision; the *freeze procedure* is an operator
  procedure that should be written beside this pack.
- **Programme duties** (MLRO, training, SCUML, independent validation) remain
  the §2.2 operator obligations.

## Sign-off

| Step | Owner | Date | Evidence |
|---|---|---|---|
| Gates 1–3 complete | compliance lead | | this pack, filled |
| Compliance committee approves the purchase | MLRO + committee | | §5 charter minutes |
| Contract executed | commercial | | contract + DPA |
| Gate 4 merged | engineering | | PR + migration + tests |
| Register flipped to closed | compliance lead | | spec §5 diff |
