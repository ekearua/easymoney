package app

// Drift guard for CBN_AML_COMPLIANCE_SPEC.md.
//
// The spec is the compliance file a reviewer traces obligation -> code -> test
// from, so its gap register must never quietly disagree with itself. The class
// of bug this pins is real and already happened once: the G7 register row kept
// describing the scheduled audit-chain job as "the remaining sliver" for a wave
// after the job shipped (migration 084), while §3.5 and the register's own
// scheduling note both said it was done. A reader checking the action list was
// being told work remained that did not.
//
// So this test asserts, mechanically:
//
//  1. the register's rows are exactly G1..G15, numbered contiguously and once
//     each (a deleted or renumbered row fails);
//  2. the closed set is exactly G1-G15 (strikethrough rows) and the open set is
//     empty — every gap in the register has closed, so flipping a gap to closed
//     (or back) requires editing this table too, which is the point — the claim
//     cannot change silently;
//  3. no closed row carries stale open-work language;
//  4. the §5 scheduling note reflects the owned-open set: when owned open work
//     exists it names every open gap, and when none does it says so (the guard
//     refuses a note that both claims and hides open owned builds);
//  5. the §8 roadmap splits open work the way the register does: G9 in the
//     vendor-track row while open, G14-G15 in the owned-unscheduled row, and
//     neither row named as current work once closed.
//
// When a gap genuinely closes, update the tables here and the doc together.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const amlSpecPath = "CBN_AML_COMPLIANCE_SPEC.md"

// amlSpecClosed and amlSpecOpen are the canonical status of every register row.
var amlSpecClosed = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
var amlSpecOpen = []int{}

// amlSpecOwnedOpen is the subset of open gaps that is ours to build (G9 is the
// vendor lane, so it appears in the vendor-track row instead). Every register
// gap has closed, so the owned-open set is empty.
var amlSpecOwnedOpen = []int{}

// amlSpecStaleOpenPhrases are sentences that mean "work remains" and therefore
// must never appear in a closed register row. "the remaining sliver" is the
// exact phrase that drifted on G7.
var amlSpecStaleOpenPhrases = []string{
	"the remaining sliver",
	"still outstanding",
	"not yet implemented",
	"tracked as a hardening item",
	"no owned list table",
	"remains open",
}

var amlRegisterRow = regexp.MustCompile(`^\|\s*\*\*G(\d+)\*\*\s*\|`)

func amlSpecRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not locate the module root (go.mod) from the test directory")
		}
		root = parent
	}
}

func amlSpecRead(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(amlSpecRepoRoot(t), amlSpecPath))
	if err != nil {
		t.Fatalf("read %s: %v", amlSpecPath, err)
	}
	return string(raw)
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatInts(ids []int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, "G"+strconv.Itoa(id))
	}
	return strings.Join(parts, ", ")
}

// TestAMLGapRegisterStatusMatchesSpec pins the register's closed/open split and
// refuses stale open-work language inside closed rows.
func TestAMLGapRegisterStatusMatchesSpec(t *testing.T) {
	spec := amlSpecRead(t)

	rows := map[int]string{}
	for _, line := range strings.Split(spec, "\n") {
		line = strings.TrimRight(line, "\r")
		match := amlRegisterRow.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		id, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		if _, dup := rows[id]; dup {
			t.Fatalf("register row G%d appears more than once; ids must be unique", id)
		}
		rows[id] = line
	}
	if len(rows) == 0 {
		t.Fatalf("no register rows found in %s; the parser or the table format changed", amlSpecPath)
	}

	want := append(append([]int{}, amlSpecClosed...), amlSpecOpen...)
	sort.Ints(want)
	var got []int
	for id := 1; id <= 200; id++ {
		if _, ok := rows[id]; ok {
			got = append(got, id)
		}
	}
	if !intsEqual(got, want) {
		t.Fatalf("register ids drifted.\n  spec has:  %s\n  test says: %s\n"+
			"A new gap needs a row in the spec AND an entry in amlSpecClosed/amlSpecOpen here.",
			formatInts(got), formatInts(want))
	}

	// Iterate in id order so both slices come out ascending.
	var actualClosed, actualOpen []int
	for id := 1; id <= 200; id++ {
		row, ok := rows[id]
		if !ok {
			continue
		}
		if strings.Contains(row, "~~") {
			actualClosed = append(actualClosed, id)
		} else {
			actualOpen = append(actualOpen, id)
		}
	}
	if !intsEqual(actualClosed, amlSpecClosed) {
		t.Errorf("closed gaps drifted.\n  spec strikes out: %s\n  test expects:     %s",
			formatInts(actualClosed), formatInts(amlSpecClosed))
	}
	if !intsEqual(actualOpen, amlSpecOpen) {
		t.Errorf("open gaps drifted.\n  spec leaves open: %s\n  test expects:     %s",
			formatInts(actualOpen), formatInts(amlSpecOpen))
	}

	for _, id := range actualClosed {
		lower := strings.ToLower(rows[id])
		for _, phrase := range amlSpecStaleOpenPhrases {
			if strings.Contains(lower, phrase) {
				t.Errorf("register row G%d is marked closed but still says %q - "+
					"either finish the row's evidence cell or reopen the gap", id, phrase)
			}
		}
	}
}

// TestAMLGapRegisterSummaryMatchesRoadmap pins the two places that restate the
// register (the §5 scheduling note and the §8 roadmap) against it.
func TestAMLGapRegisterSummaryMatchesRoadmap(t *testing.T) {
	spec := amlSpecRead(t)

	for _, id := range amlSpecOpen {
		if !strings.Contains(spec, "G"+strconv.Itoa(id)) {
			t.Errorf("open gap G%d is not mentioned anywhere in the spec", id)
		}
	}
	note := section(spec, "**Scheduling note:**", "## 6.")
	if note == "" {
		t.Fatal("could not find the scheduling note under the register")
	}
	if len(amlSpecOwnedOpen) > 0 {
		if strings.Contains(note, "Every owned build is closed") {
			t.Error("the scheduling note claims every owned build is closed while owned gaps are open")
		}
		if !strings.Contains(note, "Open owned builds") {
			t.Error("the scheduling note no longer states which owned builds are open")
		}
	} else {
		if strings.Contains(note, "Open owned builds") {
			t.Error("no owned build is open, but the scheduling note still lists open owned builds")
		}
		if !strings.Contains(note, "Every owned build is closed") {
			t.Error("every owned build is closed, but the scheduling note does not say so")
		}
	}
	for _, id := range amlSpecOwnedOpen {
		if !strings.Contains(note, "G"+strconv.Itoa(id)) {
			t.Errorf("the scheduling note omits open owned gap G%d", id)
		}
	}

	roadmap := section(spec, "| **Open (owned, unscheduled)**", "**Re-open triggers")
	if roadmap == "" {
		t.Fatal("could not find the roadmap's open-owned row")
	}
	if len(amlSpecOwnedOpen) > 0 {
		for _, id := range amlSpecOwnedOpen {
			if !strings.Contains(roadmap, "G"+strconv.Itoa(id)) {
				t.Errorf("the roadmap's open-owned row omits G%d", id)
			}
		}
	} else if !strings.Contains(roadmap, "none") {
		t.Error("no owned build is open, but the roadmap's open-owned row still lists work")
	}

	vendor := section(spec, "| **Vendor track**", "\n\n")
	if vendor == "" {
		t.Fatal("could not find the roadmap's vendor-track row")
	}
	if containsInt(amlSpecOpen, 9) && !strings.Contains(vendor, "G9") {
		t.Error("the roadmap's vendor-track row must name G9 while it is the open vendor-lane gap")
	}
	if !containsInt(amlSpecOpen, 9) && strings.Contains(vendor, "G9") {
		t.Error("G9 is closed; the vendor-track row must not present it as current open work")
	}
}

func containsInt(ids []int, want int) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// section returns the text between start and the next end marker (or the end of
// the document when end is empty).
// amlReferencedDocs are repo-root documents the spec cites by filename. They are
// asserted to exist so a rename cannot leave the compliance file pointing at a
// file nobody can open - the G9 evidence cell points at one of them.
var amlReferencedDocs = []string{
	"AML_VENDOR_EVIDENCE_PACK.md",
	"AML_VENDOR_DECISION_RECORD.md",
}

// TestAMLSpecReferencedDocsExist pins the cross-document references the register
// relies on (the G9 evidence cell and the sources list).
func TestAMLSpecReferencedDocsExist(t *testing.T) {
	spec := amlSpecRead(t)
	root := amlSpecRepoRoot(t)
	for _, name := range amlReferencedDocs {
		if !strings.Contains(spec, name) {
			t.Errorf("the spec no longer references %s; update amlReferencedDocs if that was intentional", name)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("the spec references %s but no such file exists at the repo root: %v", name, err)
		}
	}
}

func section(spec, start, end string) string {
	i := strings.Index(spec, start)
	if i < 0 {
		return ""
	}
	rest := spec[i:]
	if end == "" {
		return rest
	}
	if j := strings.Index(rest, end); j > 0 {
		return rest[:j]
	}
	return rest
}
