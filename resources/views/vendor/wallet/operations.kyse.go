//go:build kyse

package wallet

import (
	"github.com/arandu-io/kyse/components"

	wallet "github.com/hyz-is/arandu-wallet"
)

@go
// OperationsData is what the handler hands this page.
//
// It is an alias rather than a struct declared here, so the shape is written
// once, beside the code that fills it.
type OperationsData = wallet.OperationsPageData
@endgo

@extends('layouts.app')

@section('content')
	<div class="flex flex-wrap items-start justify-between gap-4">
		<div>
			<h1 class="text-2xl font-semibold tracking-tight">{{ .Wallet.Name }}</h1>
			<p class="text-muted-foreground mt-1 text-sm">{{ .Labels.T("screen.operations_lead") }}</p>
		</div>
		<div class="flex items-center gap-2">
			<a class="btn" data-variant="outline" data-size="sm" href="{{ .Prefix }}/{{ .Wallet.ID }}/entries">
				{{ .Labels.T("control.statement") }}
			</a>
			<a class="btn" data-variant="ghost" data-size="sm" href="{{ .Prefix }}">
				{{ .Labels.T("control.back") }}
			</a>
		</div>
	</div>

	<div class="card mt-6 grid gap-2 p-4">
		<div class="flex items-center justify-between">
			<span class="text-muted-foreground text-xs">{{ .Labels.T("field.balance") }}</span>
			@if(.Wallet.Negative)
				<span class="text-destructive text-lg font-semibold">{{ .Wallet.Balance }} {{ .Wallet.Currency }}</span>
			@else
				<span class="text-lg font-semibold">{{ .Wallet.Balance }} {{ .Wallet.Currency }}</span>
			@endif
		</div>
		<div class="flex items-center justify-between">
			<span class="text-muted-foreground text-xs">{{ .Labels.T("field.credit_limit") }}</span>
			<span class="text-sm">{{ .Wallet.CreditLimit }} {{ .Wallet.Currency }}</span>
		</div>
	</div>

	<div class="mt-8 grid gap-8 md:grid-cols-2">
		<form class="grid gap-3" method="post" action="{{ .Prefix }}/{{ .Wallet.ID }}/deposits">
			@csrf
			<h2 class="text-lg font-semibold">{{ .Labels.T("control.deposit") }}</h2>
			{!! components.Field(components.FieldProps{
				Name:        "amount",
				Label:       .Labels.T("field.amount"),
				Placeholder: .Labels.T("control.amount_placeholder"),
				Required:    true,
				Page:        .Form(),
			}) !!}
			<div>
				{!! components.Button(components.ButtonProps{Label: .Labels.T("control.deposit"), Type: "submit"}) !!}
			</div>
		</form>

		<form class="grid gap-3" method="post" action="{{ .Prefix }}/{{ .Wallet.ID }}/withdrawals">
			@csrf
			<h2 class="text-lg font-semibold">{{ .Labels.T("control.withdraw") }}</h2>
			{!! components.Field(components.FieldProps{
				Name:        "amount",
				Label:       .Labels.T("field.amount"),
				Placeholder: .Labels.T("control.amount_placeholder"),
				Required:    true,
				Page:        .Form(),
			}) !!}
			<div>
				{!! components.Button(components.ButtonProps{Label: .Labels.T("control.withdraw"), Type: "submit", Variant: "outline"}) !!}
			</div>
		</form>

		<form class="grid gap-3" method="post" action="{{ .Prefix }}/{{ .Wallet.ID }}/transfers">
			@csrf
			<h2 class="text-lg font-semibold">{{ .Labels.T("control.transfer") }}</h2>
			{!! components.Field(components.FieldProps{
				Name:        "to_wallet_id",
				Label:       .Labels.T("field.to_wallet"),
				Placeholder: .Labels.T("control.wallet_placeholder"),
				Required:    true,
				Page:        .Form(),
			}) !!}
			{!! components.Field(components.FieldProps{
				Name:        "amount",
				Label:       .Labels.T("field.amount"),
				Placeholder: .Labels.T("control.amount_placeholder"),
				Required:    true,
				Page:        .Form(),
			}) !!}
			<div>
				{!! components.Button(components.ButtonProps{Label: .Labels.T("control.transfer"), Type: "submit"}) !!}
			</div>
		</form>

		{{-- The control is drawn only for somebody the policy already answered
		     yes to. A button that comes back 403 is a button that teaches its
		     reader the page is broken. --}}
		@if(.MaySetCredit)
			<form class="grid gap-3" method="post" action="{{ .Prefix }}/{{ .Wallet.ID }}/credit">
				@csrf
				<input type="hidden" name="_method" value="PUT">
				<h2 class="text-lg font-semibold">{{ .Labels.T("field.credit_limit") }}</h2>
				{!! components.Field(components.FieldProps{
					Name:        "limit",
					Label:       .Labels.T("field.credit_limit"),
					Placeholder: .Labels.T("control.amount_placeholder"),
					Required:    true,
					Page:        .Form(),
				}) !!}
				<div>
					{!! components.Button(components.ButtonProps{Label: .Labels.T("field.credit_limit"), Type: "submit", Variant: "outline"}) !!}
				</div>
			</form>
		@endif
	</div>

	<div class="mt-10 border-t pt-6">
		<h2 class="text-lg font-semibold">{{ .Labels.T("screen.purchases_title") }}</h2>
		@if(len(.Purchases) > 0)
			<ul class="mt-3 grid gap-2">
				@foreach(.Purchases as line)
					<li class="card flex flex-wrap items-center justify-between gap-3 p-3 text-sm">
						<div class="min-w-0">
							<span class="font-semibold">{{ line.Product }}</span>
							<span class="text-muted-foreground ml-2 text-xs">&times;{{ line.Quantity }}</span>
							<span class="text-muted-foreground ml-2 text-xs">{{ line.Receiver }}</span>
						</div>
						<div class="flex items-center gap-2">
							{!! components.Badge(components.BadgeProps{Label: line.Kind, Variant: "outline"}) !!}
							<span class="font-semibold">{{ line.Paid }}</span>
							@if(!line.Refunded)
								<form method="post" action="{{ .Prefix }}/purchases/refunds">
									@csrf
									<input type="hidden" name="purchase_ids" value="{{ line.ID }}">
									<input type="hidden" name="reason" value="{{ .Labels.T("control.refund") }}">
									{!! components.Button(components.ButtonProps{Label: .Labels.T("control.refund"), Type: "submit", Variant: "ghost", Size: "sm"}) !!}
								</form>
							@endif
						</div>
					</li>
				@endforeach
			</ul>
		@else
			<p class="text-muted-foreground mt-3 text-sm">{{ .Labels.T("screen.purchases_empty") }}</p>
		@endif
	</div>
@endsection
