// Command reference writes the Markdown SDK reference to references/.
//
// go/doc reads each exported symbol and its doc comment, and go/doc/comment renders
// the comment as Markdown. This command only groups symbols by capability, using the
// labels in docs/capabilities.json, orders them like the PostHog/sdk-specs README,
// and writes front matter.
//
//	go run ./internal/cmd/reference --sdk-specs ../sdk-specs [--major 2]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/doc"
	"go/doc/comment"
	"go/parser"
	"go/printer"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const name, id = "PostHog Go SDK", "posthog-go"

var (
	first   = []string{"setup", "capture", "identify"}
	classes = []string{"FeatureFlagEvaluations"} // client types that get their own H2
	fset    = token.NewFileSet()
	pkg     *doc.Package
	mapped  = map[string]label{} // entry label to capability slug, "" for SDK utils
)

type entry struct {
	title, code, doc string
	children         []entry
}

type label struct {
	slug string
	next bool // from "next": the label may exist only on the next major
}

func main() {
	specs := flag.String("sdk-specs", "", "PostHog/sdk-specs checkout")
	major := flag.Int("major", 0, "target major, defaults to the declared one")
	flag.Parse()
	var files []*ast.File
	paths, _ := filepath.Glob("*.go")
	for _, p := range paths {
		if !strings.HasSuffix(p, "_test.go") {
			f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
			check(err)
			files = append(files, f)
		}
	}
	var labels map[string]any
	check(json.Unmarshal([]byte(read("docs/capabilities.json")), &labels))
	load(labels, false)
	var err error
	module := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(read("go.mod"), "\n", 2)[0], "module"))
	pkg, err = doc.NewFromFiles(fset, files, module)
	check(err)
	declared, _ := strconv.Atoi(strings.Split(strings.Trim(constant("Version"), `"`), ".")[0])
	if *major == 0 {
		*major = declared
	}
	// The next major's code lives on its own branch with its own module path, such as .../posthog-go/v2,
	// and reuses this getting-started page, so its install and import lines take that path.
	start := read("docs/getting-started.md")
	if module != "github.com/posthog/posthog-go" && !strings.Contains(start, module) {
		start = strings.ReplaceAll(start, "github.com/posthog/posthog-go", module)
	}
	body := append([]string{"## Getting started", nest(start)}, reference(capabilities(*specs), *major <= declared)...)
	title := fmt.Sprintf("%s %d.x", name, *major)
	if *major > declared {
		title += " (experimental)"
	}
	write(fmt.Sprintf("%s-%d.md", id, *major), frontMatter(title, body), body)
	if *major <= declared {
		write(id+"-reference.md", frontMatter(name, body), body)
	}
	if guides, _ := filepath.Glob("docs/migration-*.md"); len(guides) > 0 {
		slices.Reverse(guides)
		for i, g := range guides {
			guides[i] = strings.TrimSpace(read(g))
		}
		write(id+"-migration.md", "---\ntitle: \""+name+" migration guides\"\nsidebar: Docs\nshowTitle: true\n---", guides)
	}
}

// load reads docs/capabilities.json: slugs by label, a "utils" list, and a "next" object of the same shape.
func load(labels map[string]any, next bool) {
	for k, v := range labels {
		switch v := v.(type) {
		case map[string]any:
			load(v, true)
		case []any:
			for _, l := range v {
				mapped[l.(string)] = label{"", next}
			}
		default:
			mapped[k] = label{v.(string), next}
		}
	}
}

// capabilities returns each sdk-specs capability's slug and sentence-case title, in README order.
func capabilities(checkout string) (caps [][2]string) {
	acronyms := map[string]string{"id": "ID", "ai": "AI", "http": "HTTP", "mcp": "MCP"}
	for _, line := range strings.Split(read(filepath.Join(checkout, "README.md")), "\n") {
		cells := strings.Split(line, "|")
		if _, path, ok := strings.Cut(strings.Join(cells[min(2, len(cells)-1):], ""), "openspec/specs/"); ok && len(cells) > 2 {
			words := strings.Fields(strings.ToLower(cells[1]))
			for i, w := range words {
				if a, ok := acronyms[w]; ok {
					words[i] = a
				}
			}
			title := strings.Join(words, " ")
			caps = append(caps, [2]string{strings.Split(path, "/")[0], strings.ToUpper(title[:1]) + title[1:]})
		}
	}
	rank := func(slug string) int { return (slices.Index(first, slug) + len(first) + 1) % (len(first) + 1) }
	sort.SliceStable(caps, func(i, j int) bool { return rank(caps[i][0]) < rank(caps[j][0]) })
	return caps
}

// reference lists every symbol. On a next-major run, current is false and labels that no longer exist are skipped.
func reference(caps [][2]string, current bool) []string {
	var utils, types, own []entry
	embedded := map[string]bool{}
	for _, t := range pkg.Types {
		switch {
		case t.Name == "Client":
			utils = append(utils, methods(t, embedded)...)
			utils = append(utils, funcs(t.Funcs, "posthog.")...)
		case slices.Contains(classes, t.Name):
			own = append(own, typeEntry(t))
		default:
			types = append(types, typeEntry(t))
		}
	}
	utils = append(utils, funcs(pkg.Funcs, "posthog.")...)
	// An interface Client embeds keeps its type entry, because public signatures such as
	// NewSlogCaptureHandler take it. Its methods are listed with Client's.
	// A mapped symbol moves to its capability, from wherever go/doc listed it.
	tagged, found := map[string][]entry{}, map[string]bool{}
	untagged := func(list []entry) []entry {
		return slices.DeleteFunc(list, func(e entry) bool {
			found[e.title] = true
			slug := mapped[e.title].slug
			if slug != "" {
				tagged[slug] = append(tagged[slug], e)
			}
			return slug != ""
		})
	}
	for _, list := range []*[]entry{&utils, &types, &own} {
		for i := range *list {
			(*list)[i].children = untagged((*list)[i].children)
		}
		*list = untagged(*list)
	}
	for l, m := range mapped {
		if !found[l] && !m.next && current {
			log.Fatalf("docs/capabilities.json: %s names no symbol", l)
		}
	}
	for _, e := range utils {
		if _, ok := mapped[e.title]; !ok {
			log.Fatalf("docs/capabilities.json: map %s to a capability or list it under utils", e.title)
		}
	}
	var out []string
	for _, c := range caps {
		if list := tagged[c[0]]; list != nil {
			out = append(append(out, "## "+c[1]), render(list, 3)...)
			delete(tagged, c[0])
		}
	}
	for slug, list := range tagged {
		log.Fatalf("docs/capabilities.json: %s maps to %q, which is not an sdk-specs capability", list[0].title, slug)
	}
	out = append(append(out, "## SDK utils"), render(utils, 3)...)
	for _, group := range []struct {
		heading string
		values  []*doc.Value
	}{{"### Constants", pkg.Consts}, {"### Variables", pkg.Vars}} {
		if len(group.values) > 0 {
			out = append(out, group.heading)
		}
		for _, v := range group.values {
			out = append(out, block(v.Decl), markdown(v.Doc))
		}
	}
	for _, c := range own {
		out = append(append(out, "## `"+c.title+"`", c.code, c.doc), render(c.children, 3)...)
	}
	return append(append(out, "## Types"), render(types, 3)...)
}

// methods lists an interface's methods, including those of the interfaces it embeds.
func methods(t *doc.Type, embedded map[string]bool) (out []entry) {
	for _, field := range t.Decl.Specs[0].(*ast.TypeSpec).Type.(*ast.InterfaceType).Methods.List {
		if ident, ok := field.Type.(*ast.Ident); ok && len(field.Names) == 0 {
			embedded[ident.Name] = true
			for _, inner := range pkg.Types {
				if inner.Name == ident.Name {
					out = append(out, methods(inner, embedded)...)
				}
			}
		} else if len(field.Names) > 0 {
			sig := field.Names[0].Name + strings.TrimPrefix(source(field.Type), "func")
			out = append(out, entry{t.Name + "." + field.Names[0].Name, code(sig), markdown(field.Doc.Text()), nil})
		}
	}
	return out
}

// typeEntry renders a type with the constants and variables go/doc groups under it, such as an enum's values.
func typeEntry(t *doc.Type) entry {
	children := append(funcs(t.Funcs, "posthog."), funcs(t.Methods, t.Name+".")...)
	text := []string{markdown(t.Doc)}
	for _, v := range append(t.Consts, t.Vars...) {
		text = append(text, block(v.Decl), markdown(v.Doc))
	}
	return entry{t.Name, block(t.Decl), strings.TrimSpace(strings.Join(text, "\n\n")), children}
}

func funcs(list []*doc.Func, prefix string) (out []entry) {
	for _, f := range list {
		decl := *f.Decl
		decl.Doc, decl.Body = nil, nil
		out = append(out, entry{prefix + f.Name, code(source(&decl)), markdown(f.Doc), nil})
	}
	return out
}

func render(list []entry, level int) (out []string) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].title < list[j].title })
	for _, e := range list {
		out = append(append(out, strings.Repeat("#", level)+" `"+e.title+"`", e.code, e.doc), render(e.children, level+1)...)
	}
	return out
}

// markdown renders a doc comment as Markdown. Prose escapes <, because the site keeps raw HTML in a page
// and would read <key> as a tag. Code examples print as fenced blocks, tagged go only when they look like Go.
func markdown(text string) string {
	d := pkg.Parser().Parse(text)
	escape(d.Content)
	p := pkg.Printer()
	p.HeadingLevel = 5
	p.DocLinkURL = docLink
	var out []string
	var prose []comment.Block
	flush := func() {
		if len(prose) > 0 {
			// The printer escapes backticks so prose can't open a code span; doc comments use them for code.
			md := strings.ReplaceAll(string(p.Markdown(&comment.Doc{Content: prose, Links: d.Links})), "\\`", "`")
			out = append(out, strings.TrimSpace(md))
			prose = nil
		}
	}
	for _, b := range d.Content {
		if c, ok := b.(*comment.Code); ok {
			flush()
			src := strings.TrimRight(c.Text, "\n")
			out = append(out, "```"+language(src)+"\n"+src+"\n```")
		} else {
			prose = append(prose, b)
		}
	}
	flush()
	return strings.Join(out, "\n\n")
}

// language tags a code example go when it parses as Go, or looks like Go with placeholders such as
// NewWithConfig(...). Other examples, such as a list of default values, are text.
func language(src string) string {
	if _, err := parser.ParseExpr(src); err == nil {
		return "go"
	}
	for _, wrap := range []string{"package p\n%s", "package p\nfunc _() {\n%s\n}"} {
		if _, err := parser.ParseFile(token.NewFileSet(), "", fmt.Sprintf(wrap, src), 0); err == nil {
			return "go"
		}
	}
	if strings.ContainsAny(src, "({") || strings.Contains(src, ":=") {
		return "go"
	}
	return "text"
}

// docLink points a [Name] doc link at the heading the reference gives that symbol: Type.Method for a
// method, the type's name for a type, and posthog.Name for a function.
func docLink(l *comment.DocLink) string {
	if l.Recv != "" {
		return "#" + anchor(l.Recv+"."+l.Name)
	}
	for _, t := range pkg.Types {
		if t.Name == l.Name {
			return "#" + anchor(l.Name)
		}
	}
	return "#" + anchor("posthog."+l.Name)
}

var mdx = strings.NewReplacer("<", "&lt;")

func escape(blocks []comment.Block) {
	for _, b := range blocks {
		switch b := b.(type) {
		case *comment.Paragraph:
			for i, t := range b.Text {
				if s, ok := t.(comment.Plain); ok {
					b.Text[i] = comment.Plain(mdx.Replace(string(s)))
				}
			}
		case *comment.List:
			for _, item := range b.Items {
				escape(item.Content)
			}
		}
	}
}

func constant(want string) string {
	for _, v := range pkg.Consts {
		for _, s := range v.Decl.Specs {
			if s := s.(*ast.ValueSpec); s.Names[0].Name == want {
				return s.Values[0].(*ast.BasicLit).Value
			}
		}
	}
	log.Fatalf("no %s constant", want)
	return ""
}

// block prints a declaration with the comments of the fields and values go/doc kept, so the comments of
// unexported fields it filtered out don't show.
func block(decl *ast.GenDecl) string {
	d := *decl
	d.Doc = nil
	var kept []*ast.CommentGroup
	ast.Inspect(&d, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			kept = append(kept, n.Doc, n.Comment)
		case *ast.ValueSpec:
			kept = append(kept, n.Doc, n.Comment)
		case *ast.TypeSpec:
			kept = append(kept, n.Comment)
		}
		return true
	})
	kept = slices.DeleteFunc(kept, func(c *ast.CommentGroup) bool { return c == nil })
	// A filtered-out member leaves blank lines where it was, such as right after an interface's brace.
	lines := strings.Split(source(&printer.CommentedNode{Node: &d, Comments: kept}), "\n")
	var out []string
	for i, line := range lines {
		blank := strings.TrimSpace(line) == ""
		afterOpen := len(out) > 0 && strings.HasSuffix(out[len(out)-1], "{")
		beforeClose := i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "}")
		if !blank || (!afterOpen && !beforeClose) {
			out = append(out, line)
		}
	}
	return code(strings.Join(out, "\n"))
}

// source prints a node the way gofmt does: tabs indent, spaces align columns.
func source(node any) string {
	var buf bytes.Buffer
	check((&printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}).Fprint(&buf, fset, node))
	return buf.String()
}

func code(s string) string { return "```go\n" + s + "\n```" }

func frontMatter(title string, body []string) string {
	head := []string{"---", "title: " + strconv.Quote(title), "sidebar: Docs", "showTitle: true",
		"contentMaxWidthClass: max-w-full", "platformLogo: go", "tableOfContents:"}
	for _, s := range body {
		if h, ok := strings.CutPrefix(s, "## "); ok {
			head = append(head, "  - value: "+strconv.Quote(strings.ReplaceAll(h, "`", "")), "    url: "+anchor(h), "    depth: 0")
		}
	}
	return strings.Join(append(head, "---"), "\n")
}

// anchor is the github-slugger slug the docs site gives a heading.
func anchor(heading string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' {
			return '-'
		}
		if r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, strings.ToLower(heading))
}

// nest moves each heading of a hand-written page down one level and drops its H1.
func nest(page string) string {
	var out []string
	fenced := false
	for _, line := range strings.Split(strings.TrimSpace(page), "\n") {
		fenced = fenced != strings.HasPrefix(strings.TrimSpace(line), "```")
		if fenced || !strings.HasPrefix(line, "#") {
			out = append(out, line)
		} else if !strings.HasPrefix(line, "# ") {
			out = append(out, "#"+line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func read(path string) string {
	b, err := os.ReadFile(path)
	check(err)
	return string(b)
}

func write(file, front string, body []string) {
	// A fresh checkout has no references folder yet.
	check(os.MkdirAll("references", 0o755))
	// A symbol with no doc comment leaves an empty part, which would print as a run of blank lines.
	body = slices.DeleteFunc(slices.Clone(body), func(s string) bool { return strings.TrimSpace(s) == "" })
	check(os.WriteFile(filepath.Join("references", file), []byte(strings.Join(append([]string{front}, body...), "\n\n")+"\n"), 0o644))
	fmt.Println("wrote references/" + file)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
