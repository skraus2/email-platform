package templates

import (
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"regexp"
	"strings"
	texttemplate "text/template"
	"text/template/parse"
)

// Source limits (bytes).
const (
	MaxSubjectSource = 1000
	MaxBodySource    = 512 << 10
)

// Source is the template text of one version.
type Source struct {
	Subject string
	Text    string
	HTML    string // optional
}

// Compiled is a validated version, ready to render.
type Compiled struct {
	schema  Schema
	subject *texttemplate.Template
	text    *texttemplate.Template
	html    *htmltemplate.Template // nil without an HTML part
}

// Compile validates src against schema and the allowed language subset and
// prepares it for rendering. Errors are *Error with code template_invalid.
func Compile(src Source, schema Schema) (*Compiled, error) { return compile(src, schema, true) }

// compile applies the publish policy (limits, construct allowlist, HTML
// context rules) only when policy is set; parsing and the zero-value run
// always happen.
func compile(src Source, schema Schema, policy bool) (*Compiled, error) {
	for _, p := range []struct {
		part, src string
		max       int
	}{{"subject", src.Subject, MaxSubjectSource}, {"text", src.Text, MaxBodySource}, {"html", src.HTML, MaxBodySource}} {
		switch {
		case len(p.src) > p.max:
			return nil, invalidTemplate(p.part, "must be at most %d bytes", p.max)
		case strings.ContainsRune(p.src, 0):
			return nil, invalidTemplate(p.part, "must not contain NUL")
		}
	}
	if policy && src.Subject == "" {
		return nil, invalidTemplate("subject", "is required")
	}
	if policy && src.Text == "" {
		return nil, invalidTemplate("text", "is required (the text alternative of every email)")
	}

	c := &Compiled{schema: schema}
	var err error
	if c.subject, err = parseText("subject", src.Subject, schema, policy); err != nil {
		return nil, err
	}
	if c.text, err = parseText("text", src.Text, schema, policy); err != nil {
		return nil, err
	}
	if src.HTML != "" {
		h, err := htmltemplate.New("html").Option("missingkey=error").Parse(src.HTML)
		if err != nil {
			return nil, parseError("html", err)
		}
		if policy {
			if err := checkTree("html", h.Tree, len(h.Templates()), schema); err != nil {
				return nil, err
			}
			// Checked on the source and on the parsed tree (trim markers and
			// comments applied), so neither spelling hides a forbidden context.
			if err := checkHTMLContexts(src.HTML); err != nil {
				return nil, err
			}
			if err := checkHTMLContexts(h.Tree.Root.String()); err != nil {
				return nil, err
			}
		}
		c.html = h
	}
	// html/template computes its escaping on the first execution. Running
	// every part once with zero values surfaces escaping errors (ambiguous
	// URL or attribute contexts, branches ending in different contexts) now
	// instead of at send time; escaping does not depend on the data.
	zero := c.zeroData()
	for part, exec := range map[string]func(io.Writer, any) error{
		"subject": c.subject.Execute, "text": c.text.Execute, "html": c.executeHTML,
	} {
		if err := exec(io.Discard, zero); err != nil {
			return nil, invalidTemplate(part, "%s", cleanError(part, err))
		}
	}
	return c, nil
}

// Schema returns the variable schema the version was compiled with.
func (c *Compiled) Schema() Schema { return c.schema }

// HasHTML reports whether the version has an HTML part.
func (c *Compiled) HasHTML() bool { return c.html != nil }

// PrintsTopLevel reports whether the text part prints the variable name
// outside of any condition, so every rendering contains it.
func (c *Compiled) PrintsTopLevel(part, name string) bool {
	if part != "text" {
		return false
	}
	for _, n := range c.text.Root.Nodes {
		if a, ok := n.(*parse.ActionNode); ok && printsField(a, name) && len(a.Pipe.Cmds) == 1 {
			return true
		}
	}
	return false
}

// linkTail matches the end of the HTML right before a link target: an open
// <a> or <area> tag whose attribute being written is href.
var linkTail = regexp.MustCompile(`(?is)<(?:a|area)\b[^<>]*\shref\s*=\s*["']?$`)

// linkEscapers is the escaper chain html/template gives a value that is the
// whole of a URL attribute value.
var linkEscapers = [][]string{
	{"_html_template_urlfilter", "_html_template_urlnormalizer", "_html_template_attrescaper"},
	{"_html_template_urlfilter", "_html_template_urlnormalizer", "_html_template_nospaceescaper"},
}

// LinksTopLevel reports whether the HTML part uses the variable name as the
// complete target of an <a> or <area> link outside of any condition. The
// escaper chain alone cannot tell href from other URL-valued attributes
// (src, style url(...)), so the attribute is read from the HTML before the
// action. Hiding the link by styling is not detected.
func (c *Compiled) LinksTopLevel(name string) bool {
	if c.html == nil {
		return false
	}
	nodes := c.html.Tree.Root.Nodes
	for i, n := range nodes {
		a, ok := n.(*parse.ActionNode)
		if !ok || i == 0 || !printsField(a, name) || !escapedAs(a.Pipe.Cmds[1:], linkEscapers) {
			continue
		}
		if t, ok := nodes[i-1].(*parse.TextNode); ok && linkTail.Match(t.Text) {
			return true
		}
	}
	return false
}

// References reports whether part ("subject", "text" or "html") uses the
// variable name anywhere: printed, in a condition, in any branch.
func (c *Compiled) References(part, name string) bool {
	var root *parse.ListNode
	switch part {
	case "subject":
		root = c.subject.Root
	case "text":
		root = c.text.Root
	case "html":
		if c.html == nil {
			return false
		}
		root = c.html.Tree.Root
	default:
		return false
	}
	var walk func(n parse.Node) bool
	walk = func(n parse.Node) bool {
		switch n := n.(type) {
		case *parse.ListNode:
			if n == nil {
				return false
			}
			for _, child := range n.Nodes {
				if walk(child) {
					return true
				}
			}
		case *parse.ActionNode:
			return walk(n.Pipe)
		case *parse.IfNode:
			return walk(n.Pipe) || walk(n.List) || (n.ElseList != nil && walk(n.ElseList))
		case *parse.PipeNode:
			if n == nil {
				return false
			}
			for _, cmd := range n.Cmds {
				for _, arg := range cmd.Args {
					if walk(arg) {
						return true
					}
				}
			}
		case *parse.FieldNode:
			return len(n.Ident) > 0 && n.Ident[0] == name
		}
		return false
	}
	return walk(root)
}

// printsField reports whether the action prints the declared field name as
// its first command.
func printsField(a *parse.ActionNode, name string) bool {
	if a.Pipe == nil || len(a.Pipe.Cmds) == 0 || len(a.Pipe.Cmds[0].Args) != 1 {
		return false
	}
	f, ok := a.Pipe.Cmds[0].Args[0].(*parse.FieldNode)
	return ok && len(f.Ident) == 1 && f.Ident[0] == name
}

// escapedAs reports whether the commands are exactly one of the escaper
// chains.
func escapedAs(cmds []*parse.CommandNode, chains [][]string) bool {
	for _, chain := range chains {
		if len(cmds) != len(chain) {
			continue
		}
		match := true
		for k, c := range cmds {
			id, ok := (*parse.IdentifierNode)(nil), len(c.Args) == 1
			if ok {
				id, ok = c.Args[0].(*parse.IdentifierNode)
			}
			if !ok || id.Ident != chain[k] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (c *Compiled) executeHTML(w io.Writer, data any) error {
	if c.html == nil {
		return nil
	}
	return c.html.Execute(w, data)
}

func parseText(part, src string, schema Schema, policy bool) (*texttemplate.Template, error) {
	t, err := texttemplate.New(part).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, parseError(part, err)
	}
	if !policy {
		return t, nil
	}
	if err := checkTree(part, t.Tree, len(t.Templates()), schema); err != nil {
		return nil, err
	}
	return t, nil
}

func invalidTemplate(part, format string, args ...any) error {
	return fail(CodeTemplateInvalid, part, format, args...)
}

func parseError(part string, err error) error {
	return invalidTemplate(part, "%s", cleanError(part, err))
}

var errLocation = regexp.MustCompile(`^(?:html/)?template: ?(?:[a-z]+:)?([a-z]+):(\d+):(?:\d+:)?\s*`)

// cleanError turns "template: text:2: …" into "line 2: …". Escaping errors
// get their description without html/template's internal context dump.
func cleanError(part string, err error) string {
	var he *htmltemplate.Error
	if errors.As(err, &he) && he.ErrorCode != htmltemplate.OK {
		desc := he.Description
		switch he.ErrorCode {
		case htmltemplate.ErrBranchEnd:
			desc = "{{if}} branches end in different HTML contexts"
		case htmltemplate.ErrEndContext:
			desc = "the template ends inside a tag, attribute or other non-text context"
		}
		if he.Line == 0 {
			return desc
		}
		return fmt.Sprintf("line %d: %s", he.Line, desc)
	}
	msg := err.Error()
	if m := errLocation.FindStringSubmatch(msg); m != nil {
		return "line " + m[2] + ": " + msg[len(m[0]):]
	}
	return strings.TrimPrefix(strings.TrimPrefix(msg, "html/"), "template: ")
}

// checkTree enforces the allowed constructs (deny by default).
func checkTree(part string, tree *parse.Tree, templates int, schema Schema) error {
	if tree == nil || templates != 1 {
		return invalidTemplate(part, "must not define or call other templates")
	}
	w := walker{part: part, tree: tree, schema: schema}
	return w.list(tree.Root)
}

type walker struct {
	part   string
	tree   *parse.Tree
	schema Schema
}

func (w walker) fail(n parse.Node, format string, args ...any) error {
	loc, _ := w.tree.ErrorContext(n)
	line := "?"
	if i := strings.Index(loc, ":"); i >= 0 {
		rest := loc[i+1:]
		if j := strings.Index(rest, ":"); j >= 0 {
			line = rest[:j]
		}
	}
	return invalidTemplate(w.part, "line %s: %s", line, fmt.Sprintf(format, args...))
}

func (w walker) list(l *parse.ListNode) error {
	if l == nil {
		return nil
	}
	for _, n := range l.Nodes {
		if err := w.node(n); err != nil {
			return err
		}
	}
	return nil
}

func (w walker) node(n parse.Node) error {
	switch n := n.(type) {
	case *parse.TextNode, *parse.CommentNode:
		return nil
	case *parse.ActionNode:
		return w.action(n)
	case *parse.IfNode:
		if err := w.condition(n, n.Pipe); err != nil {
			return err
		}
		if err := w.list(n.List); err != nil {
			return err
		}
		return w.list(n.ElseList)
	case *parse.ListNode:
		return w.list(n)
	default:
		return w.fail(n, "only {{.name}}, {{if}}/{{else if}}/{{else}}/{{end}} and comments are allowed")
	}
}

// simplePipe reports whether p is one command without declarations.
func simplePipe(p *parse.PipeNode) bool {
	return p != nil && len(p.Decl) == 0 && !p.IsAssign && len(p.Cmds) == 1
}

// field returns the declared variable a node names ({{.name}}), if any.
func (w walker) field(n parse.Node) (string, Var, bool) {
	f, ok := n.(*parse.FieldNode)
	if !ok || len(f.Ident) != 1 {
		return "", Var{}, false
	}
	v, ok := w.schema[f.Ident[0]]
	return f.Ident[0], v, ok
}

func (w walker) action(n *parse.ActionNode) error {
	if !simplePipe(n.Pipe) || len(n.Pipe.Cmds[0].Args) != 1 {
		return w.fail(n, "an action may only print one declared variable, like {{.name}}")
	}
	if _, _, ok := w.field(n.Pipe.Cmds[0].Args[0]); !ok {
		return w.fail(n, "an action may only print one declared variable, like {{.name}}")
	}
	return nil
}

// condition accepts a declared string/boolean variable, or eq/ne of such a
// variable with one constant of the same type.
func (w walker) condition(n parse.Node, p *parse.PipeNode) error {
	const allowed = `a condition must be a declared string or boolean variable, or eq/ne of one with a constant of its type, like {{if eq .plan "pro"}}`
	if !simplePipe(p) {
		return w.fail(n, allowed)
	}
	args := p.Cmds[0].Args
	if len(args) == 1 {
		if _, v, ok := w.field(args[0]); ok && (v.Type == TypeString || v.Type == TypeBoolean) {
			return nil
		}
		return w.fail(n, allowed)
	}
	id, ok := args[0].(*parse.IdentifierNode)
	if !ok || (id.Ident != "eq" && id.Ident != "ne") || len(args) != 3 {
		return w.fail(n, allowed)
	}
	a, b := args[1], args[2]
	if _, _, isField := w.field(a); !isField {
		a, b = b, a
	}
	_, v, ok := w.field(a)
	if !ok {
		return w.fail(n, allowed)
	}
	switch b.(type) {
	case *parse.StringNode:
		ok = v.Type == TypeString
	case *parse.BoolNode:
		ok = v.Type == TypeBoolean
	default:
		ok = false
	}
	if !ok {
		return w.fail(n, allowed)
	}
	return nil
}

var errTooLarge = errors.New("output too large")

// html/template escapes these contexts but does not make them safe: a value
// right after "<" becomes a tag (with attributes), srcdoc holds a whole
// document, and content/http-equiv can trigger refreshes to any URL.
var (
	actionAsTagName    = regexp.MustCompile(`<(?:[/!?]\s*)?\{\{`)
	actionInRiskyValue = regexp.MustCompile(`(?i)<[a-z][^>]*[\s/](?:srcdoc|content|http-equiv)\s*=\s*(?:"[^"]*\{\{|'[^']*\{\{|[^\s>"']*\{\{)`)
)

func checkHTMLContexts(src string) error {
	for _, c := range []struct {
		re  *regexp.Regexp
		msg string
	}{
		{actionAsTagName, "an action must not follow < (dynamic tag names are not allowed)"},
		{actionInRiskyValue, "actions are not allowed in srcdoc, content or http-equiv attributes"},
	} {
		if loc := c.re.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			return invalidTemplate("html", "line %d: %s", line, c.msg)
		}
	}
	return nil
}
