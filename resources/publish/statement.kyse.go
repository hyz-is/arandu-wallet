//go:build kyse

package wallet

import (
	"github.com/arandu-io/kyse/components"

	wallet "github.com/hyz-is/arandu-wallet"
)

@go
type StatementData = wallet.StatementPageData

func statementTable(data StatementData) components.DataTableProps {
	rows := make([]components.TableRow, 0, len(data.Rows))
	for _, row := range data.Rows {
		directionVariant := "outline"
		if !row.Incoming {
			directionVariant = "destructive"
		}
		settledVariant := "secondary"
		if row.Settled {
			settledVariant = "outline"
		}
		rows = append(rows, components.TableRow{Key: row.ID, Cells: []components.TableCell{
			{Text: row.Sequence, SortValue: row.Sequence},
			{Text: row.OperationLabel},
			{HTML: components.Badge(components.BadgeProps{Label: row.Direction, Variant: directionVariant})},
			{Text: row.Amount},
			{Text: row.BalanceAfter},
			{HTML: components.Badge(components.BadgeProps{Label: row.Created, Variant: settledVariant})},
		}})
	}
	return components.DataTableProps{
		ID: "wallet-statement", Label: data.Labels.T("screen.statement_title"), Caption: data.Labels.T("screen.statement_lead"),
		ColumnsLabel: data.Labels.T("control.columns"),
		Columns: []components.TableColumn{
			{Label: "#", Key: "sequence"},
			{Label: data.Labels.T("field.kind"), Key: "kind"},
			{Label: data.Labels.T("field.direction"), Key: "direction"},
			{Label: data.Labels.T("field.amount"), Key: "amount"},
			{Label: data.Labels.T("field.balance_after"), Key: "balance_after", Hideable: true},
			{Label: data.Labels.T("field.settled"), Key: "settled", Hideable: true},
		},
		Rows: rows,
		Empty: components.EmptyProps{Title: data.Labels.T("screen.statement_empty")},
	}
}
@endgo

@extends('layouts.app')

@section('content')
	<div class="flex flex-wrap items-start justify-between gap-4">
		<div>
			<h1 class="text-2xl font-semibold tracking-tight">{{ .Labels.T("screen.statement_title") }}</h1>
			<p class="text-muted-foreground mt-1 text-sm">{{ .Labels.T("screen.statement_lead") }}</p>
		</div>
		<a class="btn" data-variant="outline" data-size="sm" href="{{ .Prefix }}/{{ .Wallet.ID }}">{{ .Labels.T("control.back") }}</a>
	</div>

	<div class="card mt-6 flex flex-wrap items-center justify-between gap-3 p-4">
		<div class="min-w-0"><p class="text-sm font-semibold">{{ .Wallet.Name }}</p><p class="text-muted-foreground mt-1 truncate text-xs">{{ .Wallet.HolderID }} / {{ .Wallet.Slug }}</p></div>
		@if(.Wallet.Negative)
			<span class="text-destructive text-lg font-semibold">{{ .Wallet.Balance }} {{ .Wallet.Currency }}</span>
		@else
			<span class="text-lg font-semibold">{{ .Wallet.Balance }} {{ .Wallet.Currency }}</span>
		@endif
	</div>

	<div class="mt-6">{!! components.DataTable(statementTable(.)) !!}</div>

	@if(len(.Conversions) > 0)
		<div class="mt-8"><h2 class="text-lg font-semibold">{{ .Labels.T("field.rate") }}</h2><ul class="mt-3 grid gap-2">
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
		</ul></div>
	@endif

	@if(len(.Charges) > 0)
		<div class="mt-8"><h2 class="text-lg font-semibold">{{ .Labels.T("field.fee") }}</h2><ul class="mt-3 grid gap-2">
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
		</ul></div>
	@endif

	@if(.Next != "")
		<div class="mt-6"><a class="btn" data-variant="outline" data-size="sm" href="{{ .Prefix }}/{{ .Wallet.ID }}/entries?cursor={{ .Next }}">{{ .Labels.T("control.next") }}</a></div>
	@endif
@endsection
