package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

const usage = `budgit — a local-only personal budgeting tool

Usage:
  budgit <command> [subcommand] [flags]

Commands:
  account add   --name NAME [--type TYPE] [--balance AMT]
  account list
  account set-balance --account REF --amount AMT
  category add  --name NAME --kind income|expense
  category list
  txn add       --amount AMT [--date YYYY-MM-DD] [--account REF] [--category REF] [--desc TEXT]
  txn list      [--month YYYY-MM] [--account REF] [--category REF] [--uncategorized] [--limit N]
  txn categorize ID CATEGORY
  txn delete    ID
  txn import    PATH.csv --account REF [--dry-run] [--invert|--no-invert]
  budget set    --category REF --amount AMT [--month YYYY-MM]
  budget list   [--month YYYY-MM]
  report        [--month YYYY-MM]
  serve         [--addr localhost:8080]

Amounts:
  Signed cents internally; never floats. Write amounts as 84.31, $84.31 or 1,299.
  An UNSIGNED amount takes its direction from the category: expense categories
  record an outflow, income categories an inflow. An explicit +/- always wins,
  so a refund into an expense category is "--amount +24.99".

Accounts and categories may be referenced by name (case-insensitive, unique
substring is enough) or by numeric ID.

Importing:
  "txn import" reads a CSV exported from your bank. It works out the delimiter,
  the header row and how the bank writes amounts; --dry-run shows you what it
  worked out without saving. Slashed dates are read month-first (US style).
  Re-running the same file imports nothing: rows are matched on the bank's own
  transaction id. Name columns yourself with --date-col, --amount-col,
  --debit-col, --credit-col, --desc-col, --category-col, --id-col, --type-col.

Data lives in ~/.budgit/budgit.json — override with --file or $BUDGIT_FILE.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "account":
		err = cmdAccount(os.Args[2:])
	case "category":
		err = cmdCategory(os.Args[2:])
	case "txn", "tx", "transaction":
		err = cmdTxn(os.Args[2:])
	case "budget":
		err = cmdBudget(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "budgit: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "budgit: "+err.Error())
		os.Exit(1)
	}
}

// newFS builds a subcommand flag set that always understands --file.
func newFS(name string, path *string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.StringVar(path, "file", DefaultPath(), "path to the budgit data file")
	return fs
}

// splitPositional divides args at the first flag, so a subcommand can take
// positional arguments and still understand --file after them.
func splitPositional(args []string) (pos, flags []string) {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			return pos, args[i:]
		}
		pos = append(pos, a)
	}
	return pos, nil
}

func sub(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], args[1:]
}

func out() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

// ---- account ----

func cmdAccount(args []string) error {
	action, rest := sub(args)
	var path string
	switch action {
	case "add":
		fs := newFS("account add", &path)
		name := fs.String("name", "", "account name (required)")
		typ := fs.String("type", "checking", "account type: checking, savings, credit, cash")
		balance := fs.String("balance", "", "current balance before any transactions, e.g. 4200 or -499.50 for a card you owe on")
		fs.Parse(rest)
		if strings.TrimSpace(*name) == "" {
			return fmt.Errorf("account add requires --name")
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		for _, a := range db.Accounts {
			if strings.EqualFold(a.Name, *name) {
				return fmt.Errorf("account %q already exists (id %d)", a.Name, a.ID)
			}
		}
		var opening int64
		if strings.TrimSpace(*balance) != "" {
			// Always explicit here: there is no category to infer a sign from,
			// so "-499.50" means you owe and "4200" means you hold.
			opening, _, err = ParseMoney(*balance)
			if err != nil {
				return err
			}
		}
		a := Account{ID: db.NextAccountID, Name: strings.TrimSpace(*name), Type: *typ, OpeningBalanceCents: opening}
		db.NextAccountID++
		db.Accounts = append(db.Accounts, a)
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Added account %d: %s (%s), opening balance %s\n", a.ID, a.Name, a.Type, FormatMoney(a.OpeningBalanceCents))
		return nil

	case "list", "ls", "":
		fs := newFS("account list", &path)
		fs.Parse(rest)
		db, err := Load(path)
		if err != nil {
			return err
		}
		if len(db.Accounts) == 0 {
			fmt.Println("No accounts yet. Add one: budgit account add --name \"Chase Checking\"")
			return nil
		}
		w := out()
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tOPENING\tBALANCE")
		var total int64
		for _, a := range db.Accounts {
			b := db.AccountBalance(a.ID)
			total += b
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", a.ID, a.Name, a.Type,
				FormatMoney(a.OpeningBalanceCents), FormatMoney(b))
		}
		fmt.Fprintf(w, "\t\t\t\t\n")
		fmt.Fprintf(w, "\tNET WORTH\t\t\t%s\n", FormatMoney(total))
		return w.Flush()
	case "set-balance", "balance":
		fs := newFS("account set-balance", &path)
		acct := fs.String("account", "", "account name or ID (required)")
		amount := fs.String("amount", "", "the balance the account should show right now (required)")
		fs.Parse(rest)
		if strings.TrimSpace(*acct) == "" || strings.TrimSpace(*amount) == "" {
			return fmt.Errorf("account set-balance requires --account and --amount")
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		a, fromTxns, err := SetAccountBalance(db, *acct, *amount)
		if err != nil {
			return err
		}
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("%s balance set to %s (opening %s + %s in transactions)\n",
			a.Name, FormatMoney(db.AccountBalance(a.ID)),
			FormatMoney(a.OpeningBalanceCents), FormatMoney(fromTxns))
		return nil
	}
	return fmt.Errorf("unknown account subcommand %q (want: add, list, set-balance)", action)
}

// ---- category ----

func cmdCategory(args []string) error {
	action, rest := sub(args)
	var path string
	switch action {
	case "add":
		fs := newFS("category add", &path)
		name := fs.String("name", "", "category name (required)")
		kind := fs.String("kind", KindExpense, "income or expense")
		fs.Parse(rest)
		if strings.TrimSpace(*name) == "" {
			return fmt.Errorf("category add requires --name")
		}
		// Checked here rather than in AddCategory so the message names the flag.
		k := strings.ToLower(strings.TrimSpace(*kind))
		if k != KindIncome && k != KindExpense {
			return fmt.Errorf("--kind must be %q or %q, got %q", KindIncome, KindExpense, *kind)
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		c, err := AddCategory(db, *name, k)
		if err != nil {
			return err
		}
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Added category %d: %s (%s)\n", c.ID, c.Name, c.Kind)
		return nil

	case "list", "ls", "":
		fs := newFS("category list", &path)
		fs.Parse(rest)
		db, err := Load(path)
		if err != nil {
			return err
		}
		if len(db.Categories) == 0 {
			fmt.Println("No categories yet. Add one: budgit category add --name Groceries --kind expense")
			return nil
		}
		w := out()
		fmt.Fprintln(w, "ID\tNAME\tKIND")
		for _, c := range db.Categories {
			fmt.Fprintf(w, "%d\t%s\t%s\n", c.ID, c.Name, c.Kind)
		}
		return w.Flush()
	}
	return fmt.Errorf("unknown category subcommand %q (want: add, list)", action)
}

// ---- transactions ----

func cmdTxn(args []string) error {
	action, rest := sub(args)
	var path string
	switch action {
	case "add":
		fs := newFS("txn add", &path)
		date := fs.String("date", Today(), "transaction date, YYYY-MM-DD")
		acct := fs.String("account", "", "account name or ID (required)")
		cat := fs.String("category", "", "category name or ID (optional; blank leaves it uncategorized)")
		desc := fs.String("desc", "", "description")
		amount := fs.String("amount", "", "amount, e.g. 84.31 or +24.99 (required)")
		fs.Parse(rest)

		if strings.TrimSpace(*amount) == "" {
			return fmt.Errorf("txn add requires --amount")
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		// Checked here rather than in AddTransaction so the message can name the flag.
		if strings.TrimSpace(*acct) == "" && len(db.Accounts) != 1 {
			return fmt.Errorf("txn add requires --account")
		}
		t, err := AddTransaction(db, *date, *acct, *cat, *desc, *amount)
		if err != nil {
			return err
		}
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Added txn %d: %s  %s  %s  %s  [%s]\n",
			t.ID, t.Date, FormatMoney(t.AmountCents), db.AccountName(t.AccountID),
			t.Description, db.CategoryName(t.CategoryID))
		return nil

	case "list", "ls", "":
		fs := newFS("txn list", &path)
		month := fs.String("month", "", "filter to a month, YYYY-MM")
		acct := fs.String("account", "", "filter by account")
		cat := fs.String("category", "", "filter by category")
		unc := fs.Bool("uncategorized", false, "only uncategorized transactions")
		limit := fs.Int("limit", 50, "max rows to print (0 = all)")
		fs.Parse(rest)

		db, err := Load(path)
		if err != nil {
			return err
		}
		var wantAcct, wantCat int
		if strings.TrimSpace(*acct) != "" {
			a, err := db.FindAccount(*acct)
			if err != nil {
				return err
			}
			wantAcct = a.ID
		}
		if strings.TrimSpace(*cat) != "" {
			c, err := db.FindCategory(*cat)
			if err != nil {
				return err
			}
			wantCat = c.ID
		}
		if *month != "" {
			if *month, err = ValidateMonth(*month); err != nil {
				return err
			}
		}

		db.SortTransactions()
		var rows []Transaction
		var total int64
		for _, t := range db.Transactions {
			if *month != "" && MonthOf(t.Date) != *month {
				continue
			}
			if wantAcct != 0 && t.AccountID != wantAcct {
				continue
			}
			if wantCat != 0 && t.CategoryID != wantCat {
				continue
			}
			if *unc && t.CategoryID != 0 {
				continue
			}
			rows = append(rows, t)
			total += t.AmountCents
		}
		if len(rows) == 0 {
			fmt.Println("No matching transactions.")
			return nil
		}
		shown := rows
		if *limit > 0 && len(rows) > *limit {
			shown = rows[:*limit]
		}
		w := out()
		fmt.Fprintln(w, "ID\tDATE\tACCOUNT\tCATEGORY\tDESCRIPTION\tAMOUNT")
		for _, t := range shown {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
				t.ID, t.Date, db.AccountName(t.AccountID), db.CategoryName(t.CategoryID),
				truncate(t.Description, 32), FormatMoney(t.AmountCents))
		}
		w.Flush()
		if len(shown) < len(rows) {
			fmt.Printf("\n%d of %d shown (--limit 0 for all). Net of all %d: %s\n",
				len(shown), len(rows), len(rows), FormatMoney(total))
		} else {
			fmt.Printf("\n%d transactions, net %s\n", len(rows), FormatMoney(total))
		}
		return nil

	case "delete", "del", "rm":
		// Positional: txn delete ID  (flags may follow)
		pos, flags := splitPositional(rest)
		fs := newFS("txn delete", &path)
		fs.Parse(flags)
		if len(pos) != 1 {
			return fmt.Errorf("usage: budgit txn delete <txn-id>")
		}
		id, err := strconv.Atoi(pos[0])
		if err != nil {
			return fmt.Errorf("transaction id %q is not a number", pos[0])
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		gone, err := DeleteTransaction(db, id)
		if err != nil {
			return err
		}
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Deleted txn %d: %s  %s  %s  %s  [%s]\n",
			gone.ID, gone.Date, FormatMoney(gone.AmountCents), db.AccountName(gone.AccountID),
			gone.Description, db.CategoryName(gone.CategoryID))
		return nil

	case "import":
		pos, flags := splitPositional(rest)
		fs := newFS("txn import", &path)
		var opts importOptions
		opts.Cols = map[string]string{}
		acct := fs.String("account", "", "account the file belongs to (required unless you have exactly one)")
		fs.BoolVar(&opts.DryRun, "dry-run", false, "show what would be imported, save nothing")
		fs.BoolVar(&opts.Invert, "invert", false, "flip every sign (card exports where purchases are positive)")
		fs.BoolVar(&opts.NoInvert, "no-invert", false, "confirm the signs are already correct")
		fs.BoolVar(&opts.NoCategory, "no-category", false, "ignore the file's category column")
		fs.Var((*stringList)(&opts.Maps), "map", "map a bank category onto one of yours, e.g. \"Restaurants & Dining=Eating-Out\" (repeatable)")
		fs.StringVar(&opts.Delimiter, "delimiter", "", "field separator: , ; tab pipe (default: sniffed)")
		fs.StringVar(&opts.Decimal, "decimal", "", "decimal separator: dot or comma (default: sniffed)")
		fs.StringVar(&opts.DateFormat, "date-format", "", "Go time layout, e.g. 02/01/2006 for day-first dates")
		for _, c := range []struct{ flag, help string }{
			{"date", "date"}, {"amount", "signed amount"}, {"debit", "debit"}, {"credit", "credit"},
			{"desc", "description"}, {"category", "category"}, {"id", "unique id"}, {"type", "debit/credit type"},
		} {
			field := c.flag
			if field == "id" {
				field = "extid"
			}
			fs.Var(colFlag{opts.Cols, field}, c.flag+"-col", "name of the "+c.help+" column")
		}
		fs.Parse(flags)

		if len(pos) != 1 {
			return fmt.Errorf("usage: budgit txn import <file.csv> --account REF")
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*acct) == "" && len(db.Accounts) != 1 {
			return fmt.Errorf("txn import requires --account")
		}
		if strings.TrimSpace(*acct) == "" {
			*acct = db.Accounts[0].Name
		}
		a, err := db.FindAccount(*acct)
		if err != nil {
			return err
		}
		res, err := ImportCSV(db, pos[0], a.ID, opts)
		if err != nil {
			return err
		}
		if !opts.DryRun && res.Imported > 0 {
			if err := db.Save(); err != nil {
				return err
			}
		}
		printImport(db, a, res, opts.DryRun)
		return nil

	case "categorize", "recategorize", "cat":
		// Positional: txn categorize ID CATEGORY  (flags may follow)
		pos, flags := splitPositional(rest)
		fs := newFS("txn categorize", &path)
		fs.Parse(flags)
		if len(pos) < 2 {
			return fmt.Errorf("usage: budgit txn categorize <txn-id> <category>")
		}
		id, err := strconv.Atoi(pos[0])
		if err != nil {
			return fmt.Errorf("transaction id %q is not a number", pos[0])
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		t, was, err := CategorizeTransaction(db, id, strings.Join(pos[1:], " "))
		if err != nil {
			return err
		}
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Txn %d: %s -> %s  (%s %s)\n",
			t.ID, was, db.CategoryName(t.CategoryID), t.Date, FormatMoney(t.AmountCents))
		return nil
	}
	return fmt.Errorf("unknown txn subcommand %q (want: add, list, categorize, delete, import)", action)
}

// stringList collects a flag given more than once, like --map.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ", ") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// colFlag writes one --*-col override into the shared map.
type colFlag struct {
	into  map[string]string
	field string
}

func (c colFlag) String() string     { return c.into[c.field] }
func (c colFlag) Set(v string) error { c.into[c.field] = v; return nil }

// printImport reports what the file turned out to be and what came of it.
// Every skipped row is accounted for: a silent drop is how an import quietly
// loses a paycheck.
func printImport(db *DB, a *Account, res *importResult, dry bool) {
	fmt.Println(res.Dialect.Summary())
	fmt.Printf("\nRead %d rows from %s\n\n", res.Rows, filepath.Base(res.Path))

	if dry {
		for _, p := range res.Preview {
			fmt.Printf("  %s  %-34s %12s  %s\n",
				p.Date, truncate(p.Desc, 34), FormatMoney(p.Cents), p.Category)
		}
		if n := res.Imported - len(res.Preview); n > 0 {
			fmt.Printf("  ... and %d more\n", n)
		}
		fmt.Println()
	}

	verb := "imported"
	if dry {
		verb = "would import"
	}
	w := out()
	fmt.Fprintf(w, "  %s\t%d\n", verb, res.Imported)
	if skipped := res.Pending + res.Junk + res.Zero; skipped > 0 {
		fmt.Fprintf(w, "  skipped\t%d\t(%s)\n", skipped, skipDetail(res))
	}
	if res.Duplicates > 0 {
		fmt.Fprintf(w, "  already present\t%d\n", res.Duplicates)
	}
	if res.Matched > 0 {
		fmt.Fprintf(w, "  matched\t%d\t%s already recorded, same day and amount\n",
			res.Matched, plural(res.Matched, "row", "rows"))
	}
	fmt.Fprintf(w, "  categorized\t%d\n", res.Categorized)
	if unc := res.Imported - res.Categorized; unc > 0 {
		fmt.Fprintf(w, "  uncategorized\t%d\trun: budgit txn list --uncategorized\n", unc)
	}
	if res.AssumedOut > 0 {
		fmt.Fprintf(w, "  assumed outgoing\t%d\tunrecognised type, treated as spending\n", res.AssumedOut)
	}
	if n := len(res.Suspects); n > 0 {
		fmt.Fprintf(w, "  possible duplicates\t%d\tsee below\n", n)
	}
	w.Flush()

	// Imported, not skipped: only you can tell a renumbered export from two
	// genuine purchases of the same amount on the same day.
	if len(res.Suspects) > 0 {
		fmt.Printf("\n%s already had a transaction on the same day for the same amount:\n",
			plural(len(res.Suspects), "This row", "These rows"))
		for _, s := range res.Suspects {
			fmt.Printf("  %s  %-30s %10s   matches txn %d\n",
				s.Date, truncate(s.Desc, 30), FormatMoney(s.Cents), s.ExistingID)
		}
		fmt.Println("Both were kept. Remove one with: budgit txn delete <id>")
	}

	if !dry && res.Imported > 0 {
		fmt.Printf("\n%s balance is now %s\n", a.Name, FormatMoney(db.AccountBalance(a.ID)))
	}
}

func skipDetail(res *importResult) string {
	var parts []string
	if res.Pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", res.Pending))
	}
	if res.Junk > 0 {
		parts = append(parts, fmt.Sprintf("%d with no date or amount", res.Junk))
	}
	if res.Zero > 0 {
		parts = append(parts, fmt.Sprintf("%d zero", res.Zero))
	}
	return strings.Join(parts, ", ")
}

// ---- budget ----

func cmdBudget(args []string) error {
	action, rest := sub(args)
	var path string
	switch action {
	case "set":
		fs := newFS("budget set", &path)
		cat := fs.String("category", "", "category name or ID (required)")
		month := fs.String("month", CurrentMonth(), "month, YYYY-MM")
		amount := fs.String("amount", "", "monthly allowance, e.g. 600 (required)")
		fs.Parse(rest)
		if strings.TrimSpace(*cat) == "" || strings.TrimSpace(*amount) == "" {
			return fmt.Errorf("budget set requires --category and --amount")
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		c, m, cents, err := SetCategoryBudget(db, *cat, *month, *amount)
		if err != nil {
			return err
		}
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Budget set: %s %s = %s\n", c.Name, m, FormatMoney(cents))
		return nil

	case "list", "ls", "":
		fs := newFS("budget list", &path)
		month := fs.String("month", CurrentMonth(), "month, YYYY-MM")
		fs.Parse(rest)
		m, err := ValidateMonth(*month)
		if err != nil {
			return err
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		var total int64
		w := out()
		fmt.Fprintln(w, "CATEGORY\tKIND\tBUDGET")
		n := 0
		for _, c := range db.Categories {
			if b, ok := db.BudgetFor(c.ID, m); ok {
				fmt.Fprintf(w, "%s\t%s\t%s\n", c.Name, c.Kind, FormatMoney(b))
				total += b
				n++
			}
		}
		if n == 0 {
			fmt.Printf("No budgets set for %s.\n", m)
			return nil
		}
		fmt.Fprintf(w, "\t\t\n")
		fmt.Fprintf(w, "TOTAL\t\t%s\n", FormatMoney(total))
		return w.Flush()
	}
	return fmt.Errorf("unknown budget subcommand %q (want: set, list)", action)
}

// ---- report ----

func cmdReport(args []string) error {
	var path string
	fs := newFS("report", &path)
	month := fs.String("month", CurrentMonth(), "month, YYYY-MM")
	fs.Parse(args)
	m, err := ValidateMonth(*month)
	if err != nil {
		return err
	}
	db, err := Load(path)
	if err != nil {
		return err
	}
	rep := BuildReport(db, m)

	fmt.Printf("Budget vs actual — %s\n\n", m)
	if len(rep.Expenses) == 0 && len(rep.Income) == 0 && rep.UncategorizedCount == 0 {
		fmt.Println("Nothing recorded for this month.")
		return nil
	}

	if len(rep.Expenses) > 0 {
		fmt.Println("EXPENSES")
		w := out()
		fmt.Fprintln(w, "  CATEGORY\tBUDGET\tACTUAL\tREMAINING\tUSED\t")
		for _, r := range rep.Expenses {
			budget, remain, used := "—", "—", ""
			if r.HasBudget {
				budget = FormatMoney(r.BudgetCents)
				remain = FormatMoney(r.RemainCents)
				used = fmt.Sprintf("%3.0f%% %s", r.PercentUsed, bar(r.PercentUsed))
				if r.OverBudget {
					used += " OVER"
				}
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t\n", r.Category, budget, FormatMoney(r.ActualCents), remain, used)
		}
		fmt.Fprintf(w, "  \t\t\t\t\t\n")
		totalRemain := rep.TotalBudgetCents - rep.TotalSpentCents
		totalUsed := 0.0
		if rep.TotalBudgetCents > 0 {
			totalUsed = float64(rep.TotalSpentCents) / float64(rep.TotalBudgetCents) * 100
		}
		fmt.Fprintf(w, "  TOTAL\t%s\t%s\t%s\t%3.0f%% %s\t\n",
			FormatMoney(rep.TotalBudgetCents), FormatMoney(rep.TotalSpentCents),
			FormatMoney(totalRemain), totalUsed, bar(totalUsed))
		w.Flush()
		fmt.Println()
	}

	if len(rep.Income) > 0 {
		fmt.Println("INCOME")
		w := out()
		fmt.Fprintln(w, "  CATEGORY\tRECEIVED\t")
		for _, r := range rep.Income {
			fmt.Fprintf(w, "  %s\t%s\t\n", r.Category, FormatMoney(r.ActualCents))
		}
		fmt.Fprintf(w, "  TOTAL\t%s\t\n", FormatMoney(rep.TotalIncomeCents))
		w.Flush()
		fmt.Println()
	}

	if rep.UncategorizedCount > 0 {
		fmt.Printf("%d uncategorized transaction(s), net %s — run: budgit txn list --uncategorized\n\n",
			rep.UncategorizedCount, FormatMoney(rep.UncategorizedCents))
	}

	fmt.Printf("NET (income − spend): %s\n", FormatMoney(rep.NetCents))
	return nil
}

// bar renders a 10-cell usage meter. Over-budget fills completely.
func bar(pct float64) string {
	const width = 10
	filled := int(pct / 100 * width)
	if filled < 0 {
		filled = 0
	}
	// Any real spending shows at least one cell, so a bar is never empty
	// while the percentage beside it reads non-zero.
	if filled == 0 && pct > 0 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat("·", width-filled) + "]"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
