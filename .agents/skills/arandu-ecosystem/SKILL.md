---
name: arandu-ecosystem
description: Apply the shared Arandu/HYZIS architecture before adding or changing authorization, organizations or tenants, balances or credits, tags, Markdown, API documentation, or any reusable cross-project capability. Use when a feature may overlap arandu-permission, arandu-wallet, arandu-tags, arandu-markdown or arandu-swagger, or when deciding whether a new responsibility belongs in the application or in a reusable Arandu module.
license: MIT
---

# Arandu ecosystem architecture

This procedure prevents an application from inventing a second implementation of a responsibility the Arandu ecosystem already owns.

## 1. Read the application before designing

Before writing code:

1. Read the repository `AGENTS.md`.
2. Read the relevant local `.agents/skills/`.
3. Inspect `bootstrap/app.go`, the relevant Model/Policy/Service/Repository, migrations and tests.
4. When available in the same workspace, compare the current implementation with:
   - `tayi-ai/cluster`;
   - `alpr-sh/alpr.sh`;
   - `alpr-sh/alpr-go`.
5. Prefer the newest working pattern proved by code and tests over a generic framework pattern or an old note.

Do not create a new abstraction because it is familiar. First prove that Arandu does not already have one.

## 2. Organizations are the application tenancy boundary

For applications with personal accounts, companies, teams or workspaces, follow the existing Organization pattern.

The canonical flow is:

`User identity -> Membership -> Organization/workspace -> scoped Subject -> Policy/Permission -> security.Grant -> data.Tenant(g) -> persistent resource`

Rules:

- A Membership links a login/user identity to an Organization.
- The Organization/workspace is the effective tenant for the resources it owns.
- What the member may do inside that Organization is authorization, not membership metadata.
- Organization permissions belong to Arandu Permission.
- Persistent reads and writes use the tenant carried by the authorized `security.Grant`.
- Production data access never trusts a tenant id from path, body, query or header.
- A guessed id from another tenant must not reveal that the resource exists.
- Reads are authorized exactly like writes.

The identity realm may need to resolve memberships before an Organization is selected. That lookup is not permission to let application resources bypass the Grant once the Organization context exists.

## 3. Permission means Arandu Permission

Package:

`github.com/hyz-is/arandu-permission`

Use it for:

- permission groups;
- actions;
- roles or role-equivalent grouping;
- member authorization;
- administration of access;
- resource access rules that belong to the shared permission system.

Do not create a second RBAC/ACL implementation in the application.

Application Policies still express application-domain decisions, but shared permission storage and management use Arandu Permission.

A Service authorizes before reaching persistence. A repository/model read or write receives the resulting Grant. The tenant comes from `data.Tenant(g)`.

## 4. Balances, credits and tokenization mean Arandu Wallet

Package:

`github.com/hyz-is/arandu-wallet`

Any feature involving one or more of the following must first use or extend Arandu Wallet:

- balance;
- credits;
- tokens;
- points with spendable value;
- deposits or withdrawals;
- transfers;
- reservations or holds;
- debit or credit movements;
- refunds or reversals;
- consumption units;
- wallet history;
- ledger or statements;
- tokenized rewards.

Do not introduce a local `balance`, `credits`, `credit_ledger`, `wallet`, `token_transactions` or equivalent as a new canonical store.

Existing local ledgers are legacy application code, not a pattern to copy. In particular, the historical ALPR.sh `credit_ledger` must not be used as precedent for a new project after this rule.

If Wallet lacks a reusable primitive, stop the local design and state the gap. Decide whether to extend Arandu Wallet before implementing an application-specific substitute.

The application may still own its commercial domain, such as product catalog, pricing policy, billing contracts or an external payment-provider adapter. It must not duplicate the canonical balance/ledger that Wallet owns.

## 5. Tags mean Arandu Tags

Package:

`github.com/hyz-is/arandu-tags`

Use it for reusable:

- tags;
- labels;
- flexible categorization;
- tag sets;
- attaching tags to resources.

Do not create project-specific tag tables when the shared module satisfies the need.

## 6. Markdown means Arandu Markdown

Package:

`github.com/hyz-is/arandu-markdown`

Use it when the application renders, sanitizes, previews, transforms or otherwise processes Markdown.

Do not add another Markdown parser/renderer merely because a library is familiar.

If a required behavior is missing, evaluate extending Arandu Markdown first.

## 7. API documentation means Arandu Swagger

Package:

`github.com/hyz-is/arandu-swagger`

Use it for OpenAPI/Swagger documentation of Arandu HTTP surfaces.

The route table is the source of truth. Wire documentation where the real public routes exist.

ALPR Go is the precedent: do not register Swagger in an empty scaffold that does not own the complete service surface; register it in the binary/module that has the routes that can actually be documented.

Do not maintain a second manual OpenAPI description that can drift from the Arandu router unless an explicit external contract requires a separately governed artifact.

## 8. Explicit Arandu wiring remains mandatory

Arandu modules are composed explicitly.

- Register dependencies and modules in `bootstrap/app.go` or the equivalent explicit composition point.
- Do not introduce service containers, facades, auto-discovery or hidden global wiring.
- Prefer typed configuration.
- Prefer `aru make:module` when it covers the feature.
- Keep custom generated sections inside the supported Arandu custom markers when regeneration applies.

Reading the composition file must remain sufficient to understand what the application was given.

## 9. Decide ownership before adding schema

Before creating a migration, table, service or package, answer:

1. Is this identity data or Organization-owned application data?
2. Which Organization owns it?
3. Which action authorizes access?
4. Which Policy issues the Grant?
5. Does an Arandu shared module already own this responsibility?
6. Does this need Wallet?
7. Does this need Tags?
8. Does this process Markdown?
9. Does its HTTP surface need Swagger documentation?
10. Is the capability reusable enough to belong in a new or existing Arandu module?

Do not create the schema until these answers are clear.

## 10. When a shared capability is missing

If no existing module owns a reusable responsibility:

1. name the missing capability;
2. list the existing Arandu modules inspected;
3. explain why none owns it;
4. decide whether an existing module should be extended;
5. otherwise propose a new `arandu-*` module;
6. define its boundary and public contract before embedding an implementation in one application.

A reusable capability should not be hidden inside the first application that needed it.

## 11. Validation

After the change:

- run the repository's own gates from `AGENTS.md`;
- validate both read and write tenant isolation;
- validate that authorization occurs before persistence;
- validate that the tenant came from the Grant;
- verify the application did not create a second implementation of a shared module responsibility;
- compare the result again with the relevant reference application.

The goal is not merely working code. The result must remain recognizably Arandu and compatible with the rest of the ecosystem.
