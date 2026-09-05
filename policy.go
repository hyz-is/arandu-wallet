package wallet

import (
	"context"
	"fmt"

	"github.com/arandu-io/framework/security"
)

// The actions of Wallet. Constants rather than strings at the call site: a
// typo in an action name would silently authorize nothing, or worse, everything.
//
// They carry the entity in the name because an application registers many
// packages, and the name of an action shows up in logs and in audit trails
// where "view" on its own says nothing about what was viewed.
//
// Money is split finer than read and write. A person who may see a balance is
// not thereby a person who may spend it, and the one who may spend their own is
// not the one who may undo somebody else's payment -- so viewing, depositing,
// withdrawing, transferring and reversing are five decisions and not one.
const (
	// WalletView is reading one wallet, balance included.
	WalletView security.Action = "wallet.view"
	// WalletList is paging through wallets.
	WalletList security.Action = "wallet.list"
	// WalletCreate is opening one.
	WalletCreate security.Action = "wallet.create"
	// WalletHistory is reading the ledger of one wallet.
	WalletHistory security.Action = "wallet.history"
	// WalletDeposit is putting money into one.
	WalletDeposit security.Action = "wallet.deposit"
	// WalletWithdraw is taking money out of one.
	WalletWithdraw security.Action = "wallet.withdraw"
	// WalletTransfer is moving money out of one and into another.
	WalletTransfer security.Action = "wallet.transfer"
	// WalletReverse is undoing an operation.
	WalletReverse security.Action = "wallet.reverse"
)

// OperatorRole is the role an application grants to the people who run its
// money: support staff, finance, whoever is trusted to move funds that are not
// their own and to undo what was already done.
//
// One role and not several. A package that shipped a hierarchy of roles would
// be a package deciding an application's organisation chart, and the rules
// below need exactly one distinction -- the holder, and somebody acting on the
// holder's behalf.
const OperatorRole = "wallet.operator"

// WalletPolicy is the only authority over who does what with a Wallet.
//
// It denies unless a rule below says otherwise, and the rules are written
// around two subjects: the holder, who may see and move their own money, and
// the operator, who may act across the tenant. Everything else -- a guest, a
// subject from another tenant, a signed-in person reaching for somebody else's
// wallet -- falls through to the refusal at the end.
//
// Reversal is deliberately not the holder's. Undoing a payment is a decision
// about a movement that already settled, and letting the person who received it
// take it back is a hole with a name.
type WalletPolicy struct{}

// Compile-time proof that the policy answers about this entity and no other. A
// policy that drifted onto another type would leave this one unguarded while
// the Model path still compiled.
var _ security.Policy[Wallet] = WalletPolicy{}

// Can decides whether the subject may perform the action on the record.
//
// It is the only place that decides. The service reaches the Model only after
// Authorize turns this method's nil result into a Grant.
//
// The record is the empty Wallet where the question is "may this subject do
// this kind of thing at all", and the loaded row where it is "may they do it to
// this money". Both are asked, in that order, and the second is what a rule
// about ownership answers.
func (WalletPolicy) Can(ctx context.Context, s security.Subject, a security.Action, record Wallet) error {
	// Tenant isolation comes first and applies to every action. Without it every
	// check below would be pointless in a multi-tenant system: a rule that
	// allows an owner to read their own record would allow it across customers
	// as soon as two of them have a record with the same identifier.
	//
	// The empty id is the candidate that has not been stored yet, which belongs
	// to nobody until it is written with the tenant off the Grant.
	if record.ID != "" && record.TenantID != s.Tenant {
		return fmt.Errorf("wallet belongs to another tenant")
	}

	// arandu:begin custom
	// A declared anonymous reader is answered here rather than left to fall
	// through, because falling through is what every rule below would do and
	// the reason would be invisible. There is no money a visitor with no
	// session owns.
	if s.IsGuest() {
		return fmt.Errorf("a guest has no wallet")
	}

	// The operator acts across the tenant. The tenant check above already ran,
	// so this is wide inside one customer and reaches no further.
	//
	// The actions are enumerated rather than allowed wholesale, so that an
	// action nobody has written a rule for is refused to everybody, including
	// here. A branch that answered "yes" to whatever it was asked would make
	// the next action somebody adds live the moment it is named.
	if s.HasRole(OperatorRole) {
		switch a {
		case WalletView, WalletList, WalletCreate, WalletHistory,
			WalletDeposit, WalletWithdraw, WalletTransfer, WalletReverse:
			return nil
		}
	}

	// The probe: "may this subject do this kind of thing at all". It is not the
	// decision, and the decision is the call the service makes afterwards with
	// the row it loaded -- which is where the rule below about the holder
	// finally has a holder to compare against.
	//
	// Listing is answered here and only here, because there is no single row to
	// ask about. What comes back is narrowed by the statement instead: a
	// listing that had to read a customer's rows in order to decide it may not
	// read them has already read them, so the service adds the holder predicate
	// for a subject who is not an operator.
	//
	// Reversal is absent from the list, so it stops here for everybody who is
	// not an operator.
	if isProbe(record) {
		switch a {
		case WalletList, WalletView, WalletHistory, WalletDeposit, WalletWithdraw, WalletTransfer:
			return nil
		}
		return fmt.Errorf("no rule allows %s on wallet", a)
	}

	// The holder, on a row that has one. This is the call that decides.
	if s.ID != "" && s.ID == record.HolderID {
		switch a {
		case WalletView, WalletHistory, WalletCreate, WalletDeposit, WalletWithdraw, WalletTransfer:
			return nil
		}
	}
	// arandu:end custom

	return fmt.Errorf("no rule allows %s on wallet", a)
}

// isProbe reports that the question is about the kind of thing rather than
// about a particular wallet.
//
// A wallet with neither an identifier nor a holder names nobody's money. A
// candidate on its way to being created is not one of these -- it carries the
// holder it is being opened for, which is what the rule about creation reads.
func isProbe(record Wallet) bool { return record.ID == "" && record.HolderID == "" }
