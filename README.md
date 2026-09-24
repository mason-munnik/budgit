# budgit

An open source and private personal budgeting tool that runs on your own computer. Your data stays in one file on your disk. Nothing is sent anywhere.

## Install

You need [Go](https://go.dev/dl/) installed. Nothing else.

```sh
git clone https://github.com/mason-munnik/budgit.git
cd budgit
go build -o budgit .
```

On Windows, name the output `budgit.exe` instead — `go build -o budgit .` writes exactly the name you give it, with no extension, and Windows will not run that file:

```powershell
go build -o budgit.exe .
```

That is the whole install. To run it from anywhere:

**macOS and Linux**

```sh
sudo mv budgit /usr/local/bin/
```

**Windows**

Put the exe in a folder of your own and add that folder to your `PATH` once:

```powershell
mkdir "$env:LOCALAPPDATA\Programs\budgit"
move budgit.exe "$env:LOCALAPPDATA\Programs\budgit\"
[Environment]::SetEnvironmentVariable(
  "Path", $env:Path + ";$env:LOCALAPPDATA\Programs\budgit", "User")
```

Open a new terminal afterwards. Every example below then works as written in PowerShell or Command Prompt — you type `budgit`, not `budgit.exe`. The one difference is line continuations: where an example breaks a long command across lines with a trailing `\`, use a backtick `` ` `` in PowerShell, a `^` in Command Prompt, or just put the whole command on one line.

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

Typed one in wrong? Delete it by ID — `budgit txn list` shows the IDs:

```sh
budgit txn delete 42
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

The dashboard shows your totals, how each category is doing against its budget, a spending chart, and the month's transactions.

You can also enter data there, so you do not have to keep typing commands:

- **Add a transaction** — the "+ Add transaction" button under the transactions list. The form stays open after each one and keeps the date and account, so a batch of receipts goes in quickly. The amount field follows the same rule as the CLI: unsigned takes its direction from the category, `+` in front records a refund.
- **Delete a transaction** — the × at the end of any row, then confirm.
- **Change a transaction's category** — click the category on any row and pick a new one.
- **Set an account balance** — the "Set" button beside any balance.
- **Set a budget** — click the figures on any row of "Budget vs actual". For a category with no row yet, use the "Set a budget" button. Budgets are per month, and apply to the month you are looking at.
- **Add a category** — the "+ Add category" button on the same card.

Adding accounts is still done from the command line, as is budgeting an income category. Changes made in the browser are written straight to your data file, so `budgit report` sees them immediately.

The dashboard binds to localhost only and has no password, so it refuses any request that did not come from its own page.

## Moving a transaction between categories

```sh
budgit txn categorize 42 Groceries
```

If the new category points money the other way, the amount flips to match — moving a purchase into an income category makes it an inflow. If both categories are the same kind the amount is left alone, so a refund filed under the wrong expense category stays a refund.

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
budgit txn delete           delete a transaction
budgit budget set           set a monthly budget
budgit budget list          list budgets
budgit report               budget vs actual for a month
budgit serve                open the dashboard
```

Run `budgit help` for the full list of options.

You can use a short name instead of the full one. `--category Dining` will find "Dining Out".

## Where your data lives

Everything is in one file: `~/.budgit/budgit.json` on macOS and Linux, and `%USERPROFILE%\.budgit\budgit.json` on Windows. To back it up, copy that file somewhere safe.

```sh
cp ~/.budgit/budgit.json ~/Dropbox/budgit-backup.json
```

```powershell
copy "$env:USERPROFILE\.budgit\budgit.json" "$env:USERPROFILE\Dropbox\budgit-backup.json"
```

Set `BUDGIT_FILE` to keep the file somewhere else entirely.

To start over, delete it. The next command makes a fresh one.

## License

MIT. See [LICENSE](LICENSE).
