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

// PlatformOperatorFor is the platform operator, acting for an application
// whose own platform policy has already decided -- the console that reads the
// money of every tenant, not any tenant's staff.
//
// It is OperatorFor's twin for ListAllOperations: the application asks its own
// policy whether this person runs the platform, and the Grant that policy
// issued is the proof. This turns that proof into the subject the wallet's
// policy answers WalletOperationsAll for, named after whoever the Grant was
// issued to and refused when that person is inside somebody else's account.
// Hand it only a Grant the platform's policy issued: a Grant a workspace's own
// policy issued says nothing about the other workspaces. An invalid Grant --
// the zero value, or one a policy refused -- is refused here.
func PlatformOperatorFor(g security.Grant) (security.Subject, error) {
	if err := g.Check(g.Action()); err != nil {
		return security.Subject{}, fmt.Errorf("wallet: a platform operator is derived from a Grant somebody's policy issued: %w", err)
	}
	tenant := data.Tenant(g)
	if tenant == "" {
		return security.Subject{}, fmt.Errorf("wallet: the Grant names no tenant, so there is no platform operator to derive")
	}
	if g.Subject().Impersonated() {
		return security.Subject{}, fmt.Errorf("%w: a subject inside somebody else's account reads no other tenant", security.ErrForbidden)
	}
	return security.Subject{ID: "platform:" + g.Subject().ID, Tenant: tenant, Roles: []string{PlatformRole}}, nil
}
