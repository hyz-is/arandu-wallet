package wallet

import (
	"strings"
	"testing"

	"github.com/arandu-io/framework/data"
)

// The raw fragments of a listing are written for each engine, and the two that
// no server in the default suite runs are read here: every binding has its
// placeholder, every identifier is quoted the engine's way, and the metadata is
// read with the engine's own JSON functions.
func TestTheListingSQLIsSpelledForEveryEngine(t *testing.T) {
	t.Parallel()

	for _, dialect := range dialects {
		l := newListingSQL(dialect)
		for _, status := range operationStatuses {
			text, args := l.statusClause(status)
			if got := strings.Count(text, "?"); got != len(args) {
				t.Errorf("%s %s: %d placeholders for %d bindings in %s", dialect, status, got, len(args), text)
			}
		}
		meta, args := l.metaMatches(Meta{"reason": "payment", "reference": "ALPR-1"})
		if got := strings.Count(meta, "?"); got != len(args) {
			t.Errorf("%s meta: %d placeholders for %d bindings in %s", dialect, got, len(args), meta)
		}
		switch dialect {
		case data.DialectPostgres:
			if !strings.Contains(meta, "::jsonb @> cast(? as jsonb)") || len(args) != 1 || args[0] != `{"reason":"payment","reference":"ALPR-1"}` {
				t.Errorf("postgres reads the metadata as %s %v", meta, args)
			}
			if !strings.Contains(l.contains("x"), "strpos(") {
				t.Error("postgres has no instr")
			}
		case data.DialectMySQL:
			if !strings.Contains(meta, "json_unquote(json_extract(") || !strings.Contains(l.operationExists("1 = 1"), "`wallet_operations` as `o`") {
				t.Errorf("mysql reads the metadata as %s", meta)
			}
			if args[0] != `$."reason"` || args[1] != "payment" {
				t.Errorf("mysql binds %v", args)
			}
		case data.DialectSQLite:
			if !strings.Contains(meta, "json_extract(nullif(") || strings.Contains(meta, "json_unquote") {
				t.Errorf("sqlite reads the metadata as %s", meta)
			}
		}
	}
}

func TestACursorSaysWhichWayItReads(t *testing.T) {
	t.Parallel()

	for _, before := range []bool{false, true} {
		got, id, err := decodeCursor(encodeCursor(before, "entry-1"))
		if err != nil || got != before || id != "entry-1" {
			t.Fatalf("a cursor read back as %v %q %v", got, id, err)
		}
	}
	for _, bad := range []string{"", "x", encodeCursor(false, strings.Repeat("a", maxIdentifierLen+1)), "bTp4"} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("%q was read as a cursor", bad)
		}
	}
}

func TestEveryStatusIsDecidedOnceInGo(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		settled bool
		op      Operation
		amount  Amount
		by      map[OperationKind]string
		want    OperationStatus
	}{
		{false, Operation{Kind: OperationDeposit}, 100, nil, OperationPending},
		{false, Operation{Kind: OperationDeposit}, 100, map[OperationKind]string{OperationConfirmation: "c"}, OperationConfirmed},
		{false, Operation{Kind: OperationDeposit}, 100, map[OperationKind]string{OperationVoid: "v"}, OperationVoided},
		{false, Operation{Kind: OperationVoid}, 100, nil, OperationVoided},
		{true, Operation{Kind: OperationDeposit}, 100, nil, OperationSettled},
		{true, Operation{Kind: OperationDeposit, ReversedAmount: 40}, 100, nil, OperationPartiallyReversed},
		{true, Operation{Kind: OperationDeposit, ReversedAmount: 100}, 100, nil, OperationReversed},
		{true, Operation{Kind: OperationDeposit}, 100, map[OperationKind]string{OperationReversal: "r"}, OperationReversed},
	} {
		if got := operationStatus(c.settled, c.op, c.amount, c.by); got != c.want {
			t.Errorf("%+v answers %s, want %s", c, got, c.want)
		}
	}
}
