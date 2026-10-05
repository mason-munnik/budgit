# budgit

An open source and private personal budgeting tool that runs on your own computer. Your data stays in one file on your disk. Nothing is sent anywhere.

## Install

You need [Go](https://go.dev/dl/) installed. Nothing else.

```sh
git clone https://github.com/mason-munnik/budgit.git
cd budgit
go build -o budgit ./cmd/budgit
```

On Windows, name the output `budgit.exe` instead — `go build -o budgit ./cmd/budgit` writes exactly the name you give it, with no extension, and Windows will not run that file:

```powershell
go build -o budgit.exe ./cmd/budgit
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

Typed one in wrong? Fix it, or delete it, by ID — `budgit txn list` shows the IDs:

```sh
budgit txn edit 42 --amount 48.31 --desc "Trader Joe's"
budgit txn delete 42
```

`txn edit` changes only what you give it. An amount without a sign keeps the
direction the transaction already had, so correcting a refund leaves it a refund.

Commas only separate thousands (`1,299.00`). Write cents with a dot: `1,50` is
refused rather than read as $150.

The date defaults to today. You can leave out `--account` if you only have one.

## Import from your bank

Instead of typing each transaction, point budgit at a CSV your bank exported:

```sh
budgit txn import ~/Downloads/ExportedTransactions.csv --account "Chase Checking"
```

Every bank writes CSV differently, so budgit works out the delimiter, which line
the real header is on, and how the bank writes amounts. Look before you leap:

```sh
budgit txn import statement.csv --account "Chase Checking" --dry-run
```

That prints what it worked out and what it would add, and saves nothing.

**Running the same file twice is safe.** Rows are matched on the bank's own
transaction id, so a second import adds nothing and overlapping date ranges only
add what is new.

**Overlapping downloads are safe too.** If you pull September 1-30 and later
September 15 - October 15, the shared rows are recognised and imported once.

**Entering something by hand and importing it later is also safe.** A statement
row that lands on the same day for the same exact amount claims the transaction
you typed rather than adding a second copy. Your description and category are
kept; the row just gains the bank's id, so later imports recognise it outright.
Matching is one to one, so two genuine identical purchases on the same day stay
two transactions.

A few banks renumber their own transaction ids between exports, which nothing
can match on. Budgit keeps both rows in that case and tells you, because two
identical purchases in one day are perfectly real and only you can tell which it
was:

```
  possible duplicates  1

This row already had a transaction on the same day for the same amount:
  2026-09-05  Amazon        -$37.44   matches txn 1
Both were kept. Remove one with: budgit txn delete <id>
```

Some things worth knowing:

- Dates like `3/4/2026` are read month-first. If your bank writes them the other
  way round, pass `--date-format 02/01/2006`.
- Budgit tries to match the bank's category column to your own categories, by
  name and then by a few common aliases (`Gasoline/Fuel` finds `Gas`). Anything
  it cannot place is left uncategorized — `budgit txn list --uncategorized` finds
  them. Add your own with `--map "Restaurants & Dining=Eating-Out"`, or turn the
  whole thing off with `--no-category`. It never creates a category. Your own
  rules (below) are checked first; `--no-rules` skips them for one import.
- Pending transactions are skipped. They have no date yet and can still change.
- Credit card exports often write purchases as positive numbers. If budgit sees
  signs that disagree with the file's own type column it stops and asks, rather
  than reversing your money silently. Re-run with `--invert` or `--no-invert`.

If it guesses a column wrong, name it yourself:

```sh
budgit txn import statement.csv --account Amex     --date-col "Posted Date" --amount-col "Charge" --desc-col "Merchant"
```

`--debit-col` and `--credit-col` handle banks that split money in and money out
into two columns.

## Teach it your merchants

Categorize a merchant once and budgit remembers it:

```sh
budgit rule add --match "trader joe" --category Groceries
budgit rule add --match "amazon prime" --category Subscriptions
budgit rule add --match amazon --category Shopping
```

Any transaction whose description contains that text, in any case, lands in that
category. The text needs at least three letters, so a stray "a" cannot claim
every merchant you have. Rules apply when you import a statement, and when you add a
transaction without a category, both from the command line and the dashboard. A
rule beats the bank's own category column, because it is your decision about
that exact merchant.

When more than one rule matches, the longest text wins. That is why "AMAZON PRIME
MEMBERSHIP" goes to Subscriptions and every other Amazon charge goes to Shopping,
whatever order you added the rules in.

To file what is already sitting uncategorized:

```sh
budgit rule apply --dry-run   # see what it would do
budgit rule apply
```

This only touches uncategorized transactions, and never changes an amount's
sign, so a refund stays a refund. `budgit rule list` shows your rules and
`budgit rule delete <id>` removes one.

## Set budgets and see how you did

```sh
budgit budget set --category Groceries --month 2026-09 --amount 600
budgit report --month 2026-09
```

A budget carries forward: set Groceries to 600 in September and October,
November and every month after use 600 too, until you set a new amount. The
report marks a carried-over budget with the month it came from. To stop
budgeting a category, set it to 0 from the month it should end.

## Open the dashboard

```sh
budgit serve
```

Then open http://localhost:8080 in your browser. Press ctrl+c to stop it.

The dashboard shows your totals, how each category is doing against its budget, a spending chart, and the month's transactions.

You can also enter data there, so you do not have to keep typing commands:

- **Add a transaction** — the "+ Add transaction" button under the transactions list. The form stays open after each one and keeps the date and account, so a batch of receipts goes in quickly. The amount field follows the same rule as the CLI: unsigned takes its direction from the category, `+` in front records a refund.
- **Delete a transaction** — the × at the end of any row, then confirm.
- **Edit a transaction** — the ✎ at the end of any row changes its date, description, account and amount.
- **Change a transaction's category** — click the category on any row and pick a new one, or "(uncategorized)" to take it out. Tick "always file … here" first to also make a rule, trimming the text down to the merchant name.
- **Rules** — the Rules card lists them, adds new ones and deletes them. A rule added there files matching uncategorized transactions straight away.
- **Accounts** — "+ Add account", click a name to rename it, "Set" to set its balance, × to delete it once it has no transactions.
- **Set a budget** — click the figures on any row of "Budget vs actual". For a category with no row yet, use the "Set a budget" button. A budget starts in the month you are looking at and carries forward.
- **Categories** — "+ Add category", and "Manage categories" to rename or delete them.

Budgeting an income category is still done from the command line. Changes made in the browser are written straight to your data file, so `budgit report` sees them immediately. If the command line changes the file at the same moment, the dashboard says so and saves nothing; do it again.

The dashboard binds to localhost only and has no password. It refuses any request that did not come from its own page or that names a host other than localhost, and it loads nothing from the internet.

To open it from another device on your network, you have to ask twice and set a password:

```sh
export BUDGIT_TOKEN=$(openssl rand -hex 16)
BUDGIT_ALLOW_REMOTE=1 budgit serve --addr 192.168.1.5:8080
```

The browser asks for a username and password; type anything as the username and the token as the password. The connection is plain HTTP, so only do this on a network you trust.

## Moving a transaction between categories

```sh
budgit txn categorize 42 Groceries
```

If the new category points money the other way, the amount flips to match — moving a purchase into an income category makes it an inflow. If both categories are the same kind the amount is left alone, so a refund filed under the wrong expense category stays a refund. Uncategorized counts as an expense here, so filing an uncategorized refund keeps it a refund too.

`budgit txn categorize 42 --none` takes it back out of its category.

## Renaming and deleting

```sh
budgit account rename "Amex" --name "Amex Gold"
budgit category rename Dining --name "Eating Out"
budgit category delete Coffee
budgit account delete "Old Savings"
```

Deleting a category leaves its transactions uncategorized and removes its
budgets and rules. An account can only be deleted once it has no transactions,
so its money never silently drops out of your balances.

## All the commands

```
budgit account add          add an account
budgit account list         list accounts and balances
budgit account set-balance  set what an account holds right now
budgit account rename       rename an account
budgit account delete       delete an account with no transactions
budgit category add         add a category
budgit category list        list categories
budgit category rename      rename a category
budgit category delete      delete a category
budgit txn add              record a transaction
budgit txn list             list transactions
budgit txn edit             change a transaction's date, account, description or amount
budgit txn categorize       change a transaction's category
budgit txn delete           delete a transaction
budgit txn import           import transactions from a bank CSV
budgit rule add             file a merchant under a category automatically
budgit rule list            list rules
budgit rule delete          delete a rule
budgit rule apply           categorize past uncategorized transactions by rule
budgit budget set           set a monthly budget, carried forward
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
