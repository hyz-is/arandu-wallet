//go:build kyse

package wallet

import (
	"github.com/arandu-io/kyse/components"

	wallet "github.com/hyz-is/arandu-wallet"
)

@go
// StatementData is what the handler hands this page.
//
// It is an alias rather than a struct declared here, so the shape is written
// once, beside the code that fills it.
type StatementData = wallet.StatementPageData
@endgo

@extends('layouts.app')

@section('content')
	<div class="flex flex-wrap items-start justify-between gap-4">
		<div>
			<h1 class="text-2xl font-semibold tracking-tight">{{ .Labels.T("screen.statement_title") }}</h1>
			<p class="text-muted-foreground mt-1 text-sm">{{ .Labels.T("screen.statement_lead") }}</p>
		</div>
		<a class="btn" data-variant="outline" data-size="sm" href="{{ .Prefix }}/{{ .Wallet.ID }}">
			{{ .Labels.T("control.back") }}
		</a>
	</div>

	<div class="card mt-6 flex flex-wrap items-center justify-between gap-3 p-4">
		<div class="min-w-0">
			<p class="text-sm font-semibold">{{ .Wallet.Name }}</p>
			<p class="text-muted-foreground mt-1 truncate text-xs">{{ .Wallet.HolderID }} / {{ .Wallet.Slug }}</p>
		</div>
		@if(.Wallet.Negative)
			<span class="text-destructive text-lg font-semibold">{{ .Wallet.Balance }} {{ .Wallet.Currency }}</span>
		@else
			<span class="text-lg font-semibold">{{ .Wallet.Balance }} {{ .Wallet.Currency }}</span>
		@endif
	</div>

	@if(len(.Rows) > 0)
		<div class="mt-6 overflow-x-auto">
			<table class="w-full text-sm">
				<thead>
					<tr class="text-muted-foreground text-left text-xs">
						<th class="py-2 pr-4">#</th>
						<th class="py-2 pr-4">{{ .Labels.T("field.kind") }}</th>
						<th class="py-2 pr-4">{{ .Labels.T("field.direction") }}</th>
						<th class="py-2 pr-4">{{ .Labels.T("field.amount") }}</th>
						<th class="py-2 pr-4">{{ .Labels.T("field.balance_after") }}</th>
						<th class="py-2">{{ .Labels.T("field.settled") }}</th>
					</tr>
				</thead>
				<tbody>
					@foreach(.Rows as row)
						<tr class="border-t">
							<td class="text-muted-foreground py-2 pr-4 text-xs">{{ row.Sequence }}</td>
							<td class="py-2 pr-4">{{ row.OperationLabel }}</td>
							<td class="py-2 pr-4">
								@if(row.Incoming)
									<span class="text-sm">{{ row.Direction }}</span>
								@else
									<span class="text-destructive text-sm">{{ row.Direction }}</span>
								@endif
							</td>
							<td class="py-2 pr-4 font-semibold">{{ row.Amount }}</td>
							<td class="py-2 pr-4">{{ row.BalanceAfter }}</td>
							<td class="py-2">
								@if(row.Settled)
									{!! components.Badge(components.BadgeProps{Label: row.Created, Variant: "outline"}) !!}
								@else
									{!! components.Badge(components.BadgeProps{Label: row.Created, Variant: "secondary"}) !!}
								@endif
							</td>
						</tr>
					@endforeach
				</tbody>
			</table>
		</div>
	@else
		<p class="text-muted-foreground mt-8 text-sm">{{ .Labels.T("screen.statement_empty") }}</p>
	@endif

	{{-- The rates and the charges go under the page rather than into it: each
	     belongs to an operation, and an operation writes an entry on two or
	     three wallets. Repeating one on every line would be repeating one fact
	     until two copies of it could differ. --}}
	@if(len(.Conversions) > 0)
		<div class="mt-8">
			<h2 class="text-lg font-semibold">{{ .Labels.T("field.rate") }}</h2>
			<ul class="mt-3 grid gap-2">
				@foreach(.Conversions as rate)
					<li class="card p-3 text-sm">
						<span class="font-semibold">{{ rate.From }} &rarr; {{ rate.To }}</span>
						<span class="text-muted-foreground ml-2 text-xs">{{ rate.Rate }}</span>
						<span class="text-muted-foreground ml-2 text-xs">{{ .Labels.T("field.quoted_at") }} {{ rate.QuotedAt }}</span>
						@if(!rate.Exact)
							<span class="text-muted-foreground ml-2 text-xs">+ {{ rate.Remainder }}</span>
						@endif
					</li>
				@endforeach
			</ul>
		</div>
	@endif

	@if(len(.Charges) > 0)
		<div class="mt-8">
			<h2 class="text-lg font-semibold">{{ .Labels.T("field.fee") }}</h2>
			<ul class="mt-3 grid gap-2">
				@foreach(.Charges as charge)
					<li class="card p-3 text-sm">
						<span class="font-semibold">{{ .Labels.T("field.fee") }} {{ charge.Fee }}</span>
						<span class="text-muted-foreground ml-2 text-xs">{{ .Labels.T("field.discount") }} {{ charge.Discount }}</span>
						<span class="text-muted-foreground ml-2 text-xs">{{ .Labels.T("field.base") }} {{ charge.Base }}</span>
						@if(charge.FeeWallet != "")
							<span class="text-muted-foreground ml-2 text-xs">&rarr; {{ charge.FeeWallet }}</span>
						@endif
					</li>
				@endforeach
			</ul>
		</div>
	@endif

	@if(.Next != "")
		<div class="mt-6">
			<a class="btn" data-variant="outline" data-size="sm"
			   href="{{ .Prefix }}/{{ .Wallet.ID }}/entries?cursor={{ .Next }}">
				{{ .Labels.T("control.next") }}
			</a>
		</div>
	@endif
@endsection
