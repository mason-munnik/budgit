package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

const usage = `budgit — a local-only personal budgeting tool

Usage:
  budgit <command> [subcommand] [flags]

Commands:
  account add   --name NAME [--type TYPE]
  account list
  category add  --name NAME --kind income|expense
  category list
  txn add       --amount AMT [--date YYYY-MM-DD] [--account REF] [--category REF] [--desc TEXT]
  txn list      [--month YYYY-MM] [--account REF] [--category REF] [--uncategorized] [--limit N]
  txn categorize ID CATEGORY
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
		a := Account{ID: db.NextAccountID, Name: strings.TrimSpace(*name), Type: *typ}
		db.NextAccountID++
		db.Accounts = append(db.Accounts, a)
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Added account %d: %s (%s)\n", a.ID, a.Name, a.Type)
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
		// Running balance per account is just the sum of its transactions.
		bal := map[int]int64{}
		for _, t := range db.Transactions {
			bal[t.AccountID] += t.AmountCents
		}
		w := out()
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tBALANCE")
		for _, a := range db.Accounts {
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", a.ID, a.Name, a.Type, FormatMoney(bal[a.ID]))
		}
		return w.Flush()
	}
	return fmt.Errorf("unknown account subcommand %q (want: add, list)", action)
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
		k := strings.ToLower(strings.TrimSpace(*kind))
		if k != KindIncome && k != KindExpense {
			return fmt.Errorf("--kind must be %q or %q, got %q", KindIncome, KindExpense, *kind)
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		for _, c := range db.Categories {
			if strings.EqualFold(c.Name, *name) {
				return fmt.Errorf("category %q already exists (id %d)", c.Name, c.ID)
			}
		}
		c := Category{ID: db.NextCategoryID, Name: strings.TrimSpace(*name), Kind: k}
		db.NextCategoryID++
		db.Categories = append(db.Categories, c)
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
		d, err := ValidateDate(*date)
		if err != nil {
			return err
		}
		db, err := Load(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*acct) == "" {
			if len(db.Accounts) != 1 {
				return fmt.Errorf("txn add requires --account")
			}
			*acct = db.Accounts[0].Name // unambiguous when there is exactly one
		}
		a, err := db.FindAccount(*acct)
		if err != nil {
			return err
		}
		cents, explicit, err := ParseMoney(*amount)
		if err != nil {
			return err
		}

		catID := 0
		if strings.TrimSpace(*cat) != "" {
			c, err := db.FindCategory(*cat)
			if err != nil {
				return err
			}
			catID = c.ID
			if !explicit {
				// Unsigned: let the category decide which way the money moved.
				if c.Kind == KindExpense {
					cents = -abs(cents)
				} else {
					cents = abs(cents)
				}
			}
		} else if !explicit {
			// No category to infer from; an unsigned amount is assumed spending.
			cents = -abs(cents)
		}

		t := Transaction{
			ID: db.NextTransactionID, Date: d, AccountID: a.ID,
			CategoryID: catID, Description: strings.TrimSpace(*desc), AmountCents: cents,
		}
		db.NextTransactionID++
		db.Transactions = append(db.Transactions, t)
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Added txn %d: %s  %s  %s  %s  [%s]\n",
			t.ID, t.Date, FormatMoney(t.AmountCents), a.Name, t.Description, db.CategoryName(t.CategoryID))
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

	case "categorize", "recategorize", "cat":
		// Positional: txn categorize ID CATEGORY  (flags may follow)
		var pos []string
		var flags []string
		for i, a := range rest {
			if strings.HasPrefix(a, "-") {
				flags = rest[i:]
				break
			}
			pos = append(pos, a)
		}
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
		t := db.FindTransaction(id)
		if t == nil {
			return fmt.Errorf("no transaction with id %d", id)
		}
		c, err := db.FindCategory(strings.Join(pos[1:], " "))
		if err != nil {
			return err
		}
		was := db.CategoryName(t.CategoryID)
		t.CategoryID = c.ID
		// Recategorising across kinds flips which way the money should point.
		if c.Kind == KindExpense && t.AmountCents > 0 {
			t.AmountCents = -t.AmountCents
		} else if c.Kind == KindIncome && t.AmountCents < 0 {
			t.AmountCents = -t.AmountCents
		}
		if err := db.Save(); err != nil {
			return err
		}
		fmt.Printf("Txn %d: %s -> %s  (%s %s)\n", t.ID, was, c.Name, t.Date, FormatMoney(t.AmountCents))
		return nil
	}
	return fmt.Errorf("unknown txn subcommand %q (want: add, list, categorize)", action)
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
		m, err := ValidateMonth(*month)
		if err != nil {
			return err
		}
		cents, _, err := ParseMoney(*amount)
		if err != nil {
			return err
		}
		cents = abs(cents) // a budget is an allowance, always positive
		db, err := Load(path)
		if err != nil {
			return err
		}
		c, err := db.FindCategory(*cat)
		if err != nil {
			return err
		}
		db.SetBudget(c.ID, m, cents)
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
