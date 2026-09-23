# budgit

A personal budgeting tool that runs entirely on your own machine. Monarch Money
in spirit — accounts, categories, budgets, a dashboard — minus the subscription,
the cloud, and the bank connections.

- **One binary.** No Docker, no Node, no frontend build, no database to install.
- **Zero dependencies.** Pure Go standard library. `go build` and you're done.
- **Local only.** Your data is one JSON file on your disk. Nothing phones home.
- **Money is never a float.** Everything is integer cents end to end.

<!-- Screenshot: run `budgit serve` and grab one for your README. -->

## Install

Requires [Go 1.26+](https://go.dev/dl/). Nothing else.

```sh
git clone https://github.com/mason-munnik/budgit.git
cd budgit
go build -o budgit .
```

That's the whole install. Put the binary on your `PATH` if you like:

```sh
sudo mv budgit /usr/local/bin/     # macOS / Linux
```

Prefer not to clone? `go install github.com/mason-munnik/budgit@latest` works too.

### Prebuilt binaries

`./build.sh v1.0.0` cross-compiles for macOS (Intel + Apple Silicon), Linux
(amd64 + arm64), and Windows, and writes archives to `dist/` ready to attach to
a GitHub release. Friends download one file and run it — no Go toolchain needed.

## Quick start

```sh
budgit account add  --name "Chase Checking" --type checking
budgit category add --name Groceries --kind expense
budgit category add --name Salary    --kind income

budgit txn add --date 2026-09-04 --account "Chase Checking" \
               --category Groceries --desc "Trader Joe's" --amount 84.31

budgit budget set --category Groceries --month 2026-09 --amount 600
budgit report --month 2026-09
budgit serve                      # dashboard at http://localhost:8080
```

## Commands

| Command | What it does |
|---|---|
| `account add --name NAME [--type TYPE] [--balance AMT]` | Add an account (checking, savings, credit, cash) |
| `account list` | List accounts with balances and net worth |
| `account set-balance --account REF --amount AMT` | Set what an existing account should show right now |
| `category add --name NAME --kind income\|expense` | Add a category |
| `category list` | List categories |
| `txn add --amount AMT [--date] [--account] [--category] [--desc]` | Record a transaction |
| `txn list [--month] [--account] [--category] [--uncategorized] [--limit]` | List and filter transactions |
| `txn categorize ID CATEGORY` | Categorize or recategorize a transaction |
| `budget set --category REF --amount AMT [--month]` | Set a monthly allowance |
| `budget list [--month]` | Show budgets for a month |
| `report [--month]` | Budget vs. actual by category |
| `serve [--addr localhost:8080]` | Start the web dashboard |

Accounts and categories can be referenced by name (case-insensitive; a unique
substring is enough, so `--category Dining` finds "Dining Out") or by numeric ID.

Dates are `YYYY-MM-DD`, months are `YYYY-MM`. Both default to today / this month.

## Account balances

An account's balance is its **opening balance** plus every transaction on it. Set
the opening balance when you create the account:

```sh
budgit account add --name "Chase Checking" --type checking --balance 8500
budgit account add --name "Amex Gold"      --type credit   --balance -499.50
```

For an account you already made, say what your bank shows you right now:

```sh
budgit account set-balance --account "Chase Checking" --amount 8500
```

That back-solves the opening balance so your existing transactions stay intact —
if $84.31 of spending is already recorded, the opening balance becomes $8,584.31
so the current balance reads exactly the $8,500 you typed. Re-run it any time you
want to resync against your bank.

Use a **negative** amount for money you owe (credit cards, loans). `account list`
totals every account into a net worth line.

An opening balance is deliberately **not** a transaction, so it never appears as
income or spending in a report — it only moves the balance.

## How amounts work

Amounts are stored as **signed integer cents**. Negative means money left the
account; positive means it arrived. There is no floating point anywhere in the
money path, so nothing drifts by a penny.

When you type an amount, you usually don't need the sign — **an unsigned amount
takes its direction from the category**:

```sh
--category Groceries --amount 84.31    # expense category  -> -$84.31
--category Salary    --amount 4200     # income category   -> +$4,200.00
```

An **explicit sign always wins**, which is how you record a refund into an
expense category:

```sh
--category Groceries --amount +12.49   # refund -> +$12.49
```

The report nets refunds against spending, so $216.49 of groceries minus a $12.49
return shows as $204.00 spent.

`$`, commas, and underscores are ignored, so `$1,299.99` and `1299.99` are the
same. More than two decimal places is an error rather than a silent rounding.

## The dashboard

`budgit serve` starts a local HTTP server with a single-page dashboard: headline
figures, a budget-vs-actual meter per category, a six-month spending trend, and
the month's transactions. It's read-only — edits go through the CLI.

The page is served from the binary itself (`go:embed`), so there's no build step
and no `node_modules`. Chart.js loads from a CDN; **if you're offline the trend
falls back to a table automatically** and everything else still works. There's
also a "Table view" toggle and a light/dark switch.

### A note on binding

budgit has **no authentication**, because it's meant to bind to loopback where
your OS is the access control. It therefore refuses to start on a non-loopback
address:

```
$ budgit serve --addr 0.0.0.0:8080
budgit: --addr "0.0.0.0:8080" is not loopback; budgit serves your finances with no auth.
```

If you genuinely need this (an SSH-tunnelled box, say), `BUDGIT_ALLOW_REMOTE=1`
overrides it — but understand that anyone who can reach that port can read every
transaction you have. Don't do it on shared Wi-Fi.

## Your data

One JSON file, `~/.budgit/budgit.json` by default. Override with `--file` on any
command or the `BUDGIT_FILE` environment variable.

```sh
budgit report --file ~/Dropbox/budget.json
BUDGIT_FILE=~/shared.json budgit serve
```

Writes are atomic (temp file + rename), so an interrupted command can't corrupt
it. Back it up by copying the file, or keep it in a private git repo — it's plain
text and diffs cleanly.

Because it's readable JSON, you can also hand-edit it or generate it from a bank
CSV export with a short script. IDs are repaired on load, so you can't break the
counters by editing by hand.

## Why not SQLite?

For one person with a few thousand transactions, SQLite's advantages — indexes,
concurrent writers, partial reads — never come into play, while its costs do:
`mattn/go-sqlite3` needs cgo (which breaks trivial cross-compilation) and
`modernc.org/sqlite` is a very large pure-Go dependency. A JSON file keeps the
project at zero dependencies, `CGO_ENABLED=0` everywhere, and greppable on disk.

If you ever outgrow it, the storage layer is confined to `store.go`.

## Tests

```sh
go test ./...
```

Covers money parsing/formatting round-trips, budget report arithmetic including
refunds and cross-month isolation, and the loopback guard.

## License

MIT — see [LICENSE](LICENSE).
