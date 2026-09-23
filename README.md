# budgit

An open source and private personal budgeting tool that runs on your own computer. Your data stays in one file on your disk. Nothing is sent anywhere.

## Install

You need [Go](https://go.dev/dl/) installed. Nothing else.

```sh
git clone https://github.com/mason-munnik/budgit.git
cd budgit
go build -o budgit .
```

That is the whole install. To run it from anywhere:

```sh
sudo mv budgit /usr/local/bin/
```

## Set up your accounts and categories

```sh
budgit account add --name "Chase Checking" --type checking
budgit account add --name "Amex Gold" --type credit

budgit category add --name Salary --kind income
budgit category add --name Rent --kind expense
budgit category add --name Groceries --kind expense
```

Tell it what each account holds right now:

```sh
budgit account set-balance --account "Chase Checking" --amount 8500
budgit account set-balance --account "Amex Gold" --amount -499.50
```

Use a negative amount for money you owe, like a credit card.

## Add transactions

```sh
budgit txn add --date 2026-09-04 --account "Chase Checking" \
               --category Groceries --desc "Trader Joes" --amount 84.31
```

You do not need to write a minus sign. If the category is an expense, the money goes out. If it is income, the money comes in. To record a refund, put a plus sign in front:

```sh
budgit txn add --category Groceries --desc "Returned milk" --amount +12.49
```

The date defaults to today. You can leave out `--account` if you only have one.

## Set budgets and see how you did

```sh
budgit budget set --category Groceries --month 2026-09 --amount 600
budgit report --month 2026-09
```

Budgets are set one month at a time. October needs its own lines.

## Open the dashboard

```sh
budgit serve
```

Then open http://localhost:8080 in your browser. Press ctrl+c to stop it.

The dashboard shows your totals, how each category is doing against its budget, a spending chart, and the month's transactions. It is read only. To change anything, use the commands above.

## All the commands

```
budgit account add          add an account
budgit account list         list accounts and balances
budgit account set-balance  set what an account holds right now
budgit category add         add a category
budgit category list        list categories
budgit txn add              record a transaction
budgit txn list             list transactions
budgit txn categorize       change a transaction's category
budgit budget set           set a monthly budget
budgit budget list          list budgets
budgit report               budget vs actual for a month
budgit serve                open the dashboard
```

Run `budgit help` for the full list of options.

You can use a short name instead of the full one. `--category Dining` will find "Dining Out".

## Where your data lives

Everything is in `~/.budgit/budgit.json`. To back it up, copy that file somewhere safe.

```sh
cp ~/.budgit/budgit.json ~/Dropbox/budgit-backup.json
```

To start over, delete it. The next command makes a fresh one.

## License

MIT. See [LICENSE](LICENSE).
