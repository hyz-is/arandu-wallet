package wallet

import (
	"fmt"

	"github.com/arandu-io/framework/data"
	"github.com/arandu-io/framework/security"
)

// OperatorFor is the operator of one tenant, acting for an application whose
// own policy has already decided.
//
// Some money belongs to nobody who signs in -- an organisation's wallet, a
// shop's till -- and some decisions about a person's money are the
// application's, such as giving back what a failed piece of work had held. In
// both, the application asks its own policy first, and the Grant that policy
// issued is the proof. This turns that proof into the subject the wallet's
// policy answers: the operator role, inside the Grant's tenant and no other,
// named after whoever the Grant was issued to.
//
// The wallet's policy still runs on every call made with it, tenant first, so
// the subject reaches exactly the wallets of the tenant the application's own
// decision was about. An invalid Grant -- the zero value, or one a policy
// refused -- is refused here.
func OperatorFor(g security.Grant) (security.Subject, error) {
	if err := g.Check(g.Action()); err != nil {
		return security.Subject{}, fmt.Errorf("wallet: an operator is derived from a Grant somebody's policy issued: %w", err)
	}
	tenant := data.Tenant(g)
	if tenant == "" {
		return security.Subject{}, fmt.Errorf("wallet: the Grant names no tenant, so there is no operator to derive")
	}
	return security.Subject{ID: "operator:" + g.Subject().ID, Tenant: tenant, Roles: []string{OperatorRole}}, nil
}
