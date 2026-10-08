# Signed-vendor evidence pack — closing G2 and G9

**What this is.** The register in `CBN_AML_COMPLIANCE_SPEC.md` §5 leaves exactly
two gaps open, and both close on the screening purchase:

| Gap | Open because | Closes when |
|---|---|---|
| **G2** — payment-time counterparty screening | closed against the *simulated* screener; only the live adapter is missing | the vendor adapter replaces `providers/paymentscreening.Simulated` behind `ports.PaymentScreener` |
| **G9** — adverse media + domestic lists | no vendor, so the list breadth and match quality are unproven | the contract is signed and list provenance is verified in writing |

This is the checklist an operator works through so those two flip on evidence,
not on optimism. It pairs with `AML_VENDOR_DECISION_RECORD.md` §7 (the trial
criteria) and §8 (the integration seams). Nothing here is a code change until
Gate 4.

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
| List-version provenance in the API contract | a disposition is only defensible later if the list version is recorded | `screening_results.list_version` (G8 seam) |
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

## Gate 4 — the code-side flip (only after Gates 1–3 pass)

Work items, in order:

- [ ] **Customer screening adapter** — a new provider beside
      `internal/providers/screening/http.go`, mapping ranked hits through our
      threshold policy into `ports.SanctionsScreener`'s decision vocabulary
      (`clear` / `possible` / `strong` / `blocked`).
- [ ] **Payment screening adapter** — the same client behind
      `ports.PaymentScreener`; `PAYMENT_SCREENING_PROVIDER` gains the vendor
      value and the `simulated` default stays for tests. Callers do not change.
- [ ] **List version recorded** — `screening_results` gains `list_version`
      (migration) and every decision writes it; the disposition surfaces show
      it so a later reviewer knows which list produced the match.
- [ ] **Open-hold re-evaluation** — the rescreen worker re-evaluates holds and
      ambiguous matches when a list version changes (vendor record §8(5)).
- [ ] **`.env.example` truthfulness** — the new provider value, endpoints and
      keys documented *because* the control now exists (the M0.1 rule: no
      envelope entry before the control).
- [ ] **Tests** — an adapter mapping test (hit payload → decision) plus a
      screening-path test with the vendor stubbed, so CI stays hermetic.
- [ ] **Register + roadmap** — flip G2 and G9 in `CBN_AML_COMPLIANCE_SPEC.md`
      §5 with the artifact references, and update `amlSpecClosed` /
      `amlSpecOpen` in `internal/app/aml_spec_guard_test.go` in the same
      commit (the guard fails otherwise, by design).

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
