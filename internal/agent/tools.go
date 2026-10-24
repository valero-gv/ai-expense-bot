package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"expensebot/internal/domain"
)

type filterArgs struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Period   string `json:"period"`
	Category string `json:"category"`
	Query    string `json:"query"`
	Page     int    `json:"page"`
}
type selector struct {
	ID     int64  `json:"id"`
	Query  string `json:"query"`
	Latest bool   `json:"latest"`
}
type createArgs struct {
	Amount   domain.Money `json:"amount"`
	Category string       `json:"category"`
	SpentAt  string       `json:"spent_at"`
	Comment  string       `json:"comment"`
}
type getArgs struct {
	ID int64 `json:"id"`
}
type latestArgs struct {
	Count int `json:"count"`
}
type deleteArgs struct {
	Selector selector `json:"selector"`
}
type updateArgs struct {
	Selector selector      `json:"selector"`
	Amount   *domain.Money `json:"amount"`
	Category *string       `json:"category"`
	SpentAt  *string       `json:"spent_at"`
	Comment  *string       `json:"comment"`
}

func object(props map[string]any) map[string]any {
	required := make([]string, 0, len(props))
	// Fixed lexical order makes tool specifications and tests reproducible.
	for _, key := range []string{"amount", "category", "comment", "count", "from", "id", "latest", "page", "period", "query", "selector", "spent_at", "to"} {
		if _, ok := props[key]; ok {
			required = append(required, key)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func nullable(kind, description string) map[string]any {
	return map[string]any{"type": []string{kind, "null"}, "description": description}
}
func toolDefinitions() []Tool {
	category := map[string]any{"type": "string", "enum": domain.Categories}
	amount := map[string]any{"type": "string", "description": "Positive amount as decimal string in rubles, e.g. 350.00. Never a float or exponent."}
	date := map[string]any{"type": "string", "description": "Calendar date YYYY-MM-DD in the configured timezone."}
	filters := object(map[string]any{
		"from": nullable("string", "Inclusive YYYY-MM-DD or null"), "to": nullable("string", "Inclusive YYYY-MM-DD or null"),
		"period":   map[string]any{"type": []string{"string", "null"}, "enum": []any{nil, "today", "yesterday", "this_week", "this_month", "last_7_days"}},
		"category": map[string]any{"type": []string{"string", "null"}, "enum": append([]any{nil}, stringValues(domain.Categories)...)},
		"query":    nullable("string", "Optional literal comment search, not SQL"), "page": nullable("integer", "Page number; null means 1"),
	})
	selectSchema := object(map[string]any{"id": nullable("integer", "Explicit expense ID chosen by user, or null"), "query": nullable("string", "Literal comment search or null"), "latest": nullable("boolean", "True only when user explicitly refers to latest/last expense; otherwise null")})
	makeTool := func(name, desc string, params map[string]any) Tool {
		return Tool{Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: params, Strict: true}}
	}
	return []Tool{
		makeTool("create_expense", "Save an expense for the current Telegram user. Classify category from text; use local current date unless specified.", object(map[string]any{"amount": amount, "category": category, "spent_at": date, "comment": map[string]any{"type": "string"}})),
		makeTool("list_expenses", "List current user's expenses; period is resolved on the server. Never use lists to calculate totals.", filters),
		makeTool("sum_expenses", "Calculate the exact total and count in the database, including all matching records.", filters),
		makeTool("get_expense", "Read a current-user expense by ID.", object(map[string]any{"id": map[string]any{"type": "integer"}})),
		makeTool("latest_expense", "Read exactly one most recently created expense of the current user, for corrections such as 'last was 450'. Always pass count=1.", object(map[string]any{"count": map[string]any{"type": "integer", "enum": []any{json.Number("1")}, "description": "Exactly one latest expense; must be 1."}})),
		makeTool("update_expense", "Change specified fields of exactly one expense. Exactly one of selector.id/query/latest must be set. Multiple matches require user clarification; do not choose a match yourself.", object(map[string]any{
			"selector": selectSchema, "amount": nullable("string", "New positive decimal amount or null"), "category": map[string]any{"type": []string{"string", "null"}, "enum": append([]any{nil}, stringValues(domain.Categories)...)}, "spent_at": nullable("string", "New YYYY-MM-DD or null"), "comment": nullable("string", "New literal comment (empty string clears it) or null"),
		})),
		makeTool("delete_expense", "Delete exactly one expense. Exactly one selector is required. Never guess among multiple matches or bulk-delete.", object(map[string]any{"selector": selectSchema})),
	}
}
func stringValues(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func decodeStrict(raw string, into any, required ...string) error {
	if len(raw) > 8000 {
		return fmt.Errorf("arguments exceed limit")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return fmt.Errorf("arguments must be a JSON object")
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("missing argument %s", key)
		}
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid trailing JSON")
	}
	return nil
}

type toolState struct {
	userID       int64
	userText     string
	chosenIDs    map[int64]bool
	latestChosen bool
	cache        map[string]string
	answers      []string
	problem      string
	mutated      bool
	destructive  bool
	ambiguous    bool
}
type clarification struct{ reply string }

func (e *clarification) Error() string { return e.reply }

func (e *Engine) makeFilter(a filterArgs) (domain.Filter, error) {
	if a.Period != "" && (a.From != "" || a.To != "") {
		return domain.Filter{}, fmt.Errorf("%w: use period or dates, not both", domain.ErrInvalid)
	}
	if a.Period != "" {
		var err error
		a.From, a.To, err = domain.Period(a.Period, e.cfg.Now(), e.cfg.Location)
		if err != nil {
			return domain.Filter{}, err
		}
	}
	if a.Page == 0 {
		a.Page = 1
	}
	f := domain.Filter{From: a.From, To: a.To, Category: a.Category, Query: a.Query, Page: a.Page, PageSize: 10}
	return f, domain.ValidateFilter(f)
}

func (e *Engine) resolve(ctx context.Context, s domain.Session, sel selector, st *toolState) (domain.Expense, error) {
	n := 0
	if sel.ID != 0 {
		n++
	}
	if sel.Query != "" {
		n++
	}
	if sel.Latest {
		n++
	}
	if n != 1 || sel.ID < 0 || utf8.RuneCountInString(sel.Query) > 200 || (sel.Query != "" && strings.TrimSpace(sel.Query) == "") {
		return domain.Expense{}, fmt.Errorf("%w: choose exactly one positive ID, nonempty search, or latest", domain.ErrInvalid)
	}
	if sel.ID > 0 {
		if !st.chosenIDs[sel.ID] {
			st.ambiguous = true
			st.problem = "Please specify the expense ID, for example #123."
			return domain.Expense{}, &clarification{st.problem}
		}
		return s.Get(ctx, sel.ID)
	}
	if sel.Latest {
		if !st.latestChosen {
			st.ambiguous = true
			st.problem = "Please specify the expense ID or explicitly say \"latest expense\"."
			return domain.Expense{}, &clarification{st.problem}
		}
		return s.Last(ctx)
	}
	// Extracting the user's literal query is allowed; inventing a more specific
	// phrase from prior tool results would silently choose an ambiguous target.
	if !strings.Contains(normalizedText(st.userText), normalizedText(sel.Query)) {
		st.ambiguous = true
		st.problem = "Please refine the description or specify the expense ID, for example #123."
		return domain.Expense{}, &clarification{st.problem}
	}
	f, err := e.makeFilter(filterArgs{Query: sel.Query, Page: 1})
	if err != nil {
		return domain.Expense{}, err
	}
	p, err := s.List(ctx, f)
	if err != nil {
		return domain.Expense{}, err
	}
	if p.Total == 0 {
		return domain.Expense{}, domain.ErrNotFound
	}
	if p.Total > 1 {
		st.ambiguous = true
		var b strings.Builder
		b.WriteString("Several expenses match. Please specify the expense ID:\n")
		for i, x := range p.Items {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "#%d — %s RUB, %s, %s, %s\n", x.ID, x.Amount.String(), domain.CategoryLabel(x.Category), x.SpentAt, preview(x.Comment, 80))
		}
		if p.Total > 5 {
			b.WriteString("Showing the first 5 matches. You can refine the description.")
		}
		st.problem = strings.TrimSpace(b.String())
		return domain.Expense{}, &clarification{st.problem}
	}
	if len(p.Items) != 1 {
		return domain.Expense{}, fmt.Errorf("inconsistent expense query")
	}
	// Scoped Get locks the row for the remainder of this transaction. Refresh
	// fields after a concurrent admin edit so a partial patch cannot overwrite it.
	x, err := s.Get(ctx, p.Items[0].ID)
	if err != nil {
		return domain.Expense{}, err
	}
	if !strings.Contains(strings.ToLower(x.Comment), strings.ToLower(sel.Query)) {
		return domain.Expense{}, fmt.Errorf("%w: expense no longer matches the description", domain.ErrNotFound)
	}
	return x, nil
}

func expenseText(prefix string, x domain.Expense) string {
	t := fmt.Sprintf("%s #%d: %s RUB · %s · %s", prefix, x.ID, x.Amount.String(), domain.CategoryLabel(x.Category), x.SpentAt)
	if x.Comment != "" {
		t += " · " + preview(x.Comment, 80)
	}
	return t
}

// execute returns a structured tool response. Database failures abort the whole
// transaction; invalid model arguments become a non-mutating tool error.
func (e *Engine) execute(ctx context.Context, s domain.Session, c ToolCall, st *toolState) (string, error) {
	var arg any
	var parseErr error
	switch c.Function.Name {
	case "create_expense":
		a := new(createArgs)
		parseErr = decodeStrict(c.Function.Arguments, a, "amount", "category", "spent_at", "comment")
		arg = a
	case "list_expenses", "sum_expenses":
		a := new(filterArgs)
		parseErr = decodeStrict(c.Function.Arguments, a, "from", "to", "period", "category", "query", "page")
		arg = a
	case "get_expense":
		a := new(getArgs)
		parseErr = decodeStrict(c.Function.Arguments, a, "id")
		arg = a
	case "latest_expense":
		a := new(latestArgs)
		parseErr = decodeStrict(c.Function.Arguments, a, "count")
		arg = a
	case "update_expense":
		a := new(updateArgs)
		parseErr = decodeStrict(c.Function.Arguments, a, "selector", "amount", "category", "spent_at", "comment")
		arg = a
	case "delete_expense":
		a := new(deleteArgs)
		parseErr = decodeStrict(c.Function.Arguments, a, "selector")
		arg = a
	default:
		parseErr = fmt.Errorf("unknown tool")
	}
	toolError := func(msg string) (string, error) {
		st.problem = msg
		if st.mutated {
			return "", &clarification{msg}
		}
		b, _ := json.Marshal(map[string]string{"error": msg})
		return string(b), nil
	}
	if parseErr == nil {
		parseErr = validateArguments(c.Function.Name, c.Function.Arguments)
	}
	if parseErr != nil {
		return toolError("Invalid tool arguments. Please clarify your request.")
	}
	normalized, _ := json.Marshal(arg)
	key := c.Function.Name + ":" + string(normalized)
	mutation := c.Function.Name == "create_expense" || c.Function.Name == "update_expense" || c.Function.Name == "delete_expense"
	if previous, ok := st.cache[key]; ok && mutation {
		return previous, nil
	}
	var data any
	var answer string
	var err error
	switch a := arg.(type) {
	case *createArgs:
		input := domain.Input{UserID: st.userID, Amount: a.Amount, Category: a.Category, SpentAt: a.SpentAt, Comment: a.Comment}
		if err = domain.ValidateInput(input); err != nil {
			return toolError("Check the positive amount, category, and expense date.")
		}
		var x domain.Expense
		x, err = s.Create(ctx, input)
		data = x
		if err == nil {
			st.mutated = true
			answer = expenseText("Added", x)
		}
	case *filterArgs:
		var f domain.Filter
		f, err = e.makeFilter(*a)
		if err != nil {
			return toolError("Check the period, category, and filter.")
		}
		if c.Function.Name == "sum_expenses" {
			var total domain.Summary
			total, err = s.Summary(ctx, f)
			data = total
			answer = fmt.Sprintf("Total: %s RUB. Expenses: %d.", total.Amount.String(), total.Count)
		} else {
			var p domain.Page
			p, err = s.List(ctx, f)
			for i := range p.Items {
				p.Items[i].Comment = preview(p.Items[i].Comment, 80)
			}
			data = p
			if len(p.Items) == 0 {
				answer = "No expenses match this request."
			} else {
				var lines []string
				for _, x := range p.Items {
					lines = append(lines, expenseText("Expense", x))
				}
				if int64(f.Page*f.PageSize) < p.Total {
					lines = append(lines, fmt.Sprintf("%d expenses in total. Showing page %d; ask for the next page.", p.Total, f.Page))
				}
				answer = strings.Join(lines, "\n")
			}
		}
	case *getArgs:
		if a.ID <= 0 {
			return toolError("Please specify a positive expense ID.")
		}
		var x domain.Expense
		x, err = s.Get(ctx, a.ID)
		data = x
		answer = expenseText("Expense", x)
	case *latestArgs:
		var x domain.Expense
		x, err = s.Last(ctx)
		data = x
		answer = expenseText("Latest expense", x)
	case *updateArgs:
		if a.Amount == nil && a.Category == nil && a.SpentAt == nil && a.Comment == nil {
			return toolError("Please specify what to change in the expense.")
		}
		if st.destructive {
			return "", &clarification{"You can update or delete only one expense per request. Please specify its ID."}
		}
		var x domain.Expense
		x, err = e.resolve(ctx, s, a.Selector, st)
		if err != nil {
			break
		}
		input := domain.Input{UserID: st.userID, Amount: x.Amount, Category: x.Category, SpentAt: x.SpentAt, Comment: x.Comment}
		if a.Amount != nil {
			input.Amount = *a.Amount
		}
		if a.Category != nil {
			input.Category = *a.Category
		}
		if a.SpentAt != nil {
			input.SpentAt = *a.SpentAt
		}
		if a.Comment != nil {
			input.Comment = *a.Comment
		}
		if err = domain.ValidateInput(input); err != nil {
			return toolError("Check the positive amount, category, and expense date.")
		}
		x, err = s.Update(ctx, x.ID, input)
		data = x
		if err == nil {
			st.mutated = true
			st.destructive = true
			answer = expenseText("Updated", x)
		}
	case *deleteArgs:
		if st.destructive {
			return "", &clarification{"You can update or delete only one expense per request. Please specify its ID."}
		}
		var x domain.Expense
		x, err = e.resolve(ctx, s, a.Selector, st)
		if err != nil {
			break
		}
		err = s.Delete(ctx, x.ID)
		data = map[string]any{"deleted_id": x.ID}
		if err == nil {
			st.mutated = true
			st.destructive = true
			answer = expenseText("Deleted", x)
		}
	}
	if err != nil {
		var clarify *clarification
		if errors.As(err, &clarify) {
			if st.mutated {
				return "", clarify
			}
			return toolError(clarify.reply)
		}
		if errors.Is(err, domain.ErrNotFound) {
			return toolError("Expense not found among your expenses.")
		}
		if errors.Is(err, domain.ErrInvalid) {
			return toolError("Invalid expense parameters.")
		}
		return "", fmt.Errorf("expense tool failed: %w", err)
	}
	result, _ := json.Marshal(map[string]any{"ok": true, "data": data})
	if mutation {
		st.cache[key] = string(result)
	}
	st.problem = ""
	st.answers = append(st.answers, answer)
	return string(result), nil
}

func normalizedText(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}
func preview(text string, max int) string {
	r := []rune(text)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return text
}

// Validate the same strict JSON schema advertised to the provider. Provider-side
// strict mode is useful, but it cannot replace validation at our trust boundary.
func validateArguments(name, raw string) error {
	var schema map[string]any
	for _, t := range toolDefinitions() {
		if t.Function.Name == name {
			schema = t.Function.Parameters
			break
		}
	}
	if schema == nil {
		return fmt.Errorf("unknown tool")
	}
	var value any
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return err
	}
	return validateSchema(value, schema)
}
func validateSchema(value any, schema map[string]any) error {
	types := []string{}
	switch t := schema["type"].(type) {
	case string:
		types = []string{t}
	case []string:
		types = t
	}
	matched := false
	for _, t := range types {
		switch t {
		case "null":
			matched = matched || value == nil
		case "string":
			_, ok := value.(string)
			matched = matched || ok
		case "boolean":
			_, ok := value.(bool)
			matched = matched || ok
		case "integer":
			n, ok := value.(json.Number)
			if ok {
				_, err := n.Int64()
				matched = matched || err == nil
			}
		case "object":
			_, ok := value.(map[string]any)
			matched = matched || ok
		}
	}
	if !matched {
		return fmt.Errorf("wrong argument type")
	}
	if allowed, ok := schema["enum"]; ok {
		found := false
		switch values := allowed.(type) {
		case []string:
			for _, x := range values {
				if value == x {
					found = true
				}
			}
		case []any:
			for _, x := range values {
				if value == x {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("invalid enum argument")
		}
	}
	if fields, ok := value.(map[string]any); ok {
		props := schema["properties"].(map[string]any)
		for _, key := range schema["required"].([]string) {
			if _, exists := fields[key]; !exists {
				return fmt.Errorf("missing %s", key)
			}
		}
		for key, v := range fields {
			prop, exists := props[key]
			if !exists {
				return fmt.Errorf("unknown argument %s", key)
			}
			if err := validateSchema(v, prop.(map[string]any)); err != nil {
				return err
			}
		}
	}
	return nil
}
