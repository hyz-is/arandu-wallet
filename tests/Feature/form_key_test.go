package feature_test

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/arandu-io/framework/data"

	wallet "github.com/hyz-is/arandu-wallet"
)

// These tests submit the operations screen's forms the way a browser does:
// with the fields the page drew and nothing else. A browser cannot set a header
// on a form it submits, so the idempotency key travels in the field the screen
// drew it into, and these hold that a drawn form moves money, that the same
// drawn form sent twice moves it once, and that a key the screen did not draw
// is refused -- while a client sending the header is answered as it always was.

// drawnForm is the address and the idempotency key of the form marked name on a
// drawn screen.
func drawnForm(t *testing.T, body, name string) (action, key string) {
	t.Helper()
	m := regexp.MustCompile(`<form data-form="` + regexp.QuoteMeta(name) + `" method="post" action="([^"]*)"><input type="hidden" name="` +
		regexp.QuoteMeta(wallet.IdempotencyField) + `" value="([^"]*)"></form>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the screen draws no %s form:\n%s", name, body)
	}
	return html.UnescapeString(m[1]), html.UnescapeString(m[2])
}

// entriesOf counts the rows a wallet's ledger holds, read as the operator.
func entriesOf(t *testing.T, service *wallet.WalletService, walletID string) int {
	t.Helper()
	statement, err := service.History(context.Background(), staff(), wallet.HistoryRequest{
		WalletID: walletID,
		Query:    data.Query{Limit: wallet.MaxPageSize},
	})
	if err != nil {
		t.Fatalf("reading the ledger of %s: %v", walletID, err)
	}
	return len(statement.Entries)
}

// submit posts a drawn form's fields, as a browser does on a click.
func (a servedWallet) submit(t *testing.T, action string, fields url.Values, cookies []*http.Cookie, header http.Header) (int, string) {
	t.Helper()
	rec := a.visit(t, http.MethodPost, action, fields, cookies, header)
	return rec.Code, rec.Body.String()
}

func TestTheFormsTheScreenDrewMoveMoneyOnceWithNoHeader(t *testing.T) {
	t.Parallel()

	app := serveWallet(t, true)
	service := app.module.Service()
	main := openWallet(t, service, "user-1", "main", 2)
	savings := openWallet(t, service, "user-1", "savings", 2)
	session := app.signIn(t, person("user-1"))
	body, token, cookies := app.draw(t, wallet.DefaultPrefix+"/"+main.ID, session)

	keys := map[string]bool{}
	for _, name := range []string{"deposit", "withdrawal", "transfer"} {
		_, key := drawnForm(t, body, name)
		if !strings.HasPrefix(key, "form-") || len(key) < 40 {
			t.Fatalf("the %s form carries %q, want a key minted for it", name, key)
		}
		if keys[key] {
			t.Fatalf("the %s form carries the key another form on the same page carries", name)
		}
		keys[key] = true
	}

	steps := []struct {
		form    string
		fields  url.Values
		balance wallet.Amount
		entries int
	}{
		{"deposit", url.Values{"amount": {"10.00"}}, 1000, 1},
		{"withdrawal", url.Values{"amount": {"1.50"}}, 850, 2},
		{"transfer", url.Values{"amount": {"2.00"}, "to_wallet_id": {savings.ID}}, 650, 3},
	}
	for _, step := range steps {
		action, key := drawnForm(t, body, step.form)
		fields := url.Values{"_token": {token}, wallet.IdempotencyField: {key}}
		for name, values := range step.fields {
			fields[name] = values
		}

		// The first click, and then the same drawn form again: a second click,
		// or the back button and the same button.
		for attempt := 1; attempt <= 2; attempt++ {
			code, answer := app.submit(t, action, fields, cookies, nil)
			if code != http.StatusSeeOther {
				t.Fatalf("submission %d of the %s form answered %d, want the redirect to the wallet: %s", attempt, step.form, code, answer)
			}
			if got := balanceOf(t, service, main.ID); got != step.balance {
				t.Fatalf("after submission %d of the %s form the balance is %d, want %d", attempt, step.form, got, step.balance)
			}
			if got := entriesOf(t, service, main.ID); got != step.entries {
				t.Fatalf("after submission %d of the %s form the ledger holds %d entries, want %d", attempt, step.form, got, step.entries)
			}
		}
	}
	if got := balanceOf(t, service, savings.ID); got != 200 {
		t.Fatalf("the transfer submitted twice credited %d, want 200 once", got)
	}

	// Loading the screen again draws new forms, and a new deposit is a new
	// movement.
	body, token, cookies = app.draw(t, wallet.DefaultPrefix+"/"+main.ID, cookies)
	action, key := drawnForm(t, body, "deposit")
	if keys[key] {
		t.Fatal("the screen drawn again carries the deposit key of the first drawing, so a second deposit would be answered as the first")
	}
	if code, answer := app.submit(t, action, url.Values{"_token": {token}, wallet.IdempotencyField: {key}, "amount": {"1.00"}}, cookies, nil); code != http.StatusSeeOther {
		t.Fatalf("the deposit form of the screen drawn again answered %d: %s", code, answer)
	}
	if got := balanceOf(t, service, main.ID); got != 750 {
		t.Fatalf("the second drawing's deposit left %d, want 750", got)
	}
}

func TestARefundFormSentTwiceGivesTheLineBackOnce(t *testing.T) {
	t.Parallel()

	app := serveWallet(t, true)
	service := app.module.Service()
	buyer := openWallet(t, service, "user-1", "main", 2)
	shop := openWallet(t, service, "shop-1", "main", 2)
	deposit(t, service, buyer.ID, "seed-1", "100.00")
	bought, err := service.Pay(context.Background(), staff(), wallet.PayRequest{
		IdempotencyKey: "basket-1",
		PayerWalletID:  buyer.ID,
		Cart:           wallet.NewCart(wallet.CartItem{Product: &item{key: "book", wallet: shop.ID, price: 2500}, Quantity: 1}),
	})
	if err != nil {
		t.Fatalf("paying: %v", err)
	}
	line := bought.Purchases[0]

	// Giving a line back is the operator's, so the operator draws the screen.
	session := app.signIn(t, staff())
	body, token, cookies := app.draw(t, wallet.DefaultPrefix+"/"+buyer.ID, session)
	action, key := drawnForm(t, body, "refund-"+line.ID)
	fields := url.Values{"_token": {token}, wallet.IdempotencyField: {key}, "purchase_ids": {line.ID}, "reason": {"Refund"}}

	// The second submission is answered by the line's own state rather than by
	// a replay: Refund asks whether the line was already given back before it
	// asks about the key, so the same key twice is the 409 a refund of a
	// refunded line gets. Either way the money moves once.
	for attempt, want := range []int{http.StatusSeeOther, http.StatusConflict} {
		code, answer := app.submit(t, action, fields, cookies, nil)
		if code != want {
			t.Fatalf("submission %d of the refund form answered %d, want %d: %s", attempt+1, code, want, answer)
		}
		if got := balanceOf(t, service, buyer.ID); got != 10000 {
			t.Fatalf("after submission %d of the refund form the buyer holds %d, want 10000", attempt+1, got)
		}
		if got := balanceOf(t, service, shop.ID); got != 0 {
			t.Fatalf("after submission %d of the refund form the shop holds %d, want 0", attempt+1, got)
		}
	}
}

func TestAKeyTheScreenDidNotDrawIsRefused(t *testing.T) {
	t.Parallel()

	app := serveWallet(t, true)
	service := app.module.Service()
	main := openWallet(t, service, "user-1", "main", 2)
	other := openWallet(t, service, "user-1", "other", 2)
	session := app.signIn(t, person("user-1"))

	earlier, earlierToken, _ := app.draw(t, wallet.DefaultPrefix+"/"+main.ID, session)
	_, earlierKey := drawnForm(t, earlier, "deposit")
	body, token, cookies := app.draw(t, wallet.DefaultPrefix+"/"+main.ID, session)
	depositAction, depositKey := drawnForm(t, body, "deposit")
	withdrawalAction, _ := drawnForm(t, body, "withdrawal")
	otherBody, _, _ := app.draw(t, wallet.DefaultPrefix+"/"+other.ID, session)
	_, otherKey := drawnForm(t, otherBody, "deposit")
	if earlierToken == token {
		t.Fatal("two drawings carried one token, so the drawing-bound case below proves nothing")
	}

	for _, forged := range []struct {
		why    string
		action string
		key    string
		header http.Header
	}{
		{"no key at all", depositAction, "", nil},
		{"a key the caller made up", depositAction, "form-" + strings.Repeat("A", 43), nil},
		{"a key the caller made up in the shape of nothing", depositAction, "anything", nil},
		{"the deposit form's key on the withdrawal route", withdrawalAction, depositKey, nil},
		{"another wallet's deposit key", depositAction, otherKey, nil},
		{"the key of an earlier drawing with this drawing's token", depositAction, earlierKey, nil},
		{"the drawn key beside a header naming another request", depositAction, depositKey, http.Header{wallet.IdempotencyHeader: {"client-key-1"}}},
	} {
		fields := url.Values{"_token": {token}, "amount": {"10.00"}}
		if forged.key != "" {
			fields.Set(wallet.IdempotencyField, forged.key)
		}
		code, answer := app.submit(t, forged.action, fields, cookies, forged.header)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("a submission with %s answered %d, want %d: %s", forged.why, code, http.StatusUnprocessableEntity, answer)
		}
		if !strings.Contains(answer, wallet.IdempotencyField) {
			t.Errorf("a submission with %s was refused without naming %s: %s", forged.why, wallet.IdempotencyField, answer)
		}
	}
	if got := entriesOf(t, service, main.ID); got != 0 {
		t.Fatalf("the refused submissions wrote %d entries, want none", got)
	}
	if got := balanceOf(t, service, main.ID); got != 0 {
		t.Fatalf("the refused submissions left a balance of %d, want 0", got)
	}

	// The drawn key itself, with the token it was drawn with, is taken.
	if code, answer := app.submit(t, depositAction, url.Values{"_token": {token}, wallet.IdempotencyField: {depositKey}, "amount": {"10.00"}}, cookies, nil); code != http.StatusSeeOther {
		t.Fatalf("the drawn deposit form answered %d: %s", code, answer)
	}
}

func TestAClientNamingItsRequestInTheHeaderIsAnsweredAsBefore(t *testing.T) {
	t.Parallel()

	app := serveWallet(t, true)
	service := app.module.Service()
	main := openWallet(t, service, "user-1", "main", 2)
	session := app.signIn(t, person("user-1"))
	_, token, cookies := app.draw(t, wallet.DefaultPrefix+"/"+main.ID, session)
	target := wallet.DefaultPrefix + "/" + main.ID + "/deposits"
	asked := http.Header{wallet.IdempotencyHeader: {"client-deposit-1"}, "Accept": {"application/json"}}

	// The header alone, which is the contract: 201 for the movement, 200 for
	// the same key again, and the money moved once.
	for attempt, want := range []int{http.StatusCreated, http.StatusOK} {
		code, answer := app.submit(t, target, url.Values{"_token": {token}, "amount": {"5.00"}}, cookies, asked)
		if code != want {
			t.Fatalf("header submission %d answered %d, want %d: %s", attempt+1, code, want, answer)
		}
	}
	if got := balanceOf(t, service, main.ID); got != 500 {
		t.Fatalf("the header key sent twice left %d, want 500", got)
	}

	// A field that says what the header says is the same request, whatever it
	// is: the header is still what names it.
	same := url.Values{"_token": {token}, "amount": {"5.00"}, wallet.IdempotencyField: {"client-deposit-1"}}
	if code, answer := app.submit(t, target, same, cookies, asked); code != http.StatusOK {
		t.Fatalf("the header with a field saying the same answered %d, want the replay's %d: %s", code, http.StatusOK, answer)
	}

	// The header still has to be there when nothing else names the request.
	if code, answer := app.submit(t, target, url.Values{"_token": {token}, "amount": {"5.00"}}, cookies, http.Header{"Accept": {"application/json"}}); code != http.StatusUnprocessableEntity ||
		!strings.Contains(answer, "is required") {
		t.Fatalf("a submission naming nothing answered %d, want %d for a required key: %s", code, http.StatusUnprocessableEntity, answer)
	}
	if got := balanceOf(t, service, main.ID); got != 500 {
		t.Fatalf("the replays moved money: balance %d, want 500", got)
	}
}
