//go:build kyse

package wallet

import (
	"github.com/arandu-io/kyse/components"

	wallet "github.com/hyz-is/arandu-wallet"
)

@go
// IndexData is what the handler hands this page.
//
// It is an alias rather than a struct declared here, so the shape is written
// once, beside the code that fills it. A second declaration is two shapes kept
// in step by hand, and a field missing from one of them is a blank space on a
// page that answered 200.
type IndexData = wallet.IndexPageData
@endgo

@extends('layouts.app')

@section('content')
	<div class="flex flex-wrap items-start justify-between gap-4">
		<div>
			<h1 class="text-2xl font-semibold tracking-tight">{{ .Labels.T("screen.index_title") }}</h1>
			<p class="text-muted-foreground mt-1 text-sm">{{ .Labels.T("screen.index_lead") }}</p>
		</div>
	</div>

	<form class="mt-6 flex items-end gap-2" method="get" action="{{ .Prefix }}">
		<div class="grow">
			{!! components.Label(components.LabelProps{For: "holder_id", Text: .Labels.T("field.holder")}) !!}
			{!! components.Input(components.InputProps{
				Name:        "holder_id",
				ID:          "holder_id",
				Type:        "search",
				Value:       .Holder,
				Placeholder: .Labels.T("control.holder_placeholder"),
			}) !!}
		</div>
		{!! components.Button(components.ButtonProps{Label: .Labels.T("field.search"), Type: "submit", Variant: "outline"}) !!}
	</form>

	@if(len(.Rows) > 0)
		<ul class="mt-6 grid gap-3">
			@foreach(.Rows as row)
				<li class="card flex flex-wrap items-center justify-between gap-3 p-4">
					<div class="min-w-0">
						<a class="text-sm font-semibold hover:underline" href="{{ .Prefix }}/{{ row.ID }}">{{ row.Name }}</a>
						<p class="text-muted-foreground mt-1 truncate text-xs">{{ row.HolderID }} / {{ row.Slug }}</p>
					</div>
					<div class="flex items-center gap-3">
						{{-- The balance is text the handler already formatted at this
						     wallet's own scale. A template that placed the decimal
						     point would be the one place it can be wrong in a
						     language nobody type-checks. --}}
						@if(row.Negative)
							<span class="text-destructive text-sm font-semibold">{{ row.Balance }} {{ row.Currency }}</span>
						@else
							<span class="text-sm font-semibold">{{ row.Balance }} {{ row.Currency }}</span>
						@endif
						{!! components.Badge(components.BadgeProps{Label: row.CreditLimit, Variant: "outline"}) !!}
						<a class="btn" data-variant="ghost" data-size="sm" href="{{ .Prefix }}/{{ row.ID }}/entries">
							{{ .Labels.T("control.statement") }}
						</a>
					</div>
				</li>
			@endforeach
		</ul>
	@else
		<p class="text-muted-foreground mt-8 text-sm">{{ .Labels.T("screen.index_empty") }}</p>
	@endif

	@if(.Next != "")
		<div class="mt-6">
			<a class="btn" data-variant="outline" data-size="sm"
			   href="{{ .Prefix }}?holder_id={{ .Holder }}&amp;cursor={{ .Next }}">
				{{ .Labels.T("control.next") }}
			</a>
		</div>
	@endif

	<form class="mt-10 grid gap-3 border-t pt-6" method="post" action="{{ .Prefix }}">
		@csrf
		<h2 class="text-lg font-semibold">{{ .Labels.T("control.open") }}</h2>
		{!! components.Field(components.FieldProps{
			Name:     "holder_id",
			Label:    .Labels.T("field.holder"),
			Required: true,
			Page:     .Form(),
		}) !!}
		{!! components.Field(components.FieldProps{
			Name:     "slug",
			Label:    .Labels.T("field.slug"),
			Required: true,
			Page:     .Form(),
		}) !!}
		{!! components.Field(components.FieldProps{
			Name:     "name",
			Label:    .Labels.T("field.name"),
			Required: true,
			Page:     .Form(),
		}) !!}
		{!! components.Field(components.FieldProps{
			Name:     "currency",
			Label:    .Labels.T("field.currency"),
			Required: true,
			Page:     .Form(),
		}) !!}
		<div>
			{!! components.Button(components.ButtonProps{Label: .Labels.T("control.open"), Type: "submit"}) !!}
		</div>
	</form>
@endsection
